package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhaseJournalAppendsOnlyFixedMode0600Entries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "phase.jsonl")
	journal, err := openPhaseJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.append(phaseDriverStart); err != nil {
		t.Fatal(err)
	}
	if err := journal.append(phaseStreamStart); err != nil {
		t.Fatal(err)
	}
	if err := journal.close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("phase journal mode = %v, want regular 0600", info.Mode())
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(mustReadPhaseJournal(t, path))), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		if len(entry) != 3 || entry["version"] != float64(1) || entry["timestamp"] == "" {
			t.Fatalf("phase entry = %#v, want fixed schema", entry)
		}
	}
}

func TestPhaseJournalRefusesExistingSymlinkAndUnknownPhase(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "phase.jsonl")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := openPhaseJournal(path); err == nil {
		t.Fatal("phase journal accepted a symlink")
	}

	journal, err := openPhaseJournal(filepath.Join(dir, "other.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.append(driverPhase("unbounded-detail")); err == nil {
		t.Fatal("phase journal accepted an unknown phase")
	}
	_ = journal.close()
}

func mustReadPhaseJournal(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
