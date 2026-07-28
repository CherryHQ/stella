// Package manual builds auditable release results for the concentrated manual
// check and for explicit waivers of eligible automatic outcomes.
package manual

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

var scenarioPattern = regexp.MustCompile(`^[CX][0-9]{2}-S[0-9]{2}$`)

// Scenario identifies one release_policy=manual manifest entry.
type Scenario struct {
	CapabilityID string
	ScenarioID   string
}

// Decision is the per-release reviewer input for one Manual Scenario.
type Decision struct {
	Status         string
	Reason         string
	Evidence       string
	OriginalStatus releasecontract.Status
}

// WaiverRequest asks the release owner to waive one eligible automatic
// outcome. Product Failure and missing_result can never be represented here.
type WaiverRequest struct {
	ScenarioID     string                 `json:"scenario_id"`
	OriginalStatus releasecontract.Status `json:"original_status"`
	Reason         string                 `json:"reason"`
	Evidence       string                 `json:"evidence"`
}

// EvidenceRecord is the non-secret audit artifact attached to a Manual or
// waiver Result.
type EvidenceRecord struct {
	SchemaVersion  int                    `json:"schema_version"`
	Run            releasecontract.Run    `json:"run"`
	CapabilityID   string                 `json:"capability_id"`
	ScenarioID     string                 `json:"scenario_id"`
	Status         releasecontract.Status `json:"status"`
	OriginalStatus releasecontract.Status `json:"original_status,omitempty"`
	Approver       string                 `json:"approver,omitempty"`
	Reason         string                 `json:"reason,omitempty"`
	Evidence       string                 `json:"evidence,omitempty"`
	RecordedAt     time.Time              `json:"recorded_at"`
}

// EnvPrefix returns the stable GitHub Environment variable prefix for one
// Manual Scenario, for example STELLA_MANUAL_X06_S02.
func EnvPrefix(scenarioID string) string {
	return "STELLA_MANUAL_" + strings.ReplaceAll(scenarioID, "-", "_")
}

// ReadDecision loads one Manual Scenario from its STATUS, REASON, EVIDENCE, and
// ORIGINAL_STATUS variables. It never reads unrelated environment variables.
func ReadDecision(scenarioID string, lookup func(string) (string, bool)) Decision {
	prefix := EnvPrefix(scenarioID)
	status, _ := lookup(prefix + "_STATUS")
	reason, _ := lookup(prefix + "_REASON")
	evidence, _ := lookup(prefix + "_EVIDENCE")
	original, _ := lookup(prefix + "_ORIGINAL_STATUS")
	return Decision{
		Status:         strings.ToLower(strings.TrimSpace(status)),
		Reason:         oneLine(reason),
		Evidence:       oneLine(evidence),
		OriginalStatus: releasecontract.Status(strings.ToLower(strings.TrimSpace(original))),
	}
}

// ParseWaiverRequests strictly decodes the optional Environment JSON array.
func ParseWaiverRequests(raw string) ([]WaiverRequest, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var requests []WaiverRequest
	if err := decoder.Decode(&requests); err != nil {
		return nil, fmt.Errorf("decode STELLA_RELEASE_WAIVERS_JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode STELLA_RELEASE_WAIVERS_JSON: multiple JSON values are not allowed")
		}
		return nil, fmt.Errorf("decode STELLA_RELEASE_WAIVERS_JSON trailing content: %w", err)
	}
	seen := map[string]struct{}{}
	for i, request := range requests {
		if !scenarioPattern.MatchString(request.ScenarioID) {
			return nil, fmt.Errorf("waivers[%d].scenario_id %q is invalid", i, request.ScenarioID)
		}
		if _, exists := seen[request.ScenarioID]; exists {
			return nil, fmt.Errorf("waiver Scenario %s is repeated", request.ScenarioID)
		}
		seen[request.ScenarioID] = struct{}{}
		switch request.OriginalStatus {
		case releasecontract.StatusExternalBlocked,
			releasecontract.StatusNotRun,
			releasecontract.StatusFlaky,
			releasecontract.StatusManualPending:
		default:
			return nil, fmt.Errorf(
				"waiver Scenario %s original_status %q is not waivable",
				request.ScenarioID,
				request.OriginalStatus,
			)
		}
		requests[i].Reason = oneLine(request.Reason)
		requests[i].Evidence = oneLine(request.Evidence)
		if requests[i].Reason == "" || requests[i].Evidence == "" {
			return nil, fmt.Errorf("waiver Scenario %s requires reason and evidence", request.ScenarioID)
		}
	}
	return requests, nil
}

