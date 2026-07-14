//go:build reflecteval

package reflect

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
)

type singletonReconciliationEvalCase struct {
	CaseID                        string                 `json:"case_id"`
	Subject                       factSubject            `json:"subject"`
	CurrentContent                string                 `json:"current_content"`
	CandidateContent              string                 `json:"candidate_content"`
	Constraints                   []string               `json:"constraints,omitempty"`
	ExpectedOperation             singletonFactOperation `json:"expected_operation"`
	RequiredContent               []string               `json:"required_content,omitempty"`
	ForbiddenContent              []string               `json:"forbidden_content,omitempty"`
	RequireConstraintConflictNote bool                   `json:"require_constraint_conflict_note,omitempty"`
}

type singletonReconciliationEvalResult struct {
	CaseID   string                 `json:"case_id"`
	Subject  factSubject            `json:"subject"`
	Expected singletonFactOperation `json:"expected_operation"`
	Plan     factReconciliationPlan `json:"plan"`
	Failures []string               `json:"failures,omitempty"`
	Error    string                 `json:"error,omitempty"`
}

func loadSingletonReconciliationEvalCases(path string) ([]singletonReconciliationEvalCase, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cases []singletonReconciliationEvalCase
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 32*1024), 1024*1024)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var tc singletonReconciliationEvalCase
		if err := json.Unmarshal([]byte(line), &tc); err != nil {
			return nil, fmt.Errorf("decode %s:%d: %w", path, lineNo, err)
		}
		if err := validateSingletonReconciliationEvalCase(tc); err != nil {
			return nil, fmt.Errorf("validate %s:%d: %w", path, lineNo, err)
		}
		cases = append(cases, tc)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("no singleton reconciliation eval cases in %s", path)
	}
	return cases, nil
}

func validateSingletonReconciliationEvalCase(tc singletonReconciliationEvalCase) error {
	if strings.TrimSpace(tc.CaseID) == "" || strings.TrimSpace(tc.CandidateContent) == "" {
		return fmt.Errorf("case_id and candidate_content are required")
	}
	if tc.Subject != factSubjectUser && tc.Subject != factSubjectAgent {
		return fmt.Errorf("unsupported singleton subject %q", tc.Subject)
	}
	if tc.ExpectedOperation != singletonOperationNoop && tc.ExpectedOperation != singletonOperationCreate && tc.ExpectedOperation != singletonOperationReplace {
		return fmt.Errorf("unsupported expected operation %q", tc.ExpectedOperation)
	}
	return nil
}

func singletonReconciliationEvalDatasetPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "internal", "reflect", "testdata", "reconciliation_eval", "profile_soul.jsonl")
}

func buildSingletonReconciliationEvalBundle(tc singletonReconciliationEvalCase) factRelatedBundle {
	candidate := factCandidate{
		Ref:     "fact-0001",
		Subject: tc.Subject,
		Content: tc.CandidateContent,
		Evidence: []factEvidence{{
			SourceType: singletonEvalEvidenceSource(tc.Subject),
			Source:     tc.CandidateContent,
			Reason:     "Direct durable user-provided delta for reconciliation evaluation.",
		}},
		ExpectedEffect: "Preserve the durable delta without dropping unrelated singleton content.",
	}
	bundle := factRelatedBundle{
		Knowledge: knowledgeRelatedBundle{Limits: relatedBundleLimits{MaxRelatedPerCandidate: defaultMaxRelatedKnowledgePerCandidate}},
	}
	if tc.Subject == factSubjectUser {
		bundle.Profile.Candidates = []factCandidate{candidate}
		bundle.Profile.Current = singletonEvalCurrentFact(memory.FactSubjectUser, tc.CurrentContent)
		return bundle
	}

	bundle.Soul.Candidates = []factCandidate{candidate}
	bundle.Soul.Current = singletonEvalCurrentFact(memory.FactSubjectAgent, tc.CurrentContent)
	for i, constraint := range tc.Constraints {
		bundle.Soul.ActiveConstraints = append(bundle.Soul.ActiveConstraints, memory.ConstraintEntry{
			ID:   fmt.Sprintf("constraint-%d", i+1),
			Text: constraint,
		})
	}
	return bundle
}

