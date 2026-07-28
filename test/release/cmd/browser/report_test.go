//go:build linux

package main

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

func TestOutcomesFromReportRequiresEveryScenario(t *testing.T) {
	startedAt := time.Date(2026, time.July, 28, 1, 2, 3, 0, time.UTC)
	report := passingRawReport(startedAt)
	report.Tests = report.Tests[:len(report.Tests)-1]

	outcomes, err := outcomesFromReport(report, 1, nil, nil)
	if err != nil {
		t.Fatalf("outcomesFromReport: %v", err)
	}
	if len(outcomes) != len(browserScenarios) {
		t.Fatalf("got %d outcomes, want %d", len(outcomes), len(browserScenarios))
	}
	last := outcomes[len(outcomes)-1]
	if last.Definition.ScenarioID != "X02-S02" || last.Status != releasecontract.StatusProductFailure {
		t.Fatalf("missing Scenario outcome = %#v", last)
	}
	if !strings.Contains(last.Reason, "did not report") {
		t.Fatalf("missing Scenario reason = %q", last.Reason)
	}
}

func TestOutcomesFromReportMarksRetrySuccessFlaky(t *testing.T) {
	report := passingRawReport(time.Date(2026, time.July, 28, 1, 2, 3, 0, time.UTC))
	outcomes, err := outcomesFromReport(report, 2, nil, nil)
	if err != nil {
		t.Fatalf("outcomesFromReport: %v", err)
	}
	for _, outcome := range outcomes {
		if outcome.Status != releasecontract.StatusFlaky {
			t.Errorf("%s status = %s, want flaky", outcome.Definition.ScenarioID, outcome.Status)
		}
		if outcome.Reason == "" {
			t.Errorf("%s flaky result has no reason", outcome.Definition.ScenarioID)
		}
	}
}

func TestOutcomesFromReportPropagatesHarnessFailure(t *testing.T) {
	report := passingRawReport(time.Date(2026, time.July, 28, 1, 2, 3, 0, time.UTC))
	outcomes, err := outcomesFromReport(report, 1, nil, errors.New("candidate cleanup failed"))
	if err != nil {
		t.Fatalf("outcomesFromReport: %v", err)
	}
	for _, outcome := range outcomes {
		if outcome.Status != releasecontract.StatusProductFailure {
			t.Errorf("%s status = %s, want product_failure", outcome.Definition.ScenarioID, outcome.Status)
		}
	}
}

func TestFakeAnthropicGatesOneRealSSETurn(t *testing.T) {
	fake, err := startFakeAnthropic()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fake.Close(); err != nil {
			t.Errorf("close fake: %v", err)
		}
	})

	gateResponse, err := http.Post(fake.URL()+"/control/gate", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = gateResponse.Body.Close()
	if gateResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("gate status = %d", gateResponse.StatusCode)
	}

	request, err := http.NewRequest(http.MethodPost, fake.URL()+"/v1/messages", strings.NewReader(`{"model":"claude-release-browser"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	reader := bufio.NewReader(response.Body)
	var before strings.Builder
	for !strings.Contains(before.String(), fakeFirstChunk) {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatalf("read gated prefix: %v", readErr)
		}
		before.WriteString(line)
	}
	if strings.Contains(before.String(), fakeFullReply) {
		t.Fatal("gated prefix already contains the full reply")
	}

	releaseResponse, err := http.Post(fake.URL()+"/control/release", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = releaseResponse.Body.Close()
	if releaseResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("release status = %d", releaseResponse.StatusCode)
	}
	after, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), fakeSecondChunk) {
		t.Fatalf("released suffix does not contain %q", fakeSecondChunk)
	}
}

func passingRawReport(startedAt time.Time) rawReport {
	report := rawReport{
		SchemaVersion: rawReportSchemaVersion,
		StartedAt:     startedAt,
		FinishedAt:    startedAt.Add(time.Minute),
		Status:        "passed",
	}
	for index, definition := range browserScenarios {
		report.Tests = append(report.Tests, rawTestResult{
			Title:          definition.Title,
			Status:         "passed",
			ExpectedStatus: "passed",
			StartedAt:      startedAt.Add(time.Duration(index) * time.Second),
			DurationMS:     100,
		})
	}
	return report
}
