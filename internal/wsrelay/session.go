package wsrelay

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	readTimeout          = 60 * time.Second
	writeTimeout         = 10 * time.Second
	maxInboundMessageLen = 64 << 20 // 64 MiB
	heartbeatInterval    = 30 * time.Second
	pendingChannelBuffer = 64
)

var errClosed = errors.New("websocket session closed")

type pendingRequest struct {
	ch         chan Message
	done       chan struct{}
	initOnce   sync.Once
	closeOnce  sync.Once
	finishOnce sync.Once
	mu         sync.Mutex
	closed     bool
	finishing  bool
	terminal   bool
	reqCtx     context.Context
	stopCancel func() bool
	onBlocked  func()
}

func (pr *pendingRequest) initialize() {
	pr.initOnce.Do(func() {
		if pr.done == nil {
			pr.done = make(chan struct{})
		}
	})
}

func newPendingRequest(ctx context.Context) *pendingRequest {
	return &pendingRequest{
		ch:     make(chan Message, pendingChannelBuffer),
		done:   make(chan struct{}),
		reqCtx: ctx,
	}
}

func (pr *pendingRequest) ensureInitializedLocked() {
	if pr.ch == nil {
		pr.ch = make(chan Message, pendingChannelBuffer)
	}
}

func (pr *pendingRequest) deliver(sessClosed <-chan struct{}, msg Message) bool {
	if pr == nil {
		return false
	}
	pr.initialize()
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.ensureInitializedLocked()
	if pr.closed || pr.terminal || pr.finishing {
		return false
	}

	if pr.onBlocked != nil && len(pr.ch) == cap(pr.ch) {
		fn := pr.onBlocked
		pr.onBlocked = nil
		fn()
	}

	var ctxDone <-chan struct{}
	if pr.reqCtx != nil {
		ctxDone = pr.reqCtx.Done()
	}
	select {
	case <-ctxDone:
		return false
	case <-pr.done:
		return false
	case <-sessClosed:
		return false
	case pr.ch <- msg:
		return true
	}
}

func (pr *pendingRequest) deliverTerminal(sessClosed <-chan struct{}, msg Message) bool {
	if pr == nil {
		return false
	}
	pr.initialize()
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.ensureInitializedLocked()
	if pr.closed || pr.terminal || pr.finishing {
		return false
	}

	if pr.onBlocked != nil && len(pr.ch) == cap(pr.ch) {
		fn := pr.onBlocked
		pr.onBlocked = nil
		fn()
	}

	var ctxDone <-chan struct{}
	if pr.reqCtx != nil {
		ctxDone = pr.reqCtx.Done()
	}
	select {
	case <-ctxDone:
		return false
	case <-pr.done:
		return false
	case <-sessClosed:
		return false
	case pr.ch <- msg:
		pr.terminal = true
		return true
	}
}

func (pr *pendingRequest) setOnBlocked(fn func()) {
	if pr == nil {
		return
	}
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.onBlocked = fn
}

func (pr *pendingRequest) setStopCancel(stop func() bool) {
	if pr == nil {
		return
	}
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.stopCancel = stop
}

func (pr *pendingRequest) cancel() { pr.close() }

// cancelWithError preserves queued frames while adding a terminal error. It does
// not block session cleanup; explicit request cancellation still stops delivery.
func (pr *pendingRequest) cancelWithError(cause error) {
	if pr == nil {
		return
	}
	pr.finishOnce.Do(func() {
		pr.initialize()
		pr.mu.Lock()
		pr.ensureInitializedLocked()
		if pr.closed || pr.finishing {
			pr.mu.Unlock()
			return
		}
		if pr.terminal || cause == nil {
			pr.mu.Unlock()
			pr.close()
			return
		}
		pr.finishing = true
		pr.mu.Unlock()
		go func() {
			pr.mu.Lock()
			if !pr.closed {
				select {
				case pr.ch <- Message{Type: MessageTypeError, Payload: map[string]any{"error": cause.Error()}}:
					pr.terminal = true
				case <-pr.done:
				}
			}
			pr.mu.Unlock()
			pr.close()
		}()
	})
}

func (pr *pendingRequest) close() {
	if pr == nil {
		return
	}
	pr.initialize()
	pr.closeOnce.Do(func() {
		// Close done before taking mu so it unblocks a sender already waiting
		// under backpressure.
		close(pr.done)
		pr.mu.Lock()
		defer pr.mu.Unlock()
		pr.ensureInitializedLocked()
		pr.closed = true
		if pr.stopCancel != nil {
			pr.stopCancel()
		}
		close(pr.ch)
	})
}

