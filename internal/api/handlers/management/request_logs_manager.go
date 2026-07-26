package management

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"time"
)

const (
	defaultRequestLogSyncInterval = 5 * time.Second
	defaultRequestLogBatchSize    = 100
)

type requestLogSyncStatus struct {
	Syncing       bool
	LastSyncedAt  time.Time
	LastSyncError string
}

type requestLogIndexManagerOptions struct {
	RetentionDays func() int
	ScanHook      func(context.Context) error
	BatchSize     int
	BatchHook     func(int)
	SyncInterval  time.Duration
	Now           func() time.Time
}

type requestLogIndexManager struct {
	dir           string
	store         *requestLogStore
	retentionDays func() int
	scanHook      func(context.Context) error
	batchSize     int
	batchHook     func(int)
	syncInterval  time.Duration
	now           func() time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	startOnce sync.Once
	closeOnce sync.Once
	trigger   chan struct{}

	statusMu sync.RWMutex
	status   requestLogSyncStatus
	closeErr error
}

type parsedRequestLogCandidate struct {
	parsed    parsedRequestLog
	candidate requestLogCandidate
}

func newRequestLogIndexManager(dir string, opts requestLogIndexManagerOptions) (*requestLogIndexManager, error) {
	store, err := openRequestLogStore(dir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	retentionDays := opts.RetentionDays
	if retentionDays == nil {
		retentionDays = func() int { return requestLogRetentionDays }
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = defaultRequestLogBatchSize
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &requestLogIndexManager{
		dir:           dir,
		store:         store,
		retentionDays: retentionDays,
		scanHook:      opts.ScanHook,
		batchSize:     batchSize,
		batchHook:     opts.BatchHook,
		syncInterval:  opts.SyncInterval,
		now:           now,
		ctx:           ctx,
		cancel:        cancel,
		trigger:       make(chan struct{}, 1),
	}, nil
}

func (m *requestLogIndexManager) Start() {
	if m == nil {
		return
	}
	m.startOnce.Do(func() {
		m.wg.Add(1)
		go m.run()
		m.TriggerSync()
	})
}

func (m *requestLogIndexManager) TriggerSync() {
	if m == nil {
		return
	}
	select {
	case <-m.ctx.Done():
		return
	default:
	}
	select {
	case m.trigger <- struct{}{}:
	default:
	}
}

func (m *requestLogIndexManager) Status() requestLogSyncStatus {
	if m == nil {
		return requestLogSyncStatus{}
	}
	m.statusMu.RLock()
	defer m.statusMu.RUnlock()
	return m.status
}

func (m *requestLogIndexManager) List(ctx context.Context, opts requestLogQueryOptions) ([]requestLogListItem, int, error) {
	if m == nil || m.store == nil {
		return nil, 0, fmt.Errorf("request log index unavailable")
	}
	return m.store.list(ctx, opts)
}

func (m *requestLogIndexManager) Detail(ctx context.Context, id string) (requestLogDetail, error) {
	if m == nil || m.store == nil {
		return requestLogDetail{}, fmt.Errorf("request log index unavailable")
	}
	cutoff := requestLogRetentionCutoff(m.now(), m.retentionDays())
	detail, err := m.store.detailWithCutoff(ctx, id, cutoff)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return detail, err
	}
	candidate, err := findRequestLogCandidateByIDWithCutoff(m.dir, id, requestLogCutoffTime(cutoff))
	if err != nil {
		if os.IsNotExist(err) {
			return requestLogDetail{}, sql.ErrNoRows
		}
		return requestLogDetail{}, err
	}
	parsed, err := parseRequestLogFile(candidate)
	if err != nil {
		return requestLogDetail{}, err
	}
	if err := m.store.upsertParsed(ctx, parsed, candidate); err != nil {
		return requestLogDetail{}, err
	}
	return m.store.detailWithCutoff(ctx, id, cutoff)
}

func (m *requestLogIndexManager) Export(ctx context.Context, w io.Writer, opts requestLogQueryOptions, format string) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("request log index unavailable")
	}
	return m.store.export(ctx, w, opts, format)
}

func (m *requestLogIndexManager) FailureDetails(ctx context.Context, provider string, limit int) ([]requestLogFailureDetail, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("request log index unavailable")
	}
	return m.store.failureDetails(ctx, provider, limit)
}

