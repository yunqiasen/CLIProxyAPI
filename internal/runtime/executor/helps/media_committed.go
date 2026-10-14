package helps

import (
	"errors"
	"fmt"
	"net/http"
)

// MediaCommittedError stops every retry layer after a successful media submission.
// A poll or result-decoding failure does not make creating a second task safe.
type MediaCommittedError struct {
	TaskID string
	Cause  error
}

func (e *MediaCommittedError) Error() string {
	if e.TaskID != "" {
		return fmt.Sprintf("media task %s accepted; result failed: %v", e.TaskID, e.Cause)
	}
	return fmt.Sprintf("media request accepted; result failed: %v", e.Cause)
}
func (e *MediaCommittedError) Unwrap() error         { return e.Cause }
func (e *MediaCommittedError) IsRequestStop() bool   { return true }
func (e *MediaCommittedError) IsRequestScoped() bool { return true }
func (e *MediaCommittedError) StatusCode() int {
	var status interface{ StatusCode() int }
	if errors.As(e.Cause, &status) && status.StatusCode() > 0 {
		return status.StatusCode()
	}
	return http.StatusBadGateway
}
