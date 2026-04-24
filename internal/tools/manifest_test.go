package tools

import "testing"

func TestBinDir(t *testing.T) {
	got := BinDir("/home/user/.anna")
	if got != "/home/user/.anna/bin" {
		t.Fatalf("unexpected BinDir: %s", got)
	}
}
