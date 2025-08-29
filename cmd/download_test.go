package cmd

import (
	"testing"
)

// TestDownloadFile is a basic test to ensure DownloadFile handles empty input.
func TestDownloadFile_EmptyLink(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("DownloadFile should panic or log.Fatal on empty link")
		}
	}()
	DownloadFile("")
}
