package helps

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestWebsocketTimelineConcurrentRequestsRetainAuth(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", c)
	cfg := &config.Config{}
	cfg.RequestLog = true
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			RecordAPIWebsocketRequest(ctx, cfg, UpstreamRequestLog{AuthID: fmt.Sprintf("auth-%03d", i), Provider: "codex", Body: []byte(`{"model":"fixture"}`)})
		}(i)
	}
	close(start)
	wg.Wait()
	value, _ := c.Get(apiWebsocketTimelineKey)
	body, _ := value.([]byte)
	if count := strings.Count(string(body), "Event: api.websocket.request"); count != 64 {
		t.Fatalf("concurrent log records lost: got %d want 64", count)
	}
	for i := 0; i < 64; i++ {
		if !strings.Contains(string(body), fmt.Sprintf("auth-%03d", i)) {
			t.Errorf("missing auth %d", i)
		}
	}
}
