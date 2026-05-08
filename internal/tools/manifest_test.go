package tools

import "testing"

func TestBinDir(t *testing.T) {
	got := BinDir("/home/user/.stella")
	if got != "/home/user/.stella/bin" {
		t.Fatalf("unexpected BinDir: %s", got)
	}
}
