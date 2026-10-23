package cli

import (
	"strings"
	"testing"
)

func TestReadDownloadBodyNeverReturnsTruncatedSuccess(t *testing.T) {
	data, err := readDownloadBody(strings.NewReader("12345"), 5)
	if err != nil || string(data) != "12345" {
		t.Fatalf("exact limit: %q, %v", data, err)
	}
	data, err = readDownloadBody(strings.NewReader("123456"), 5)
	if err == nil || data != nil {
		t.Fatalf("oversize returned partial success: %q, %v", data, err)
	}
}