func (m *requestLogIndexManager) APIKeyUsageIdentityByAuthID(ctx context.Context, cutoff *int64) (map[string]apiKeyUsageHistoricalIdentity, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("request log index unavailable")
	}
	return m.store.apiKeyUsageIdentityByAuthID(ctx, cutoff)
}

func (m *requestLogIndexManager) APIKeyUsageByAuthID(ctx context.Context, now time.Time, cutoff *int64) (map[string]apiKeyUsageEntry, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("request log index unavailable")
	}
	return m.store.apiKeyUsageByAuthID(ctx, now, cutoff)
}

func (m *requestLogIndexManager) Close() error {
	if m == nil {
		return nil
	}
	m.closeOnce.Do(func() {
		m.cancel()
		m.wg.Wait()
		if m.store != nil {
			m.closeErr = m.store.close()
		}
	})
	return m.closeErr
}

func (m *requestLogIndexManager) run() {
	defer m.wg.Done()
	var ticker *time.Ticker
	var tick <-chan time.Time
	if m.syncInterval > 0 {
		ticker = time.NewTicker(m.syncInterval)
		tick = ticker.C
		defer ticker.Stop()
	}
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.trigger:
			m.runSync()
		case <-tick:
			m.runSync()
		}
	}
}

func (m *requestLogIndexManager) runSync() {
	m.statusMu.Lock()
	m.status.Syncing = true
	m.statusMu.Unlock()

	err := m.sync(m.ctx)
	completedAt := m.now()
	m.statusMu.Lock()
	m.status.Syncing = false
	if err != nil {
		m.status.LastSyncError = err.Error()
	} else {
		m.status.LastSyncedAt = completedAt
		m.status.LastSyncError = ""
	}
	m.statusMu.Unlock()
}

func (m *requestLogIndexManager) sync(ctx context.Context) error {
	if m.scanHook != nil {
		if err := m.scanHook(ctx); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	retentionDays := m.retentionDays()
	cutoff := requestLogRetentionCutoff(m.now(), retentionDays)
	cutoffTime := requestLogCutoffTime(cutoff)
	candidates, err := collectRequestLogCandidateMetadataWithCutoff(m.dir, cutoffTime)
	if err != nil {
		if os.IsNotExist(err) {
			candidates = nil
		} else {
			return err
		}
	}
	states, err := m.store.compactSyncStates(ctx)
	if err != nil {
		return err
	}

	seen := make(map[string]struct{}, len(candidates))
	changed := make([]parsedRequestLogCandidate, 0)
	parseErrors := make([]error, 0)
	for _, candidate := range candidates {
		id := requestLogIDFromFilename(candidate.name)
		seen[id] = struct{}{}
		if state, ok := states[id]; ok && state.size == candidate.size && state.modified == candidate.modTime.Unix() && !state.needsRefresh {
			continue
		}
		parsed, errParse := parseRequestLogFile(candidate)
		if errParse != nil {
			parseErrors = append(parseErrors, fmt.Errorf("parse %s: %w", candidate.name, errParse))
			continue
		}
		changed = append(changed, parsedRequestLogCandidate{parsed: parsed, candidate: candidate})
	}

	for start := 0; start < len(changed); start += m.batchSize {
		end := start + m.batchSize
		if end > len(changed) {
			end = len(changed)
		}
		batch := changed[start:end]
		if m.batchHook != nil {
			m.batchHook(len(batch))
		}
		if err := m.store.upsertParsedBatch(ctx, batch); err != nil {
			return err
		}
	}

	staleIDs := make([]string, 0)
	for id := range states {
		if _, ok := seen[id]; !ok {
			staleIDs = append(staleIDs, id)
		}
	}
	sort.Strings(staleIDs)
	for start := 0; start < len(staleIDs); start += m.batchSize {
		end := start + m.batchSize
		if end > len(staleIDs) {
			end = len(staleIDs)
		}
		if err := m.store.deleteIDs(ctx, staleIDs[start:end]); err != nil {
			return err
		}
	}
	if cutoffTime != nil {
		if err := m.store.pruneBefore(ctx, *cutoffTime); err != nil {
			return err
		}
	}
	return errors.Join(parseErrors...)
}
