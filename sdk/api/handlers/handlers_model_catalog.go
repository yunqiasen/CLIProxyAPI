package handlers

import (
	"bytes"
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

type modelCatalogCaptureKey struct{}

type catalogCaptureWriter struct {
	gin.ResponseWriter
	header  http.Header
	body    bytes.Buffer
	status  int
	written bool
}

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
	capture := &catalogCaptureWriter{header: make(http.Header), status: http.StatusOK}
	copied := c.Copy()
	copied.Writer = capture
	copied.Request = c.Request.Clone(context.WithValue(c.Request.Context(), modelCatalogCaptureKey{}, true))
	build(copied)
	return bytes.Clone(capture.body.Bytes()), capture.status
}
