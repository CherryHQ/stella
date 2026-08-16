package storagequal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
)

var (
	requiredIdentityEvidence = map[string]struct{}{
		"same_namespace_inode": {}, "distinct_client_root_objects": {}, "declared_independent_mounts": {},
	}
	requiredConformance = map[string]struct{}{
		"atomic_same_directory_rename": {}, "symlink_and_containment": {}, "modes_and_ownership": {},
		"advisory_lock_across_clients": {}, "atomic_append": {}, "concurrent_read_write_no_torn_records": {},
		"close_to_open_a_to_b": {}, "close_to_open_b_to_a": {}, "fsync_file_and_directory": {},
	}
)

// ParseAndValidateRecord decodes the canonical qualification schema and proves
// that every required evidence item supports the record's passing conclusion.
// Unknown fields are rejected so a newer schema cannot silently acquire V1
// authority through an older Stella binary.
func ParseAndValidateRecord(data []byte) (Record, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil {
		return Record{}, fmt.Errorf("decode qualification record: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Record{}, errors.New("qualification record must contain exactly one JSON value")
	}
	if err := ValidateRecord(record); err != nil {
		return Record{}, err
	}
	return record, nil
}

// ValidateRecord is the single semantic authority used by evidence installation
// and runtime admission.
func ValidateRecord(r Record) error {
	if r.SchemaVersion != 1 {
		return fmt.Errorf("qualification record schema_version %d is unsupported", r.SchemaVersion)
	}
	started, err := time.Parse(time.RFC3339Nano, r.StartedUTC)
	if err != nil || started.Location() != time.UTC {
		return errors.New("qualification record started_utc must be a UTC RFC3339 timestamp")
	}
	ended, err := time.Parse(time.RFC3339Nano, r.EndedUTC)
	if err != nil || ended.Location() != time.UTC || ended.Before(started) {
		return errors.New("qualification record ended_utc must be a UTC RFC3339 timestamp at or after started_utc")
	}
	if strings.TrimSpace(r.Backend) == "" || strings.TrimSpace(r.BackendVersion) == "" || strings.TrimSpace(r.Topology) == "" || r.Clients < 2 || r.Nodes < 1 || len(r.MountOptions) == 0 || strings.TrimSpace(r.NamespaceIdentity) == "" || strings.TrimSpace(r.IdentityMechanism) == "" || strings.TrimSpace(r.ReferenceHardware) == "" || !r.IndependentMounts {
		return errors.New("qualification record metadata is incomplete")
	}
	if !sort.StringsAreSorted(r.MountOptions) {
		return errors.New("qualification record mount options are not canonical")
	}
	for index, option := range r.MountOptions {
		if strings.TrimSpace(option) == "" || (index > 0 && option == r.MountOptions[index-1]) {
			return errors.New("qualification record mount options are empty or duplicated")
		}
	}
	if err := validateResults("identity evidence", r.IdentityEvidence, requiredIdentityEvidence); err != nil {
		return err
	}
	if err := validateResults("conformance", r.Conformance, requiredConformance); err != nil {
		return err
	}
	if err := validateLimits(r.Limits); err != nil {
		return err
	}
	if err := validateBenchmarks(r.Benchmarks, r.Limits); err != nil {
		return err
	}
	f := r.FailureInjection
	if !f.Injected || !f.DisconnectObserved || !f.Remounted || !f.Revalidated || !f.OutcomeUnknown || f.ErrorClass != "outcome_unknown" || strings.TrimSpace(f.Detail) == "" {
		return errors.New("qualification record failure injection evidence is incomplete or failed")
	}
	if r.Recovery.Name != "full_revalidation_after_remount" || !r.Recovery.Passed || r.Recovery.Detail != f.Detail {
		return errors.New("qualification record recovery evidence is incomplete or failed")
	}
	if len(r.Readiness) != 3 || r.Readiness[0] != (Transition{"not_ready", "qualification_started"}) || r.Readiness[1] != (Transition{"not_ready", "fault_injected"}) || r.Readiness[2] != (Transition{"ready", "full_contract_validated"}) {
		return errors.New("qualification record readiness transitions are incomplete or inconsistent")
	}
	if !r.QualifiedShared || !r.OverallPass {
		return errors.New("qualification record pass fields are inconsistent with required evidence")
	}
	return nil
}

func validateResults(kind string, results []Result, required map[string]struct{}) error {
	if len(results) != len(required) {
		return fmt.Errorf("qualification record %s set is incomplete", kind)
	}
	seen := make(map[string]struct{}, len(results))
	for _, result := range results {
		if _, ok := required[result.Name]; !ok {
			return fmt.Errorf("qualification record %s %q is unsupported", kind, result.Name)
		}
		if _, duplicate := seen[result.Name]; duplicate {
			return fmt.Errorf("qualification record %s %q is duplicated", kind, result.Name)
		}
		if !result.Passed {
			return fmt.Errorf("qualification record %s %q failed", kind, result.Name)
		}
		if kind == "identity evidence" && strings.TrimSpace(result.Detail) == "" {
			return fmt.Errorf("qualification record %s %q lacks detail", kind, result.Name)
		}
		seen[result.Name] = struct{}{}
	}
	return nil
}

func validateLimits(l Limits) error {
	if !positiveFinite(l.MetadataP95MS) || !positiveFinite(l.SmallFilesP95MS) || !positiveFinite(l.ConcurrentP95MS) || !positiveFinite(l.StreamMiBPerSecond) || l.MinimumFreeBytes <= 0 {
		return errors.New("qualification record benchmark limits are incomplete")
	}
	return nil
}

func positiveFinite(value float64) bool {
	return value > 0 && !math.IsInf(value, 0) && !math.IsNaN(value)
}

func validateBenchmarks(benchmarks []Benchmark, limits Limits) error {
	required := map[string]Benchmark{
		"typed_root_metadata_traversal":        {Unit: "ms_p95", Criterion: limits.MetadataP95MS, Comparison: "less_or_equal"},
		"small_file_project_skill_publication": {Unit: "ms_p95", Criterion: limits.SmallFilesP95MS, Comparison: "less_or_equal"},
		"large_upload_share_streaming":         {Unit: "MiB_per_second", Criterion: limits.StreamMiBPerSecond, Comparison: "greater_or_equal"},
		"concurrent_api_sandbox_access":        {Unit: "ms_p95", Criterion: limits.ConcurrentP95MS, Comparison: "less_or_equal"},
		"free_capacity":                        {Unit: "bytes", Criterion: float64(limits.MinimumFreeBytes), Comparison: "greater_or_equal"},
	}
	if len(benchmarks) != len(required) {
		return errors.New("qualification record benchmark set is incomplete")
	}
	seen := make(map[string]struct{}, len(benchmarks))
	for _, benchmark := range benchmarks {
		expected, ok := required[benchmark.Name]
		if !ok {
			return fmt.Errorf("qualification record benchmark %q is unsupported", benchmark.Name)
		}
		if _, duplicate := seen[benchmark.Name]; duplicate {
			return fmt.Errorf("qualification record benchmark %q is duplicated", benchmark.Name)
		}
		if benchmark.Unit != expected.Unit || benchmark.Criterion != expected.Criterion || benchmark.Comparison != expected.Comparison || !benchmark.Passed || math.IsInf(benchmark.Value, 0) || math.IsNaN(benchmark.Value) || benchmark.Value < 0 {
			return fmt.Errorf("qualification record benchmark %q is inconsistent or failed", benchmark.Name)
		}
		meets := benchmark.Value <= benchmark.Criterion
		if benchmark.Comparison == "greater_or_equal" {
			meets = benchmark.Value >= benchmark.Criterion
		}
		if !meets {
			return fmt.Errorf("qualification record benchmark %q does not meet its criterion", benchmark.Name)
		}
		seen[benchmark.Name] = struct{}{}
	}
	return nil
}
