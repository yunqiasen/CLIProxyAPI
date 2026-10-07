package handlers

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/gin-gonic/gin"
)

type modelCatalogCaptureKey struct{}

type catalogCaptureWriter struct {
	closed  chan bool
	header  http.Header
	body    bytes.Buffer
	status  int
	written bool
}

var _ gin.ResponseWriter = (*catalogCaptureWriter)(nil)

// Capture has no socket: optional writer operations must never touch the caller's response.
func (w *catalogCaptureWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("catalog capture does not support connection hijacking")
}
func (w *catalogCaptureWriter) CloseNotify() <-chan bool { return w.closed }
func (w *catalogCaptureWriter) Pusher() http.Pusher      { return nil }

func (w *catalogCaptureWriter) Header() http.Header { return w.header }
func (w *catalogCaptureWriter) WriteHeader(status int) {
	if !w.written {
		w.status = status
	}
}
func (w *catalogCaptureWriter) WriteHeaderNow() { w.written = true }
func (w *catalogCaptureWriter) Write(body []byte) (int, error) {
	w.written = true
	return w.body.Write(body)
}
func (w *catalogCaptureWriter) WriteString(body string) (int, error) { return w.Write([]byte(body)) }
func (w *catalogCaptureWriter) Status() int                          { return w.status }
func (w *catalogCaptureWriter) Size() int {
	if !w.written {
		return -1
	}
	return w.body.Len()
}
func (w *catalogCaptureWriter) Written() bool { return w.written }
func (w *catalogCaptureWriter) Flush()        { w.WriteHeaderNow() }

// CaptureModelCatalog reuses a catalog builder without applying presentation filters.
// Only trusted in-process management callers can create this context marker.
func CaptureModelCatalog(c *gin.Context, build gin.HandlerFunc) ([]byte, int) {
	capture := &catalogCaptureWriter{header: make(http.Header), status: http.StatusOK, closed: make(chan bool)}
	defer close(capture.closed)
	copied := c.Copy()
	copied.Writer = capture
	copied.Request = c.Request.Clone(context.WithValue(c.Request.Context(), modelCatalogCaptureKey{}, true))
	build(copied)
	return bytes.Clone(capture.body.Bytes()), capture.status
}