type session struct {
	conn       *websocket.Conn
	manager    *Manager
	provider   string
	id         string
	closed     chan struct{}
	closeOnce  sync.Once
	finishOnce sync.Once
	writeMutex sync.Mutex
	pending    sync.Map // map[string]*pendingRequest
}

func newSession(conn *websocket.Conn, mgr *Manager, id string) *session {
	s := &session{
		conn:     conn,
		manager:  mgr,
		provider: "",
		id:       id,
		closed:   make(chan struct{}),
	}
	conn.SetReadLimit(maxInboundMessageLen)
	conn.SetReadDeadline(time.Now().Add(readTimeout))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		return nil
	})
	s.startHeartbeat()
	return s
}

func (s *session) startHeartbeat() {
	if s == nil || s.conn == nil {
		return
	}
	ticker := time.NewTicker(heartbeatInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-s.closed:
				return
			case <-ticker.C:
				s.writeMutex.Lock()
				err := s.conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(writeTimeout))
				s.writeMutex.Unlock()
				if err != nil {
					s.cleanup(err)
					return
				}
			}
		}
	}()
}

func (s *session) run(ctx context.Context) {
	defer s.cleanup(errClosed)
	for {
		var msg Message
		if err := s.conn.ReadJSON(&msg); err != nil {
			s.cleanup(err)
			return
		}
		s.dispatch(msg)
	}
}

func isTerminalMessage(msg Message) bool {
	return msg.Type == MessageTypeHTTPResp || msg.Type == MessageTypeError || msg.Type == MessageTypeStreamEnd
}

func (s *session) dispatch(msg Message) {
	if msg.Type == MessageTypePing {
		_ = s.send(context.Background(), Message{ID: msg.ID, Type: MessageTypePong})
		return
	}
	if value, loaded := s.pending.Load(msg.ID); loaded {
		req := value.(*pendingRequest)
		if isTerminalMessage(msg) {
			// LoadAndDelete only removes the request whose terminal is in
			// flight. Replacing the ID first prevents it from closing a newer
			// request that reused the same key.
			if actual, loaded := s.pending.Load(msg.ID); loaded && actual != req {
				return
			}
			if req.deliverTerminal(s.closed, msg) {
				s.pending.CompareAndDelete(msg.ID, req)
				req.close()
				return
			}
			cause := errClosed
			if req.reqCtx != nil && req.reqCtx.Err() != nil {
				cause = req.reqCtx.Err()
			} else if msg.Type == MessageTypeError {
				if errMsg, ok := msg.Payload["error"].(string); ok && errMsg != "" {
					cause = errors.New(errMsg)
				} else {
					cause = errors.New("wsrelay: upstream error")
				}
			}
			s.pending.CompareAndDelete(msg.ID, req)
			req.cancelWithError(cause)
			return
		}
		req.deliver(s.closed, msg)
		return
	}
	if isTerminalMessage(msg) {
		if s.manager != nil {
			s.manager.logDebugf("wsrelay: received terminal message for unknown id %s (provider=%s)", msg.ID, s.provider)
		}
	}
}

func (s *session) send(ctx context.Context, msg Message) error {
	select {
	case <-s.closed:
		return errClosed
	default:
	}
	s.writeMutex.Lock()
	defer s.writeMutex.Unlock()
	if err := s.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	if err := s.conn.WriteJSON(msg); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}

func (s *session) request(ctx context.Context, msg Message) (<-chan Message, error) {
	if msg.ID == "" {
		return nil, fmt.Errorf("wsrelay: message id is required")
	}
	req := newPendingRequest(ctx)
	if _, loaded := s.pending.LoadOrStore(msg.ID, req); loaded {
		req.close()
		return nil, fmt.Errorf("wsrelay: duplicate message id %s", msg.ID)
	}
	if ctx != nil {
		stop := context.AfterFunc(ctx, func() {
			s.pending.CompareAndDelete(msg.ID, req)
			req.cancel()
		})
		req.setStopCancel(stop)
	}
	if err := s.send(ctx, msg); err != nil {
		s.pending.CompareAndDelete(msg.ID, req)
		req.close()
		return nil, err
	}
	return req.ch, nil
}

func (s *session) cleanup(cause error) {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.pending.Range(func(key, value any) bool {
			if s.pending.CompareAndDelete(key, value) {
				value.(*pendingRequest).cancelWithError(cause)
			}
			return true
		})
		if s.conn != nil {
			_ = s.conn.Close()
		}
		if s.manager != nil {
			s.manager.handleSessionClosed(s, cause)
		}
	})
}
