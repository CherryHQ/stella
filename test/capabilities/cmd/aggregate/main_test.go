//go:build capability

package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

func TestParseSecretNamesAcceptsCommaAndWhitespace(t *testing.T) {
	got := parseSecretNames("MODEL_KEY, EMAIL_PASSWORD\nTELEGRAM_TOKEN")
	want := []string{"MODEL_KEY", "EMAIL_PASSWORD", "TELEGRAM_TOKEN"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSecretNames() = %v, want %v", got, want)
	}
}

func TestRunRejectsUnsafeTargetBeforeUsingItsDirectory(t *testing.T) {
	_, _, _, err := run(
		t.TempDir(),
		"missing-manifest.yaml",
		releasecontract.Run{
			ID:      "../outside",
			Version: "v1.2.3",
			Commit:  "0123456789abcdef0123456789abcdef01234567",
		},
		nil,
		time.Now(),
	)
	if err == nil || !strings.Contains(err.Error(), "validate release target") {
		t.Fatalf("expected target validation before filesystem access, got %v", err)
	}
}
