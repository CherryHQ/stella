//go:build linux

package main

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
	if last.Definition.ScenarioID != "X07-S02" || last.Status != releasecontract.StatusProductFailure {
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

func TestFakeAnthropicFailsOneTurnAndRecovers(t *testing.T) {
	fake, err := startFakeAnthropic()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fake.Close(); err != nil {
			t.Errorf("close fake: %v", err)
		}
	})

	errorResponse, err := http.Post(fake.URL()+"/control/error", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = errorResponse.Body.Close()
	if errorResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("error control status = %d, want %d", errorResponse.StatusCode, http.StatusNoContent)
	}

	send := func(message string) (*http.Response, error) {
		request, requestErr := http.NewRequest(
			http.MethodPost,
			fake.URL()+"/v1/messages",
			strings.NewReader(`{"model":"claude-release-browser","messages":[{"role":"user","content":"`+message+`"}]}`),
		)
		if requestErr != nil {
			return nil, requestErr
		}
		request.Header.Set("Content-Type", "application/json")
		return http.DefaultClient.Do(request)
	}

	failed, err := send("failing turn")
	if err != nil {
		t.Fatal(err)
	}
	failedBody, err := io.ReadAll(failed.Body)
	_ = failed.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if failed.StatusCode != http.StatusInternalServerError || !strings.Contains(string(failedBody), fakeErrorMessage) {
		t.Fatalf("failed response = status %d body %q", failed.StatusCode, failedBody)
	}

	// The same logical turn must keep failing across Provider SDK retries.
	retried, err := send("failing turn")
	if err != nil {
		t.Fatal(err)
	}
	retriedBody, err := io.ReadAll(retried.Body)
	_ = retried.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if retried.StatusCode != http.StatusInternalServerError || !strings.Contains(string(retriedBody), fakeErrorMessage) {
		t.Fatalf("retried response = status %d body %q", retried.StatusCode, retriedBody)
	}

	recovered, err := send("recovery turn")
	if err != nil {
		t.Fatal(err)
	}
	recoveredBody, err := io.ReadAll(recovered.Body)
	_ = recovered.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if recovered.StatusCode != http.StatusOK || !strings.Contains(string(recoveredBody), fakeFullReply) {
		t.Fatalf("recovery response = status %d body %q", recovered.StatusCode, recoveredBody)
	}
	if summary := fake.Summary(); summary.FailedCalls != 2 || summary.MessageCalls != 3 {
		t.Fatalf("fake summary = %#v, want two failed attempts across three calls", summary)
	}
}

func TestFakeAnthropicSelectsGoalControlFromToolSchema(t *testing.T) {
	fake, err := startFakeAnthropic()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fake.Close(); err != nil {
			t.Errorf("close fake: %v", err)
		}
	})

	send := func(content string) string {
		t.Helper()
		body := `{
			"model":"claude-release-browser",
			"messages":[{"role":"user","content":` + content + `}],
			"tools":[{
				"name":"goal_control",
				"input_schema":{"properties":{"action":{"enum":["decompose","fail"]}}}
			}]
		}`
		request, requestErr := http.NewRequest(
			http.MethodPost,
			fake.URL()+"/v1/messages",
			strings.NewReader(body),
		)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		request.Header.Set("Content-Type", "application/json")
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		defer func() { _ = response.Body.Close() }()
		raw, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("goal response status = %d body %q", response.StatusCode, raw)
		}
		return string(raw)
	}

	initial := send(`"plan the goal"`)
	if !strings.Contains(initial, `"name":"goal_control"`) ||
		!strings.Contains(initial, `\"action\":\"decompose\"`) {
		t.Fatalf("initial goal response does not contain a decompose tool call: %q", initial)
	}

	followUp := send(`[{"type":"tool_result","tool_use_id":"toolu_test","content":"ok"}]`)
	if strings.Contains(followUp, `"name":"goal_control"`) || !strings.Contains(followUp, fakeFullReply) {
		t.Fatalf("tool-result follow-up must end with text, got %q", followUp)
	}
}

func TestFakeAnthropicServesDeterministicRSSFixture(t *testing.T) {
	fake, err := startFakeAnthropic()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fake.Close(); err != nil {
			t.Errorf("close fake: %v", err)
		}
	})

	response, err := http.Get(fake.URL() + "/fixtures/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK ||
		!strings.Contains(string(raw), "Release Browser Feed Entry") ||
		!strings.Contains(string(raw), fake.URL()+"/fixtures/article") {
		t.Fatalf("feed response = status %d body %q", response.StatusCode, raw)
	}
}

func TestScanBrowserArtifactsIncludesEphemeralProbe(t *testing.T) {
	t.Setenv("STELLA_RELEASE_SECRET_ENVS", "")
	root := t.TempDir()
	probe := "release-browser-ephemeral-probe"
	if err := os.WriteFile(filepath.Join(root, "network.json"), []byte("value="+probe), 0o644); err != nil {
		t.Fatal(err)
	}

	err := scanBrowserArtifacts(root, map[string]string{"STELLA_E2E_SECRET_PROBE": probe})
	if err == nil || !strings.Contains(err.Error(), "STELLA_E2E_SECRET_PROBE") {
		t.Fatalf("expected ephemeral probe detection, got %v", err)
	}
	if strings.Contains(err.Error(), probe) {
		t.Fatalf("probe value leaked in error: %v", err)
	}
}

func passingRawReport(startedAt time.Time) rawReport {
	report := rawReport{
		SchemaVersion: rawReportSchemaVersion,
		StartedAt:     startedAt,
		FinishedAt:    startedAt.Add(time.Minute),
		Status:        "passed",
	}
	seen := map[string]bool{}
	for index, definition := range browserScenarios {
		if seen[definition.Title] {
			continue
		}
		seen[definition.Title] = true
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
