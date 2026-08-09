package db_test

import (
	"context"
	"strings"
	"testing"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
)

func TestPreviousGADelegateActorBackfillAssemblesAsInformationOnly(t *testing.T) {
	database := appdb.PreviousGAUpgradedDBForTest(t)
	provider, err := lcm.New(database, nil, nil)
	if err != nil {
		t.Fatalf("create LCM provider: %v", err)
	}
	assembled, err := provider.Assemble(context.Background(), memory.Session{
		ID: "previous-ga-delegate", UserID: "00000000-0000-0000-0000-000000000001", AgentID: "previous-ga-agent",
		Channel: "delegate",
	}, 100_000, 1)
	if err != nil {
		t.Fatalf("assemble backfilled delegate: %v", err)
	}
	assembledText := make([]string, 0, len(assembled))
	for _, message := range assembled {
		assembledText = append(assembledText, memory.MessageText(message))
	}
	delegateContext := strings.Join(assembledText, "\n")
	for _, want := range []string{`"type":"agent"`, `"id":"previous-ga-agent"`, `"authority":"information_only"`, `"content":"legacy delegate input"`} {
		if !strings.Contains(delegateContext, want) {
			t.Fatalf("assembled backfilled delegate lost %s: %s", want, delegateContext)
		}
	}
}