func singletonEvalEvidenceSource(subject factSubject) factEvidenceSource {
	if subject == factSubjectAgent {
		return factEvidenceAgentSoulInstruction
	}
	return factEvidenceUserMessage
}

func singletonEvalCurrentFact(subject memory.FactSubject, content string) *memory.Fact {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	return &memory.Fact{ID: "current-singleton", Subject: subject, Content: content}
}

func singletonReconciliationEvalFailures(tc singletonReconciliationEvalCase, plan factReconciliationPlan) []string {
	op, content, conflictNotes := singletonReconciliationEvalPlan(tc.Subject, plan)
	var failures []string
	if op != tc.ExpectedOperation {
		failures = append(failures, fmt.Sprintf("operation=%q want %q", op, tc.ExpectedOperation))
	}
	if tc.ExpectedOperation == singletonOperationNoop && strings.TrimSpace(content) != "" {
		failures = append(failures, "noop returned proposed content")
	}
	if tc.ExpectedOperation != singletonOperationNoop {
		for _, required := range tc.RequiredContent {
			if !containsFold(content, required) {
				failures = append(failures, fmt.Sprintf("proposed content missing %q", required))
			}
		}
		for _, forbidden := range tc.ForbiddenContent {
			if containsFold(content, forbidden) {
				failures = append(failures, fmt.Sprintf("proposed content retained forbidden %q", forbidden))
			}
		}
	}
	if tc.RequireConstraintConflictNote && len(conflictNotes) == 0 {
		failures = append(failures, "constraint conflict note is required")
	}
	return failures
}

func singletonReconciliationEvalPlan(subject factSubject, plan factReconciliationPlan) (singletonFactOperation, string, []string) {
	if subject == factSubjectAgent {
		return plan.Soul.Operation, plan.Soul.ProposedContent, plan.Soul.ConstraintConflictNotes
	}
	return plan.Profile.Operation, plan.Profile.ProposedContent, nil
}

func containsFold(text string, fragment string) bool {
	return strings.Contains(strings.ToLower(text), strings.ToLower(fragment))
}

