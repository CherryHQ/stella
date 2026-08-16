package storagequal

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func referenceRecord(t *testing.T) Record {
	t.Helper()
	data, err := os.ReadFile("../../docs/qualification/shared-posix/juicefs-ce-1.4.1-orb-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := ParseAndValidateRecord(data)
	if err != nil {
		t.Fatalf("actual Run-produced reference record rejected: %v", err)
	}
	return record
}

func cloneRecord(t *testing.T, record Record) Record {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var clone Record
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func TestParseAndValidateRecordRejectsMinimalRecord(t *testing.T) {
	minimal := []byte(`{"namespace_identity":"forged","qualified_shared":true,"overall_pass":true}`)
	if _, err := ParseAndValidateRecord(minimal); err == nil {
		t.Fatal("minimal fabricated record accepted")
	}
}

func TestValidateRecordRejectsPartialAndTamperedEvidence(t *testing.T) {
	valid := referenceRecord(t)
	tests := map[string]func(*Record){
		"unsupported schema":      func(r *Record) { r.SchemaVersion++ },
		"missing timestamp":       func(r *Record) { r.StartedUTC = "" },
		"missing metadata":        func(r *Record) { r.BackendVersion = "" },
		"missing identity":        func(r *Record) { r.IdentityEvidence = r.IdentityEvidence[:2] },
		"duplicate identity":      func(r *Record) { r.IdentityEvidence[1] = r.IdentityEvidence[0] },
		"duplicate conformance":   func(r *Record) { r.Conformance[1] = r.Conformance[0] },
		"failed conformance":      func(r *Record) { r.Conformance[0].Passed = false },
		"missing limit":           func(r *Record) { r.Limits.MetadataP95MS = 0 },
		"duplicate benchmark":     func(r *Record) { r.Benchmarks[1] = r.Benchmarks[0] },
		"tampered criterion":      func(r *Record) { r.Benchmarks[0].Criterion++ },
		"failed benchmark":        func(r *Record) { r.Benchmarks[0].Passed = false },
		"missing fault detail":    func(r *Record) { r.FailureInjection.Detail = "" },
		"failed recovery":         func(r *Record) { r.Recovery.Passed = false },
		"missing readiness":       func(r *Record) { r.Readiness = r.Readiness[:2] },
		"inconsistent pass field": func(r *Record) { r.OverallPass = false },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			record := cloneRecord(t, valid)
			mutate(&record)
			if err := ValidateRecord(record); err == nil {
				t.Fatal("tampered record accepted")
			}
		})
	}
}

func TestParseAndValidateRecordRejectsUnknownFields(t *testing.T) {
	data, err := json.Marshal(referenceRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	data = append(bytes.TrimSuffix(data, []byte("}")), []byte(`,"future_authority":true}`)...)
	if _, err := ParseAndValidateRecord(data); err == nil {
		t.Fatal("unknown qualification field accepted")
	}
}
