package helps

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

func TestScannerLineReaderRetainsReadAhead(t *testing.T) {
	s := bufio.NewScanner(strings.NewReader("first\n\ndata: second\n\ndata: third\n\n"))
	if !s.Scan() || s.Text() != "first" {
		t.Fatal("missing first line")
	}
	got, err := io.ReadAll(&ScannerLineReader{Scanner: s})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "\ndata: second\n\ndata: third\n\n" {
		t.Fatalf("remaining stream: %q", got)
	}
}