// BuildManualResult converts one reviewer decision into the canonical result
// and an attached audit record. An empty status remains explicitly pending.
func BuildManualResult(
	run releasecontract.Run,
	scenario Scenario,
	decision Decision,
	approver string,
	attempt int,
	now time.Time,
) (releasecontract.Result, EvidenceRecord, error) {
	now = now.UTC()
	status := releasecontract.StatusManualPending
	reason := "manual decision is not configured in " + EnvPrefix(scenario.ScenarioID) + "_STATUS"
	var waiver *releasecontract.Waiver

	switch decision.Status {
	case "":
	case string(releasecontract.StatusPass):
		if strings.TrimSpace(approver) == "" || decision.Evidence == "" {
			return releasecontract.Result{}, EvidenceRecord{}, fmt.Errorf(
				"manual Pass for %s requires approver and evidence",
				scenario.ScenarioID,
			)
		}
		status = releasecontract.StatusPass
		reason = ""
	case string(releasecontract.StatusProductFailure):
		if decision.Reason == "" || decision.Evidence == "" {
			return releasecontract.Result{}, EvidenceRecord{}, fmt.Errorf(
				"manual Product Failure for %s requires reason and evidence",
				scenario.ScenarioID,
			)
		}
		status = releasecontract.StatusProductFailure
		reason = decision.Reason
	case string(releasecontract.StatusExternalBlocked),
		string(releasecontract.StatusNotRun),
		string(releasecontract.StatusManualPending):
		if decision.Reason == "" {
			return releasecontract.Result{}, EvidenceRecord{}, fmt.Errorf(
				"manual status %s for %s requires a reason",
				decision.Status,
				scenario.ScenarioID,
			)
		}
		status = releasecontract.Status(decision.Status)
		reason = decision.Reason
	case string(releasecontract.StatusWaived):
		if strings.TrimSpace(approver) == "" || decision.Reason == "" || decision.Evidence == "" {
			return releasecontract.Result{}, EvidenceRecord{}, fmt.Errorf(
				"manual waiver for %s requires approver, reason, and evidence",
				scenario.ScenarioID,
			)
		}
		switch decision.OriginalStatus {
		case releasecontract.StatusExternalBlocked,
			releasecontract.StatusNotRun,
			releasecontract.StatusManualPending:
		default:
			return releasecontract.Result{}, EvidenceRecord{}, fmt.Errorf(
				"manual waiver for %s has non-waivable original status %q",
				scenario.ScenarioID,
				decision.OriginalStatus,
			)
		}
		status = releasecontract.StatusWaived
		reason = "manual outcome waived: " + decision.Reason
		waiver = &releasecontract.Waiver{
			OriginalStatus: decision.OriginalStatus,
			Approver:       oneLine(approver),
			Reason:         decision.Reason,
			Commit:         run.Commit,
			ScenarioID:     scenario.ScenarioID,
			ApprovedAt:     now,
		}
	default:
		return releasecontract.Result{}, EvidenceRecord{}, fmt.Errorf(
			"manual Scenario %s has unsupported status %q",
			scenario.ScenarioID,
			decision.Status,
		)
	}

	result := releasecontract.Result{
		SchemaVersion: releasecontract.SchemaVersion,
		Run:           run,
		Platforms:     []releasecontract.Platform{{OS: "linux", Arch: "amd64"}},
		CapabilityID:  scenario.CapabilityID,
		ScenarioID:    scenario.ScenarioID,
		Runner:        releasecontract.Runner{Kind: releasecontract.RunnerManual, Name: "release-manual-gate"},
		Attempt:       attempt,
		StartedAt:     now,
		FinishedAt:    now,
		Status:        status,
		Reason:        reason,
		Waiver:        waiver,
	}
	evidence := EvidenceRecord{
		SchemaVersion:  1,
		Run:            run,
		CapabilityID:   scenario.CapabilityID,
		ScenarioID:     scenario.ScenarioID,
		Status:         status,
		OriginalStatus: decision.OriginalStatus,
		Approver:       oneLine(approver),
		Reason:         reason,
		Evidence:       decision.Evidence,
		RecordedAt:     now,
	}
	if err := result.Validate(); err != nil {
		return releasecontract.Result{}, EvidenceRecord{}, err
	}
	return result, evidence, nil
}

// BuildWaiverResult records an eligible automatic outcome as explicitly waived
// for the same commit and Scenario.
func BuildWaiverResult(
	original releasecontract.Result,
	request WaiverRequest,
	approver string,
	attempt int,
	now time.Time,
) (releasecontract.Result, EvidenceRecord, error) {
	if original.ScenarioID != request.ScenarioID {
		return releasecontract.Result{}, EvidenceRecord{}, fmt.Errorf("waiver Scenario does not match original result")
	}
	if original.Status != request.OriginalStatus {
		return releasecontract.Result{}, EvidenceRecord{}, fmt.Errorf(
			"waiver Scenario %s expects %s but current result is %s",
			request.ScenarioID,
			request.OriginalStatus,
			original.Status,
		)
	}
	if strings.TrimSpace(approver) == "" {
		return releasecontract.Result{}, EvidenceRecord{}, fmt.Errorf("waiver Scenario %s requires an approver", request.ScenarioID)
	}
	now = now.UTC()
	result := releasecontract.Result{
		SchemaVersion: releasecontract.SchemaVersion,
		Run:           original.Run,
		Platforms:     append([]releasecontract.Platform(nil), original.Platforms...),
		CapabilityID:  original.CapabilityID,
		ScenarioID:    original.ScenarioID,
		Runner:        releasecontract.Runner{Kind: releasecontract.RunnerManual, Name: "release-waiver-gate"},
		Attempt:       attempt,
		StartedAt:     now,
		FinishedAt:    now,
		Status:        releasecontract.StatusWaived,
		Reason:        "automatic outcome waived: " + request.Reason,
		Waiver: &releasecontract.Waiver{
			OriginalStatus: request.OriginalStatus,
			Approver:       oneLine(approver),
			Reason:         request.Reason,
			Commit:         original.Run.Commit,
			ScenarioID:     original.ScenarioID,
			ApprovedAt:     now,
		},
	}
	evidence := EvidenceRecord{
		SchemaVersion:  1,
		Run:            original.Run,
		CapabilityID:   original.CapabilityID,
		ScenarioID:     original.ScenarioID,
		Status:         releasecontract.StatusWaived,
		OriginalStatus: request.OriginalStatus,
		Approver:       oneLine(approver),
		Reason:         request.Reason,
		Evidence:       request.Evidence,
		RecordedAt:     now,
	}
	if err := result.Validate(); err != nil {
		return releasecontract.Result{}, EvidenceRecord{}, err
	}
	return result, evidence, nil
}

func oneLine(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	const maxValueLength = 500
	if len(value) > maxValueLength {
		return value[:maxValueLength] + "..."
	}
	return value
}
