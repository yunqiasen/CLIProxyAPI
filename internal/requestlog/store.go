package requestlog

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxAttempts = 128

type entry struct {
	record    Record
	attempts  int
	recovered bool
}

// Store bounds live metadata; durable history belongs to the existing log index.
type Store struct {
	mu        sync.Mutex
	capacity  int
	records   map[string]*entry
	version   uint64
	evictedAt uint64
	dropped   uint64
}

// Request is a context-scoped observation handle. Nil handles are inert.
type Request struct {
	store        *Store
	id           string
	attemptIndex int
	wire         bool
}

func NewStore(capacity int) *Store {
	if capacity <= 0 {
		capacity = 4096
	}
	if capacity > 65536 {
		capacity = 65536
	}
	return &Store{capacity: capacity, records: make(map[string]*entry)}
}

func (s *Store) Begin(record Record) *Request {
	if s == nil {
		return nil
	}
	record.ID = bounded(record.ID, 128)
	if record.ID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.records[record.ID]; exists {
		return nil
	}
	if len(s.records) >= s.capacity {
		var oldest *entry
		for _, r := range s.records {
			if r.record.State != Active && (oldest == nil || r.record.Version < oldest.record.Version) {
				oldest = r
			}
		}
		if oldest == nil {
			s.dropped++
			s.version++
			return nil
		}
		delete(s.records, oldest.record.ID)
		s.version++
		s.evictedAt = max(s.evictedAt, oldest.record.Version)
	}
	record.URL = cleanURL(record.URL)
	record.Model = bounded(record.Model, 256)
	record.IP = bounded(record.IP, 64)
	record.Method = bounded(record.Method, 16)
	record.Provider = bounded(record.Provider, 256)
	record.AuthID = credentialReference(record.AuthID)
	record.UpstreamModel = bounded(record.UpstreamModel, 256)
	record.SessionID = bounded(record.SessionID, 256)
	record.ParentSessionID = bounded(record.ParentSessionID, 256)
	record.TraceID = bounded(record.TraceID, 128)
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now()
	}
	record.State = Active
	record.Phase = "waiting"
	record.Status = 0
	record.Recovered = false
	record.ErrorKind = ""
	record.Attempts = nil
	record.AttemptsTruncated = false
	s.version++
	record.Version = s.version
	s.records[record.ID] = &entry{record: record}
	return &Request{store: s, id: record.ID}
}

