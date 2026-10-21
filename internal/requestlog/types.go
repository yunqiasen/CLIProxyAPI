// Package requestlog observes request routing without controlling execution.
package requestlog

import "time"

const (
	Active      = "active"
	Succeeded   = "succeeded"
	Failed      = "failed"
	Canceled    = "canceled"
	Interrupted = "interrupted"
)

// Attempt is one selected route and its actual upstream exchange, not a token record.
type Attempt struct {
	Index     int    `json:"index"`
	Provider  string `json:"provider,omitempty"`
	AuthID    string `json:"auth_id,omitempty"`
	Model     string `json:"model,omitempty"`
	URL       string `json:"url,omitempty"`
	State     string `json:"state"`
	Status    int    `json:"status,omitempty"`
	ErrorKind string `json:"error_kind,omitempty"`
}

// Record is a logical model call. Multiple calls may share a transport TraceID.
type Record struct {
	ID                string    `json:"id"`
	TraceID           string    `json:"trace_id"`
	Timestamp         time.Time `json:"timestamp"`
	URL               string    `json:"url"`
	Method            string    `json:"method"`
	Model             string    `json:"model"`
	Provider          string    `json:"provider,omitempty"`
	AuthID            string    `json:"auth_id,omitempty"`
	UpstreamModel     string    `json:"upstream_model,omitempty"`
	IP                string    `json:"ip,omitempty"`
	SessionID         string    `json:"session_id,omitempty"`
	ParentSessionID   string    `json:"parent_session_id,omitempty"`
	State             string    `json:"state"`
	Phase             string    `json:"phase,omitempty"`
	Status            int       `json:"status,omitempty"`
	ErrorKind         string    `json:"error_kind,omitempty"`
	Recovered         bool      `json:"recovered"`
	Attempts          []Attempt `json:"attempts"`
	AttemptsTruncated bool      `json:"attempts_truncated,omitempty"`
	Version           uint64    `json:"-"`
}

// Scope is transport metadata supplied by middleware, never the request body or keys.
type Scope struct {
	TraceID string
	URL     string
	Method  string
	IP      string
}

// ChangeSet is a bounded, non-destructive snapshot for the index writer.
type ChangeSet struct {
	Version uint64
	Records []Record
	Dropped uint64
	Reset   bool
}
