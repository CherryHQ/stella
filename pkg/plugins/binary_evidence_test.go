package plugins

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseBinaryMiseListUsesActiveResolvedVersionOnly(t *testing.T) {
	requests := []BinaryEvidenceRequest{{Key: "uv", Lookup: "uv", PublicName: "uv", RequestedVersion: ">=0.5"}}
	evidence, err := ParseBinaryMiseList([]byte(`{"uv":[{"version":"0.5.9","requested_version":">=0.5","installed":true,"active":false},{"version":"0.6.14","requested_version":">=0.5","install_path":"/private/path","installed":true,"active":true}]}`), requests)
	if err != nil {
		t.Fatalf("ParseBinaryMiseList: %v", err)
	}
	if got, want := evidence.Tools[0].ResolvedVersion, "0.6.14"; got != want {
		t.Fatalf("resolved version = %q, want %q", got, want)
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("marshal evidence: %v", err)
	}
	if strings.Contains(string(data), "/private/path") {
		t.Fatalf("evidence leaked install path: %s", data)
	}
}