func (s *Store) Get(id string) (Record, bool) {
	if s == nil {
		return Record{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok {
		return Record{}, false
	}
	return cloneRecord(r.record), true
}

// Changes never consumes records. Callers retain the cursor only after successful persistence.
func (s *Store) Changes(after uint64) ChangeSet {
	if s == nil {
		return ChangeSet{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := ChangeSet{Version: s.version, Dropped: s.dropped, Reset: after > s.version || (after < s.evictedAt), Records: []Record{}}
	for _, r := range s.records {
		if after == 0 || result.Reset || r.record.Version > after {
			result.Records = append(result.Records, cloneRecord(r.record))
		}
	}
	sort.Slice(result.Records, func(i, j int) bool { return result.Records[i].Version < result.Records[j].Version })
	return result
}

func cloneRecord(r Record) Record { r.Attempts = append([]Attempt{}, r.Attempts...); return r }

func (r *Request) update(fn func(*entry) bool) {
	if r == nil || r.store == nil {
		return
	}
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.records[r.id]
	if e == nil || e.record.State != Active {
		return
	}
	if fn(e) {
		s.version++
		e.record.Version = s.version
	}
}

// SelectRoute accounts for failures before an executor opens an upstream connection.
func (r *Request) SelectRoute(a Attempt) {
	r.update(func(e *entry) bool {
		r.attemptIndex = newAttempt(e, a)
		r.wire = false
		return true
	})
}

// BeginAttempt materializes a selected route or starts an executor-internal retry.
func (r *Request) BeginAttempt(a Attempt) {
	r.update(func(e *entry) bool {
		last := r.attempt(e)
		if last == nil || r.wire {
			r.attemptIndex = newAttempt(e, a)
		} else {
			if a.Provider != "" {
				last.Provider = bounded(a.Provider, 256)
			}
			if a.AuthID != "" {
				last.AuthID = credentialReference(a.AuthID)
			}
			if a.Model != "" {
				last.Model = bounded(a.Model, 256)
			}
			last.URL = cleanURL(a.URL)
			setRoute(&e.record, *last)
		}
		r.wire = true
		return true
	})
}

func newAttempt(e *entry, a Attempt) int {
	if n := len(e.record.Attempts); n > 0 {
		last := &e.record.Attempts[n-1]
		if last.State == Active {
			if last.Status >= 200 && last.Status < 400 {
				last.State = Succeeded
			} else {
				last.State = "retried"
			}
		}
		if last.State == Failed || last.State == "retried" {
			e.recovered = true
		}
	}
	e.attempts++
	a.Index = e.attempts
	a.State = Active
	a.Status = 0
	a.ErrorKind = ""
	a.Provider = bounded(a.Provider, 256)
	a.AuthID = credentialReference(a.AuthID)
	a.Model = bounded(a.Model, 256)
	a.URL = cleanURL(a.URL)
	if len(e.record.Attempts) < maxAttempts {
		e.record.Attempts = append(e.record.Attempts, a)
	} else {
		e.record.Attempts[maxAttempts-1] = a
		e.record.AttemptsTruncated = true
	}
	setRoute(&e.record, a)
	e.record.Phase = "routing"
	if e.attempts > 1 {
		e.record.Phase = "retrying"
	}
	return a.Index
}

// attempt is called only while the store lock is held. Missing truncated
// attempts deliberately ignore late callbacks rather than corrupting a new one.
func (r *Request) attempt(e *entry) *Attempt {
	for i := len(e.record.Attempts) - 1; i >= 0; i-- {
		if e.record.Attempts[i].Index == r.attemptIndex {
			return &e.record.Attempts[i]
		}
	}
	return nil
}

func setRoute(r *Record, a Attempt) {
	r.Provider = a.Provider
	r.AuthID = a.AuthID
	r.UpstreamModel = a.Model
}

func (r *Request) Response(status int) {
	r.update(func(e *entry) bool {
		a := r.attempt(e)
		if a == nil {
			return false
		}
		if a.Status == status {
			return false
		}
		a.Status = status
		if status >= 400 {
			a.State = Failed
			a.ErrorKind = Classify(status, nil)
			if a.Index < e.attempts {
				e.recovered = true
			}
		}
		return true
	})
}

func (r *Request) Failure(err error) {
	if err == nil {
		return
	}
	r.update(func(e *entry) bool {
		a := r.attempt(e)
		if a == nil {
			return false
		}
		a.State = Failed
		var statusError interface{ StatusCode() int }
		if errors.As(err, &statusError) && statusError.StatusCode() >= 400 {
			a.Status = statusError.StatusCode()
		}
		a.ErrorKind = Classify(a.Status, err)
		if a.Index < e.attempts {
			e.recovered = true
		}
		return true
	})
}

func (r *Request) Output() {
	r.update(func(e *entry) bool {
		a := r.attempt(e)
		if a == nil || a.Index != e.attempts || e.record.Phase == "responding" {
			return false
		}
		if a.State == Failed {
			return false
		}
		e.record.Phase = "responding"
		return true
	})
}

func (r *Request) Session(id, parent string) {
	r.update(func(e *entry) bool {
		id = bounded(id, 256)
		parent = bounded(parent, 256)
		if e.record.SessionID == id && e.record.ParentSessionID == parent {
			return false
		}
		e.record.SessionID = id
		e.record.ParentSessionID = parent
		return true
	})
}

// Finish seals a logical outcome; late usage or transport callbacks cannot rewrite it.
func (r *Request) Finish(state string, status int, err error) {
	r.update(func(e *entry) bool {
		switch state {
		case Succeeded, Failed, Canceled, Interrupted:
		default:
			state = Failed
		}
		var last *Attempt
		if n := len(e.record.Attempts); n > 0 {
			last = &e.record.Attempts[n-1]
		}
		if state == Succeeded && last != nil && last.State == Failed {
			state = Failed
		}
		e.record.State = state
		e.record.Recovered = state == Succeeded && e.recovered
		e.record.Phase = ""
		e.record.Status = status
		if state != Succeeded {
			e.record.ErrorKind = Classify(status, err)
			if e.record.ErrorKind == "unknown" && last != nil && last.ErrorKind != "" {
				e.record.ErrorKind = last.ErrorKind
			}
			if state == Canceled || state == Interrupted {
				e.record.ErrorKind = state
			}
		}
		if n := len(e.record.Attempts); n > 0 {
			a := &e.record.Attempts[n-1]
			if a.State == Active || state != Succeeded {
				a.State = state
				if state != Succeeded {
					a.ErrorKind = e.record.ErrorKind
				}
			}
			if a.Status == 0 {
				a.Status = status
			}
		}
		return true
	})
}

// Classify yields only a bounded category; upstream messages and secrets stay out of telemetry.
func Classify(status int, err error) string {
	text := ""
	if err != nil {
		text = strings.ToLower(err.Error())
	}
	switch {
	case errors.Is(err, context.Canceled):
		return Canceled
	case strings.Contains(text, "different") && strings.Contains(text, "resource"), strings.Contains(text, "encrypted"), strings.Contains(text, "invalid_signature"), strings.Contains(text, "orphan"), strings.Contains(text, "no tool call found"), strings.Contains(text, "previous_response"), strings.Contains(text, "reasoning_signature"):
		return "compatibility"
	case strings.Contains(text, "rate_limit"), status == 429:
		return "rate_limit"
	case strings.Contains(text, "insufficient_quota"), strings.Contains(text, "quota exceeded"), status == 402:
		return "quota"
	case status == 401, strings.Contains(text, "no auth available"), strings.Contains(text, "auth_unavailable"):
		return "authentication"
	case status == 403:
		return "permission"
	case errors.Is(err, context.DeadlineExceeded), strings.Contains(text, "connection"), strings.Contains(text, "dial "), strings.Contains(text, "tls "), strings.Contains(text, "eof"):
		return "connection"
	case status >= 500:
		return "upstream"
	default:
		return "unknown"
	}
}

func bounded(v string, n int) string {
	v = strings.TrimSpace(v)
	if len(v) > n {
		for n > 0 && !utf8.RuneStart(v[n]) {
			n--
		}
		return v[:n]
	}
	return v
}
func credentialReference(v string) string {
	if strings.Contains(v, "sk-") || strings.Contains(v, "Bearer ") {
		return "[credential]"
	}
	return bounded(v, 128)
}
func cleanURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return bounded(u.String(), 1024)
}