func writeSingletonReconciliationEvalReport(path string, results []singletonReconciliationEvalResult) error {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func TestReconciliationEvalDatasetParse(t *testing.T) {
	cases, err := loadSingletonReconciliationEvalCases(singletonReconciliationEvalDatasetPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 8 {
		t.Fatalf("eval case count = %d, want 8", len(cases))
	}
	counts := map[factSubject]int{}
	for _, tc := range cases {
		counts[tc.Subject]++
	}
	if counts[factSubjectUser] != 4 || counts[factSubjectAgent] != 4 {
		t.Fatalf("subject counts = %#v, want user=4 agent=4", counts)
	}
}

func TestReconciliationEvalChecksOperationAndContent(t *testing.T) {
	tc := singletonReconciliationEvalCase{
		CaseID:            "profile-preserve",
		Subject:           factSubjectUser,
		ExpectedOperation: singletonOperationReplace,
		RequiredContent:   []string{"concise", "Go developer"},
		ForbiddenContent:  []string{"verbose English"},
	}
	plan := factReconciliationPlan{Profile: factSingletonWritePlan{
		Operation:       singletonOperationReplace,
		ProposedContent: "The user prefers concise replies and is a Go developer.",
	}}
	if failures := singletonReconciliationEvalFailures(tc, plan); len(failures) != 0 {
		t.Fatalf("expected passing checks, got %#v", failures)
	}
	plan.Profile.ProposedContent = "The user prefers verbose English replies."
	if failures := singletonReconciliationEvalFailures(tc, plan); len(failures) < 2 {
		t.Fatalf("expected required and forbidden checks to fail, got %#v", failures)
	}
}

func TestReconciliationEvalFakeCaptureAndReport(t *testing.T) {
	cases, err := loadSingletonReconciliationEvalCases(singletonReconciliationEvalDatasetPath(t))
	if err != nil {
		t.Fatal(err)
	}
	tc := cases[0]
	stream := sequentialCaptureStream(t, []ai.ToolCall{rawToolCall(toolSubmitFactReconciliation, `{"plan":{"profile":{"operation":"replace_singleton","candidate_refs":["fact-0001"],"proposed_content":"The user prefers concise replies, works in Chinese, and wants concrete examples for complex concepts.","rationale":"merge the durable delta"},"soul":{"operation":"noop"},"knowledge":{"operations":[]}}}`)})
	runner := candidateLineReviewer{Stream: stream, Model: ai.Model{ID: "test-model"}}
	plan, err := runner.reconcileFacts(context.Background(), buildSingletonReconciliationEvalBundle(tc))
	if err != nil {
		t.Fatal(err)
	}
	result := singletonReconciliationEvalResult{
		CaseID:   tc.CaseID,
		Subject:  tc.Subject,
		Expected: tc.ExpectedOperation,
		Plan:     plan,
		Failures: singletonReconciliationEvalFailures(tc, plan),
	}
	if len(result.Failures) != 0 {
		t.Fatalf("fake capture checks failed: %#v", result.Failures)
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := writeSingletonReconciliationEvalReport(path, []singletonReconciliationEvalResult{result}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{tc.CaseID, "replace_singleton", "proposed_content"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("report missing %q: %s", want, data)
		}
	}
}

func TestReflectReconciliationEvalManual(t *testing.T) {
	if os.Getenv("STELLA_REFLECT_EVAL") != "1" {
		t.Skip("set STELLA_REFLECT_EVAL=1 and use -tags reflecteval to run manual reconciliation eval")
	}
	providerName := strings.TrimSpace(os.Getenv("STELLA_REFLECT_EVAL_PROVIDER"))
	modelID := strings.TrimSpace(os.Getenv("STELLA_REFLECT_EVAL_MODEL"))
	if providerName == "" || modelID == "" {
		t.Fatal("STELLA_REFLECT_EVAL_PROVIDER and STELLA_REFLECT_EVAL_MODEL are required")
	}
	stream, err := reflectEvalStream(providerName)
	if err != nil {
		t.Fatal(err)
	}
	runner := candidateLineReviewer{
		Stream: stream,
		Model:  ai.Model{ID: modelID, Name: modelID, API: providerName, Provider: providerName, MaxTokens: 4096},
	}
	cases, err := loadSingletonReconciliationEvalCases(singletonReconciliationEvalDatasetPath(t))
	if err != nil {
		t.Fatal(err)
	}

	results := make([]singletonReconciliationEvalResult, 0, len(cases))
	for _, tc := range cases {
		plan, runErr := runSingletonReconciliationEvalCase(t.Context(), runner, tc)
		result := singletonReconciliationEvalResult{CaseID: tc.CaseID, Subject: tc.Subject, Expected: tc.ExpectedOperation, Plan: plan}
		if runErr != nil {
			result.Error = runErr.Error()
		} else {
			result.Failures = singletonReconciliationEvalFailures(tc, plan)
		}
		results = append(results, result)
	}

	reportPath := filepath.Join(repoRoot(t), "dist", "reflect-evals", "672", fmt.Sprintf("profile-soul-%d.json", time.Now().UTC().UnixNano()))
	if err := writeSingletonReconciliationEvalReport(reportPath, results); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote singleton reconciliation eval report to %s", reportPath)
	for _, result := range results {
		if result.Error != "" || len(result.Failures) > 0 {
			t.Errorf("case %s failed: error=%s checks=%v", result.CaseID, result.Error, result.Failures)
		}
	}
}

func runSingletonReconciliationEvalCase(ctx context.Context, runner candidateLineReviewer, tc singletonReconciliationEvalCase) (factReconciliationPlan, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		plan, err := runner.reconcileFacts(ctx, buildSingletonReconciliationEvalBundle(tc))
		if err == nil {
			return plan, nil
		}
		lastErr = err
		// Retry transport failures only; protocol or semantic failures remain failures.
		if !isRetryableReflectEvalErrorMessage(err.Error()) {
			break
		}
	}
	return factReconciliationPlan{}, lastErr
}
