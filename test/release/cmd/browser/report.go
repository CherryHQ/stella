//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

const rawReportSchemaVersion = 1

type scenarioDefinition struct {
	CapabilityID string
	ScenarioID   string
	Title        string
}

var browserScenarios = []scenarioDefinition{
	{CapabilityID: "C02", ScenarioID: "C02-S02", Title: "[C02-S02] authenticate through the browser"},
	{CapabilityID: "C05", ScenarioID: "C05-S02", Title: "[C05-S02] manage an agent and its user permissions"},
	{CapabilityID: "C06", ScenarioID: "C06-S02", Title: "[C06-S02] configure a provider and mask its secret"},
	{CapabilityID: "C07", ScenarioID: "C07-S03", Title: "[C07-S03] stream and restore a chat session"},
	{CapabilityID: "C17", ScenarioID: "C17-S02", Title: "[C17-S02] share and revoke an artifact"},
	{CapabilityID: "X02", ScenarioID: "X02-S02", Title: "[X02-S02] manage and invoke a webhook channel"},
	// The same real-candidate journey also proves the Webhook-specific inbound
	// lifecycle. X02 owns channel CRUD; X07 owns the inbound protocol behavior.
	{CapabilityID: "X07", ScenarioID: "X07-S02", Title: "[X02-S02] manage and invoke a webhook channel"},
}

type rawReport struct {
	SchemaVersion int             `json:"schema_version"`
	StartedAt     time.Time       `json:"started_at"`
	FinishedAt    time.Time       `json:"finished_at"`
	Status        string          `json:"status"`
	Tests         []rawTestResult `json:"tests"`
}

type rawTestResult struct {
	Title          string          `json:"title"`
	Status         string          `json:"status"`
	ExpectedStatus string          `json:"expected_status"`
	StartedAt      time.Time       `json:"started_at"`
	DurationMS     int64           `json:"duration_ms"`
	Error          string          `json:"error,omitempty"`
	Attachments    []rawAttachment `json:"attachments"`
}

type rawAttachment struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Path        string `json:"path,omitempty"`
}

type scenarioOutcome struct {
	Definition scenarioDefinition
	StartedAt  time.Time
	FinishedAt time.Time
	Status     releasecontract.Status
	Reason     string
	Paths      []string
}

func loadRawReport(path string) (rawReport, error) {
	file, err := os.Open(path)
	if err != nil {
		return rawReport{}, fmt.Errorf("open Playwright report: %w", err)
	}
	defer func() { _ = file.Close() }()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var report rawReport
	if err := decoder.Decode(&report); err != nil {
		return rawReport{}, fmt.Errorf("decode Playwright report: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return rawReport{}, fmt.Errorf("decode Playwright report: multiple JSON values are not allowed")
		}
		return rawReport{}, fmt.Errorf("decode Playwright report trailing content: %w", err)
	}
	if report.SchemaVersion != rawReportSchemaVersion {
		return rawReport{}, fmt.Errorf("playwright report schema_version must be %d", rawReportSchemaVersion)
	}
	if report.StartedAt.IsZero() || report.FinishedAt.IsZero() || report.FinishedAt.Before(report.StartedAt) {
		return rawReport{}, fmt.Errorf("playwright report has invalid timestamps")
	}
	return report, nil
}

func outcomesFromReport(
	report rawReport,
	attempt int,
	sharedPaths []string,
	harnessErr error,
) ([]scenarioOutcome, error) {
	definitions := make(map[string]struct{}, len(browserScenarios))
	for _, definition := range browserScenarios {
		definitions[definition.Title] = struct{}{}
	}

	seen := make(map[string]rawTestResult, len(report.Tests))
	var structuralErrors []string
	for _, testResult := range report.Tests {
		if _, known := definitions[testResult.Title]; !known {
			structuralErrors = append(structuralErrors, fmt.Sprintf("unexpected Playwright test %q", testResult.Title))
			continue
		}
		if _, duplicate := seen[testResult.Title]; duplicate {
			structuralErrors = append(structuralErrors, fmt.Sprintf("duplicate Playwright test %q", testResult.Title))
			continue
		}
		seen[testResult.Title] = testResult
	}

	outcomes := make([]scenarioOutcome, 0, len(browserScenarios))
	for _, definition := range browserScenarios {
		testResult, found := seen[definition.Title]
		outcome := scenarioOutcome{
			Definition: definition,
			StartedAt:  report.StartedAt.UTC(),
			FinishedAt: report.FinishedAt.UTC(),
			Status:     releasecontract.StatusProductFailure,
			Paths:      append([]string(nil), sharedPaths...),
		}
		if !found {
			outcome.Reason = "Playwright did not report the required Scenario"
			outcomes = append(outcomes, outcome)
			continue
		}
		if !testResult.StartedAt.IsZero() {
			outcome.StartedAt = testResult.StartedAt.UTC()
			outcome.FinishedAt = outcome.StartedAt.Add(time.Duration(testResult.DurationMS) * time.Millisecond)
		}
		for _, attachment := range testResult.Attachments {
			if attachment.Path != "" {
				outcome.Paths = append(outcome.Paths, attachment.Path)
			}
		}
		outcome.Paths = uniqueSorted(outcome.Paths)

		switch testResult.Status {
		case "passed":
			if harnessErr != nil {
				outcome.Reason = "browser harness failed after the Scenario passed: " + oneLine(harnessErr.Error())
				break
			}
			if attempt > 1 {
				outcome.Status = releasecontract.StatusFlaky
				outcome.Reason = fmt.Sprintf("release workflow attempt %d passed after a retry", attempt)
			} else {
				outcome.Status = releasecontract.StatusPass
				outcome.Reason = ""
			}
		case "failed", "timedOut", "interrupted", "skipped":
			outcome.Reason = fmt.Sprintf("Playwright status %s", testResult.Status)
			if message := oneLine(testResult.Error); message != "" {
				outcome.Reason += ": " + message
			}
		default:
			outcome.Reason = fmt.Sprintf("unsupported Playwright status %q", testResult.Status)
		}
		outcomes = append(outcomes, outcome)
	}

	if len(structuralErrors) > 0 {
		sort.Strings(structuralErrors)
		return outcomes, fmt.Errorf("%s", strings.Join(structuralErrors, "; "))
	}
	return outcomes, nil
}

func failureOutcomes(startedAt, finishedAt time.Time, reason string, paths []string) []scenarioOutcome {
	outcomes := make([]scenarioOutcome, 0, len(browserScenarios))
	for _, definition := range browserScenarios {
		outcomes = append(outcomes, scenarioOutcome{
			Definition: definition,
			StartedAt:  startedAt.UTC(),
			FinishedAt: finishedAt.UTC(),
			Status:     releasecontract.StatusProductFailure,
			Reason:     oneLine(reason),
			Paths:      uniqueSorted(paths),
		})
	}
	return outcomes
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func ensurePathBelow(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %s must stay below %s", path, root)
	}
	return nil
}
