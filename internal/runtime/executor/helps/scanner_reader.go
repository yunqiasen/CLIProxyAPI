package helps

import (
	"bufio"
	"io"
	"strings"
)

// ScannerLineReader continues an existing line scanner without losing its read-ahead.
// The current token has already been consumed; subsequent tokens retain SSE separators.
type ScannerLineReader struct {
	Scanner *bufio.Scanner
	pending *strings.Reader
}

func (r *ScannerLineReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for r.pending == nil || r.pending.Len() == 0 {
		if !r.Scanner.Scan() {
			if err := r.Scanner.Err(); err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		r.pending = strings.NewReader(r.Scanner.Text() + "\n")
	}
	return r.pending.Read(p)
}
