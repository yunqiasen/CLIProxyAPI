package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/requestlog"
)

func TestRequestObservationDoesNotCountIdleWebsocketOrModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := requestlog.NewStore(8)
	router := gin.New()
	router.Use(logging.GinLogrusLogger(), RequestObservationMiddleware(s))
	router.GET("/v1/models", func(c *gin.Context) { c.JSON(200, gin.H{"data": []string{}}) })
	router.GET("/v1/responses", func(c *gin.Context) { c.Status(200) })
	for _, path := range []string{"/v1/models", "/v1/responses"} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
	}
	if records := s.Changes(0).Records; len(records) != 0 {
		t.Fatalf("non-inference traffic observed: %+v", records)
	}
}

func TestRequestObservationRecordsPreExecutionFailureAndProtectsTrace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := requestlog.NewStore(8)
	router := gin.New()
	router.Use(logging.GinLogrusLogger(), RequestObservationMiddleware(s))
	var trace string
	router.POST("/v1/responses", func(c *gin.Context) {
		trace = logging.GetGinRequestID(c)
		info, err := captureRequestInfo(c, false)
		if err != nil {
			t.Error(err)
			return
		}
		if info.Headers["X-CPA-Log-Trace-ID"][0] != trace {
			t.Error("server trace not used")
		}
		if c.Request.Header.Get("X-CPA-Log-Trace-ID") != "spoof" {
			t.Error("observer changed client headers")
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad model"})
	})
	req := httptest.NewRequest("POST", "/v1/responses", nil)
	req.Header.Set("X-CPA-Log-Trace-ID", "spoof")
	router.ServeHTTP(httptest.NewRecorder(), req)
	records := s.Changes(0).Records
	if len(records) != 1 || records[0].TraceID != trace || records[0].State != requestlog.Failed || records[0].Status != 400 {
		t.Fatalf("failure not tracked: %+v", records)
	}
}

func TestRequestObservationCopiesOnlyScopeIntoCallerContext(t *testing.T) {
	s := requestlog.NewStore(8)
	scoped := requestlog.WithScope(context.Background(), s, requestlog.Scope{TraceID: "trace"})
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	inherited := requestlog.InheritScope(parent, scoped)
	inherited, r := requestlog.Start(inherited, "call", "model")
	r.Output()
	r.Finish(requestlog.Canceled, 499, context.Canceled)
	if requestlog.FromContext(inherited) == nil {
		t.Fatal("scope lost")
	}
	cancel()
	if inherited.Err() != context.Canceled {
		t.Fatal("caller cancellation replaced")
	}
}
