package wsrelay

import (
	"testing"
	"time"
)

func TestSessionDeliversTerminalAfterFullQueue(t *testing.T) {
	s := &session{closed: make(chan struct{})}
	req := &pendingRequest{ch: make(chan Message, 8)}
	s.pending.Store("r", req)
	for i := 0; i < 8; i++ {
		s.dispatch(Message{ID: "r", Type: MessageTypeStreamChunk})
	}
	sent := make(chan struct{})
	go func() { s.dispatch(Message{ID: "r", Type: MessageTypeStreamEnd}); close(sent) }()
	// A terminal send must wait for capacity, rather than silently disappear.
	select {
	case <-sent:
		t.Fatal("terminal dispatch completed while queue remained full")
	case <-time.After(20 * time.Millisecond):
	}
	count, terminal := 0, false
	for msg := range req.ch {
		count++
		terminal = terminal || msg.Type == MessageTypeStreamEnd
	}
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("dispatch stuck")
	}
	if count != 9 || !terminal {
		t.Fatalf("received=%d terminal=%v; terminal lost behind full queue", count, terminal)
	}
}

func TestPendingRequestCancellationUnblocksFullQueue(t *testing.T) {
	req := &pendingRequest{ch: make(chan Message, 1)}
	req.deliver(Message{Type: MessageTypeStreamChunk})
	finished := make(chan struct{})
	go func() { req.deliver(Message{Type: MessageTypeStreamEnd}); close(finished) }()
	req.close()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("cancellation left blocked writer")
	}
	for range req.ch {
	}
}

func TestSessionCleanupRetainsQueuedOutputAndError(t *testing.T) {
	s := &session{closed: make(chan struct{})}
	req := &pendingRequest{ch: make(chan Message, 1)}
	s.pending.Store("r", req)
	req.deliver(Message{ID: "r", Type: MessageTypeStreamChunk})
	s.cleanup(errClosed)
	first := <-req.ch
	if first.Type != MessageTypeStreamChunk {
		t.Fatal("queued output lost")
	}
	select {
	case last := <-req.ch:
		if last.Type != MessageTypeError {
			t.Fatal("terminal error lost")
		}
	case <-time.After(time.Second):
		t.Fatal("missing terminal")
	}
	if _, ok := <-req.ch; ok {
		t.Fatal("channel open after terminal")
	}
}

func TestCanceledTerminalDoesNotCloseReusedRequestID(t *testing.T) {
	s := &session{closed: make(chan struct{})}
	old := &pendingRequest{ch: make(chan Message, 1)}
	old.deliver(Message{Type: MessageTypeStreamChunk})
	s.pending.Store("same", old)
	done := make(chan struct{})
	go func() { s.dispatch(Message{ID: "same", Type: MessageTypeStreamEnd}); close(done) }()
	// Wait until dispatch is blocked on the old full channel.
	deadline := time.After(time.Second)
	for {
		if !old.mu.TryLock() {
			break
		}
		old.mu.Unlock()
		select {
		case <-deadline:
			t.Fatal("dispatch never blocked")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	newer := &pendingRequest{ch: make(chan Message, 1)}
	newer.initialize()
	s.pending.Store("same", newer)
	old.close()
	<-done
	value, ok := s.pending.Load("same")
	if !ok || value != newer {
		t.Fatal("terminal removed replacement request")
	}
	select {
	case <-newer.done:
		t.Fatal("terminal closed replacement request")
	default:
	}
	newer.close()
}
