package main

import (
	"context"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
)

type countingSnapshotLoader struct{ calls int }

func (l *countingSnapshotLoader) Snapshot(context.Context, string) (*config.Snapshot, error) {
	l.calls++
	return &config.Snapshot{}, nil
}

func TestMemorySummarizerWithoutAgentFailsBeforeCredentialResolution(t *testing.T) {
	loader := &countingSnapshotLoader{}
	summarize := buildMemorySummarizer(loader, nil)

	_, err := summarize(context.Background(), "private conversation")
	if err == nil || !strings.Contains(err.Error(), "no agent ID") {
		t.Fatalf("Summarize error = %v, want missing-agent failure", err)
	}
	if loader.calls != 0 {
		t.Fatalf("snapshot loader called %d times without an Agent identity", loader.calls)
	}
}
