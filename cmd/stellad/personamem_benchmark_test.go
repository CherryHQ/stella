//go:build personamemeval

package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/tools"
)

type personaMemStubTool struct {
	execute func(context.Context, map[string]any) (string, error)
}

func (tool personaMemStubTool) Definition() tools.Definition {
	return tools.Definition{Name: memoryBenchmarkToolName}
}

func (tool personaMemStubTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	return tool.execute(ctx, args)
}

func TestPersonaMemQuestionTypeAliases(t *testing.T) {
	tests := map[string]personaMemCategory{
		"recall_user_shared_facts":                      personaMemRecall,
		"recalling_facts_mentioned_by_the_user":         personaMemRecall,
		"acknowledge_latest_user_preferences":           personaMemLatest,
		"acknowledge_latest_preferences":                personaMemLatest,
		"track_full_preference_evolution":               personaMemEvolution,
		"track_full_preference_updates":                 personaMemEvolution,
		"recalling_the_reasons_behind_previous_updates": personaMemRevisit,
		"revisit_reasons_behind_preference_updates":     personaMemRevisit,
		"generalize_to_new_scenarios":                   personaMemGeneralization,
		"generalizing_to_new_scenarios":                 personaMemGeneralization,
		"provide_preference_aligned_recommendations":    personaMemRecommendation,
		"suggest_new_ideas":                             personaMemSuggestIdeas,
	}
	for value, want := range tests {
		got, ok := normalizePersonaMemQuestionType(value)
		if !ok || got != want {
			t.Errorf("normalize %q = %q/%t, want %q/true", value, got, ok, want)
		}
	}
	if _, ok := normalizePersonaMemQuestionType("unknown_personamem_task"); ok {
		t.Fatal("unknown question type was accepted")
	}
}

func TestPersonaMemSnapshotStatusRequiresExplicitOperatorInput(t *testing.T) {
	t.Setenv("PERSONAMEM_MODEL_SNAPSHOT_STATUS", "")
	status, verified, err := personaMemSnapshotStatusFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if status != personaMemDefaultSnapshotStatus || verified {
		t.Fatalf("default snapshot status = %q/%t", status, verified)
	}

	t.Setenv("PERSONAMEM_MODEL_SNAPSHOT_STATUS", "operator-confirmed-latest")
	status, verified, err = personaMemSnapshotStatusFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if status != "operator-confirmed-latest" || !verified {
		t.Fatalf("explicit snapshot status = %q/%t", status, verified)
	}

	t.Setenv("PERSONAMEM_MODEL_SNAPSHOT_STATUS", "invalid\nstatus")
	if _, _, err := personaMemSnapshotStatusFromEnv(); err == nil {
		t.Fatal("multi-line snapshot status was accepted")
	}
}

func TestPersonaMemExtractAnswerMatchesOfficialPolicy(t *testing.T) {
	tests := []struct {
		name       string
		prediction string
		gold       string
		correct    bool
		extracted  string
	}{
		{name: "tagged", prediction: "Reasoning. <final_answer> (c)</final_answer>", gold: "(c)", correct: true, extracted: "(c)"},
		{name: "last tag wins", prediction: "<final_answer>(a) <final_answer>(d)", gold: "(d)", correct: true, extracted: "(d)"},
		{name: "word option", prediction: "<final_answer> b", gold: "(b)", correct: true, extracted: "b"},
		{name: "multiple choices fail", prediction: "<final_answer>(a) or (b)", gold: "(a)", correct: false, extracted: "(a) or (b)"},
		{name: "fallback full response", prediction: "The answer is (c). <final_answer>unknown", gold: "(c)", correct: true, extracted: "unknown"},
		{name: "wrong", prediction: "<final_answer>(a)", gold: "(d)", correct: false, extracted: "(a)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			correct, extracted := extractPersonaMemAnswer(test.prediction, test.gold)
			if correct != test.correct || extracted != test.extracted {
				t.Fatalf("extract = %t/%q, want %t/%q", correct, extracted, test.correct, test.extracted)
			}
		})
	}
}

func TestPersonaMemFrozenMemoryToolFiltersCurrentSession(t *testing.T) {
	var delegatedLimit int
	inner := personaMemStubTool{execute: func(_ context.Context, args map[string]any) (string, error) {
		delegatedLimit = memoryBenchmarkIntArg(args, "limit", 0)
		payload, err := json.Marshal([]memory.SearchResult{
			{SourceID: "self", SessionID: "qa-current", Score: 3},
			{SourceID: "history-1", SessionID: "history-1", Score: 2},
			{SourceID: "history-2", SessionID: "history-2", Score: 1},
		})
		return string(payload), err
	}}
	tool := &frozenMemoryBenchmarkTool{inner: inner, blockedSessionID: "qa-current", maxCalls: 8}

	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "search",
		"limit":  2,
	})
	if err != nil {
		t.Fatal(err)
	}
	var hits []memory.SearchResult
	if err := json.Unmarshal([]byte(result), &hits); err != nil {
		t.Fatal(err)
	}
	if delegatedLimit != 66 {
		t.Fatalf("delegated search limit = %d, want 66", delegatedLimit)
	}
	if len(hits) != 2 || hits[0].SourceID != "history-1" || hits[1].SourceID != "history-2" {
		t.Fatalf("filtered hits = %#v", hits)
	}
}

func TestPersonaMemFrozenMemoryToolStopsAfterBudget(t *testing.T) {
	var delegatedCalls int
	inner := personaMemStubTool{execute: func(_ context.Context, _ map[string]any) (string, error) {
		delegatedCalls++
		return "delegated", nil
	}}
	tool := &frozenMemoryBenchmarkTool{inner: inner, maxCalls: 1}

	if result, err := tool.Execute(context.Background(), map[string]any{"action": "status"}); err != nil || result != "delegated" {
		t.Fatalf("first call = %q, %v", result, err)
	}
	result, err := tool.Execute(context.Background(), map[string]any{"action": "status"})
	if err != nil {
		t.Fatal(err)
	}
	if delegatedCalls != 1 || result != memoryBenchmarkBudgetExhaustedMessage {
		t.Fatalf("budget response = %q, delegated calls = %d", result, delegatedCalls)
	}
}

func TestPersonaMemSelectedDatasetContract(t *testing.T) {
	if _, err := auditPersonaMemQuestionSplits(); err != nil {
		t.Fatal(err)
	}
	samples, hashes, err := loadPersonaMemDataset(personaMemSelectedSpecs)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 6 || len(hashes) != 4 {
		t.Fatalf("selected samples/hashes = %d/%d, want 6/4", len(samples), len(hashes))
	}
	if !sameStringMap(hashes, personaMemExpectedDatasetSHA256) {
		t.Fatalf("dataset SHA-256 = %#v, want fixed PersonaMem v1 hashes", hashes)
	}
	wantQuestionCounts := []int{17, 28, 21, 64, 52, 46}
	personas := make(map[string]struct{})
	questionIDs := make(map[string]struct{})
	rawEndpointCount := 0
	effectiveEndpointCount := 0
	coreQuestionCount := 0
	extendedQuestionCount := 0
	for index, sample := range samples {
		if len(sample.Questions) != wantQuestionCounts[index] {
			t.Fatalf("selected sample %d questions = %d, want %d", index, len(sample.Questions), wantQuestionCounts[index])
		}
		personas[sample.Spec.PersonaID] = struct{}{}
		rawEndpointCount += sample.Spec.RawEndpointCount
		effectiveEndpointCount += len(personaMemEndpoints(sample))
		coreRawEndpoints := make(map[int]struct{})
		extendedRawEndpoints := make(map[int]struct{})
		for _, question := range sample.Questions {
			if _, duplicate := questionIDs[question.QuestionID]; duplicate {
				t.Fatalf("duplicate question ID %s", question.QuestionID)
			}
			questionIDs[question.QuestionID] = struct{}{}
			if personaMemIsCoreCategory(question.Category) {
				coreQuestionCount++
				coreRawEndpoints[question.RawEndpoint] = struct{}{}
			} else if personaMemIsExtendedCategory(question.Category) {
				extendedQuestionCount++
				extendedRawEndpoints[question.RawEndpoint] = struct{}{}
			}
		}
		for endpoint := range extendedRawEndpoints {
			if _, ok := coreRawEndpoints[endpoint]; !ok {
				t.Fatalf("%s extension endpoint %d has no core question", sample.Spec.ContextID, endpoint)
			}
		}
	}
	if len(personas) != 6 || rawEndpointCount != 48 || effectiveEndpointCount != 47 {
		t.Fatalf("personas/raw/effective endpoints = %d/%d/%d, want 6/48/47", len(personas), rawEndpointCount, effectiveEndpointCount)
	}
	if coreQuestionCount != 148 || extendedQuestionCount != 80 || len(questionIDs) != 228 {
		t.Fatalf("core/extended/total questions = %d/%d/%d, want 148/80/228", coreQuestionCount, extendedQuestionCount, len(questionIDs))
	}
	for _, sample := range samples {
		for _, endpoint := range personaMemEndpoints(sample) {
			// Official inference uses context[:endpoint], so endpoint itself must
			// remain future content until the next checkpoint.
			if endpoint < len(sample.Messages) && sample.Messages[endpoint-1] == sample.Messages[endpoint] {
				t.Fatalf("%s endpoint %d does not separate distinct messages", sample.Spec.Split, endpoint)
			}
		}
	}
}

func TestPersonaMemRepositoryLocalPaths(t *testing.T) {
	wantRoot := filepath.Clean(personaMemRepositoryRoot)
	gotRoot, err := findPersonaMemRepositoryRoot(filepath.Join(wantRoot, "cmd", "stellad"))
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != wantRoot {
		t.Fatalf("repository root = %q, want %q", gotRoot, wantRoot)
	}

	wantBenchmarkRoot := filepath.Join(wantRoot, "dist", "benchmarks", "personamem")
	if personaMemBenchmarkRoot != wantBenchmarkRoot {
		t.Fatalf("benchmark root = %q, want %q", personaMemBenchmarkRoot, wantBenchmarkRoot)
	}
	for _, path := range []string{personaMemRunsRoot, personaMemHomesRoot} {
		relative, err := filepath.Rel(wantRoot, path)
		if err != nil {
			t.Fatal(err)
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("PersonaMem path escaped worktree: %s", path)
		}
	}
	configuredDataRoot := strings.TrimSpace(os.Getenv("PERSONAMEM_DATA_ROOT"))
	if configuredDataRoot == "" {
		if personaMemDataRoot != filepath.Join(wantBenchmarkRoot, "data") {
			t.Fatalf("default data root = %q", personaMemDataRoot)
		}
	} else if personaMemDataRoot != filepath.Clean(configuredDataRoot) {
		t.Fatalf("configured data root = %q, want %q", personaMemDataRoot, filepath.Clean(configuredDataRoot))
	}

	runName, err := personaMemRunName("representative")
	if err != nil {
		t.Fatal(err)
	}
	if runName != "representative-v2" {
		t.Fatalf("representative run name = %q, want representative-v2", runName)
	}
	home, err := personaMemHomeForMode("representative")
	if err != nil {
		t.Fatal(err)
	}
	if home != filepath.Join(personaMemBenchmarkRoot, "h", "r-v2") {
		t.Fatalf("representative home = %q", home)
	}
	if _, err := personaMemRunName("selected"); err == nil {
		t.Fatal("legacy selected mode was accepted")
	}
}

func TestPersonaMemRepresentativeSelectorContract(t *testing.T) {
	stats32, err := loadPersonaMemSelectionStats("32k")
	if err != nil {
		t.Fatal(err)
	}
	stats128, err := loadPersonaMemSelectionStats("128k")
	if err != nil {
		t.Fatal(err)
	}

	pools32, err := buildPersonaMemSelectionPools("32k", stats32)
	if err != nil {
		t.Fatal(err)
	}
	pools128, err := buildPersonaMemSelectionPools("128k", stats128)
	if err != nil {
		t.Fatal(err)
	}
	if got := []int{len(pools32[0]), len(pools32[1]), len(pools32[2])}; !samePersonaMemInts(got, []int{12, 10, 15}) {
		t.Fatalf("32k low/middle/high pools = %v, want [12 10 15]", got)
	}
	if got := []int{len(pools128[0]), len(pools128[1]), len(pools128[2])}; !samePersonaMemInts(got, []int{24, 20, 16}) {
		t.Fatalf("128k low/middle/high pools = %v, want [24 20 16]", got)
	}

	triples32 := buildPersonaMemSelectionTriples(pools32)
	triples128 := buildPersonaMemSelectionTriples(pools128)
	if len(triples32) != 1641 || len(triples128) != 6996 {
		t.Fatalf("32k/128k triples = %d/%d, want 1641/6996", len(triples32), len(triples128))
	}

	valid, bestHash, bestIDs := choosePersonaMemRepresentativeCombination(triples32, triples128)
	if valid != 1935 {
		t.Fatalf("valid selector combinations = %d, want 1935", valid)
	}
	if bestHash != personaMemSelectorHash {
		t.Fatalf("selector hash = %s, want %s", bestHash, personaMemSelectorHash)
	}
	wantIDs := make([]string, 0, len(personaMemSelectedSpecs))
	for _, spec := range personaMemSelectedSpecs {
		wantIDs = append(wantIDs, spec.ContextID)
	}
	sort.Strings(wantIDs)
	if strings.Join(bestIDs, "|") != strings.Join(wantIDs, "|") {
		t.Fatalf("selected context IDs = %v, want %v", bestIDs, wantIDs)
	}
}

func TestPersonaMemSelectionDigestIsCanonical(t *testing.T) {
	first := personaMemSample{Spec: personaMemSampleSpec{Split: "32k", ContextID: "context-a"}, Questions: []personaMemQuestion{
		{QuestionID: "q2", Category: personaMemLatest, RawEndpoint: 4, Endpoint: 4},
		{QuestionID: "q1", Category: personaMemRecall, RawEndpoint: 2, Endpoint: 2},
	}}
	second := personaMemSample{Spec: personaMemSampleSpec{Split: "128k", ContextID: "context-b"}, Questions: []personaMemQuestion{
		{QuestionID: "q3", Category: personaMemEvolution, RawEndpoint: 8, Endpoint: 7},
	}}
	want := personaMemSelectionSHA256([]personaMemSample{first, second})
	first.Questions[0], first.Questions[1] = first.Questions[1], first.Questions[0]
	if got := personaMemSelectionSHA256([]personaMemSample{second, first}); got != want {
		t.Fatalf("reordered selection digest = %s, want %s", got, want)
	}
	second.Questions[0].QuestionID = "q3-changed"
	if got := personaMemSelectionSHA256([]personaMemSample{first, second}); got == want {
		t.Fatal("selection digest did not change after question identity changed")
	}
}

func TestPersonaMemCheckpointRejectsContractMismatch(t *testing.T) {
	temporary := t.TempDir()
	path := filepath.Join(temporary, "checkpoint.json")
	runDir := filepath.Join(temporary, "representative-v2")
	datasetSHA := map[string]string{"questions_32k.csv": "dataset-a"}
	model := personaMemModelIdentity{
		ProviderHost: "router.example", ProviderEndpointSHA256: "endpoint-a",
		RequestedModel: personaMemAnswerModelID, SnapshotStatus: personaMemDefaultSnapshotStatus,
		SnapshotVerified: true, Note: "test model identity",
	}
	checkpoint, err := loadOrCreatePersonaMemCheckpoint(path, runDir, "representative", model, datasetSHA, "selection-a", 148, 80)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Version != 3 || checkpoint.SelectionSHA256 != "selection-a" ||
		checkpoint.ReflectReviewerTimeoutSeconds != int64(personaMemReflectTimeout/time.Second) ||
		checkpoint.ReflectPreWriteMaxAttempts != personaMemReflectMaxAttempts {
		t.Fatalf("new checkpoint = %#v", checkpoint)
	}
	if _, err := loadOrCreatePersonaMemCheckpoint(path, runDir, "representative", model, datasetSHA, "selection-a", 148, 80); err != nil {
		t.Fatalf("reload matching checkpoint: %v", err)
	}
	if _, err := loadOrCreatePersonaMemCheckpoint(path, runDir, "representative", model, datasetSHA, "selection-b", 148, 80); err == nil {
		t.Fatal("checkpoint with mismatched selection digest was accepted")
	}

	tests := map[string]func(*personaMemCheckpoint){
		"legacy version": func(value *personaMemCheckpoint) { value.Version = 2 },
		"dataset hash":   func(value *personaMemCheckpoint) { value.DatasetSHA256["questions_32k.csv"] = "dataset-b" },
		"answer model":   func(value *personaMemCheckpoint) { value.AnswerModel = "different-model" },
		"provider endpoint": func(value *personaMemCheckpoint) {
			value.Model.ProviderEndpointSHA256 = "endpoint-b"
		},
		"reflect timeout": func(value *personaMemCheckpoint) { value.ReflectReviewerTimeoutSeconds = 1 },
		"reflect retries": func(value *personaMemCheckpoint) { value.ReflectPreWriteMaxAttempts = 1 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			payload, err := json.Marshal(checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			var changed personaMemCheckpoint
			if err := json.Unmarshal(payload, &changed); err != nil {
				t.Fatal(err)
			}
			mutate(&changed)
			changedPath := filepath.Join(temporary, strings.ReplaceAll(name, " ", "-")+".json")
			if err := writeMemoryBenchmarkJSONAtomic(changedPath, &changed); err != nil {
				t.Fatal(err)
			}
			if _, err := loadOrCreatePersonaMemCheckpoint(changedPath, runDir, "representative", model, datasetSHA, "selection-a", 148, 80); err == nil {
				t.Fatalf("checkpoint with mismatched %s was accepted", name)
			}
		})
	}

}

func TestPersonaMemSmokeReusesOneEndpoint(t *testing.T) {
	samples, _, err := loadPersonaMemDataset(personaMemSelectedSpecs)
	if err != nil {
		t.Fatal(err)
	}
	smoke := personaMemSmokeSamples(samples)
	if len(smoke) != 1 || len(smoke[0].Questions) != 2 {
		t.Fatalf("smoke samples/questions = %d/%d, want 1/2", len(smoke), len(smoke[0].Questions))
	}
	if smoke[0].Questions[0].Endpoint != smoke[0].Questions[1].Endpoint {
		t.Fatalf("smoke questions use endpoints %d and %d", smoke[0].Questions[0].Endpoint, smoke[0].Questions[1].Endpoint)
	}
	if !personaMemQuestionsContainCoreAndExtended(smoke[0].Questions) {
		t.Fatal("smoke endpoint does not include one core and one extended question")
	}
}

func TestPersonaMemPythonSliceEnd(t *testing.T) {
	tests := map[int]int{-100: 0, -1: 9, 0: 0, 5: 5, 10: 10, 11: 10}
	for endpoint, want := range tests {
		if got := personaMemPythonSliceEnd(endpoint, 10); got != want {
			t.Errorf("slice end %d = %d, want %d", endpoint, got, want)
		}
	}
}

func TestPersonaMemScoreExcludesIncompleteFromAccuracy(t *testing.T) {
	samples := []personaMemSample{{Questions: []personaMemQuestion{
		{QuestionID: "q1", Category: personaMemRecall},
		{QuestionID: "q2", Category: personaMemRecall},
		{QuestionID: "q3", Category: personaMemLatest},
		{QuestionID: "q4", Category: personaMemEvolution},
		{QuestionID: "q5", Category: personaMemRevisit},
		{QuestionID: "q6", Category: personaMemGeneralization},
		{QuestionID: "q7", Category: personaMemRecommendation},
		{QuestionID: "q8", Category: personaMemSuggestIdeas},
	}}}
	answers := []*personaMemAnswerRecord{
		{QuestionID: "q1", Category: personaMemRecall, Completed: true, Correct: true},
		{QuestionID: "q2", Category: personaMemRecall, Completed: false},
		{QuestionID: "q3", Category: personaMemLatest, Completed: true, Correct: false},
		{QuestionID: "q4", Category: personaMemEvolution, Completed: true, Correct: true},
		{QuestionID: "q5", Category: personaMemRevisit, Completed: true, Correct: false},
		{QuestionID: "q6", Category: personaMemGeneralization, Completed: true, Correct: true},
		{QuestionID: "q7", Category: personaMemRecommendation, Completed: true, Correct: false},
		{QuestionID: "q8", Category: personaMemSuggestIdeas, Completed: true, Correct: true},
	}
	report := buildPersonaMemScoreReport(samples, answers)
	if report.Overall.Correct != 4 || report.Overall.Completed != 7 || report.Overall.Expected != 8 {
		t.Fatalf("overall score = %#v", report.Overall)
	}
	if report.Overall.Accuracy == nil || *report.Overall.Accuracy != float64(4)/7 {
		t.Fatalf("overall accuracy = %v, want 4/7", report.Overall.Accuracy)
	}
	recall := report.CoreCategories[personaMemRecall]
	if recall.Correct != 1 || recall.Completed != 1 || recall.Expected != 2 || recall.Accuracy == nil || *recall.Accuracy != 1 {
		t.Fatalf("recall score = %#v", recall)
	}
	if report.CoreMacroAccuracy == nil || *report.CoreMacroAccuracy != 0.5 {
		t.Fatalf("core macro accuracy = %v, want 0.5", report.CoreMacroAccuracy)
	}
	if got := report.ExtendedCategories[personaMemGeneralization]; got.Correct != 1 || got.Expected != 1 {
		t.Fatalf("generalization score = %#v", got)
	}
}

func TestPersonaMemCoreMacroRequiresAllCategories(t *testing.T) {
	samples := []personaMemSample{{Questions: []personaMemQuestion{{QuestionID: "q1", Category: personaMemRecall}}}}
	report := buildPersonaMemScoreReport(samples, []*personaMemAnswerRecord{{
		QuestionID: "q1", Category: personaMemRecall, Completed: true, Correct: true,
	}})
	if report.CoreMacroAccuracy != nil {
		t.Fatalf("core macro accuracy = %v, want nil", *report.CoreMacroAccuracy)
	}
}

func TestPersonaMemNormalizeLegacyMemoryCallOutcomes(t *testing.T) {
	calls := make([]personaMemMemoryCall, 10)
	for index := range calls {
		calls[index].ResultBytes = 100 + index
	}
	calls[8].ResultBytes = len(memoryBenchmarkBudgetExhaustedMessage)
	calls[9].ResultBytes = len(memoryBenchmarkBudgetExhaustedMessage)

	if err := normalizePersonaMemMemoryCallOutcomes(calls); err != nil {
		t.Fatal(err)
	}
	for index, call := range calls {
		want := "executed"
		if index >= personaMemQAToolBudget {
			want = "budget_denied"
		}
		if call.Outcome != want {
			t.Errorf("call %d outcome = %q, want %q", index, call.Outcome, want)
		}
	}
}

func TestPersonaMemEmptyAnswerIsRetryable(t *testing.T) {
	if !isPersonaMemQATransient(errPersonaMemEmptyAnswer) {
		t.Fatal("empty PersonaMem answer was not retryable")
	}
	if !isPersonaMemQATransient(errors.New("provider: context deadline exceeded")) {
		t.Fatal("provider deadline string was not retryable")
	}
	if isPersonaMemQATransient(errors.New("permanent benchmark contract error")) {
		t.Fatal("permanent PersonaMem error was retryable")
	}
}

type personaMemSelectionStats struct {
	Split                  string
	ContextID              string
	PersonaID              string
	EffectiveEndpointCount int
	CategoryCounts         [4]int
	DistanceCounts         [4][3]int
}

type personaMemSelectionTriple struct {
	Contexts       [3]*personaMemSelectionStats
	CategoryCounts [4]int
	DistanceCounts [4][3]int
}

func loadPersonaMemSelectionStats(split string) ([]*personaMemSelectionStats, error) {
	contextLengths, err := loadPersonaMemContextLengths(split)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(personaMemDataRoot, "questions_"+split+".csv")
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open PersonaMem selector questions: %w", err)
	}
	defer func() { _ = file.Close() }()

	reader := csv.NewReader(file)
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read PersonaMem selector header: %w", err)
	}
	columns := make(map[string]int, len(header))
	for index, name := range header {
		columns[name] = index
	}
	required := []string{
		"persona_id", "question_type", "shared_context_id", "end_index_in_shared_context",
		"distance_to_ref_proportion_in_context",
	}
	for _, name := range required {
		if _, ok := columns[name]; !ok {
			return nil, fmt.Errorf("PersonaMem selector data lacks column %q", name)
		}
	}

	type aggregate struct {
		stats              *personaMemSelectionStats
		effectiveEndpoints map[int]struct{}
	}
	aggregates := make(map[string]*aggregate)
	for rowNumber := 2; ; rowNumber++ {
		row, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read PersonaMem selector row %d: %w", rowNumber, readErr)
		}
		category, ok := normalizePersonaMemQuestionType(row[columns["question_type"]])
		if !ok || !personaMemIsCoreCategory(category) {
			continue
		}
		categoryIndex, ok := personaMemCoreCategoryIndex(category)
		if !ok {
			return nil, fmt.Errorf("index PersonaMem core category %q", category)
		}
		contextID := row[columns["shared_context_id"]]
		messageCount, ok := contextLengths[contextID]
		if !ok {
			return nil, fmt.Errorf("PersonaMem selector context %s is missing", contextID)
		}
		rawEndpoint, err := strconv.Atoi(row[columns["end_index_in_shared_context"]])
		if err != nil {
			return nil, fmt.Errorf("PersonaMem selector row %d endpoint: %w", rowNumber, err)
		}
		distanceBucket, err := personaMemDistanceBucket(row[columns["distance_to_ref_proportion_in_context"]])
		if err != nil {
			return nil, fmt.Errorf("PersonaMem selector row %d distance: %w", rowNumber, err)
		}

		entry := aggregates[contextID]
		if entry == nil {
			entry = &aggregate{
				stats: &personaMemSelectionStats{
					Split: split, ContextID: contextID, PersonaID: row[columns["persona_id"]],
				},
				effectiveEndpoints: make(map[int]struct{}),
			}
			aggregates[contextID] = entry
		}
		if entry.stats.PersonaID != row[columns["persona_id"]] {
			return nil, fmt.Errorf("PersonaMem context %s has multiple personas", contextID)
		}
		entry.stats.CategoryCounts[categoryIndex]++
		entry.stats.DistanceCounts[categoryIndex][distanceBucket]++
		entry.effectiveEndpoints[personaMemPythonSliceEnd(rawEndpoint, messageCount)] = struct{}{}
	}

	result := make([]*personaMemSelectionStats, 0, len(aggregates))
	for _, entry := range aggregates {
		entry.stats.EffectiveEndpointCount = len(entry.effectiveEndpoints)
		result = append(result, entry.stats)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ContextID < result[j].ContextID })
	return result, nil
}

func loadPersonaMemContextLengths(split string) (map[string]int, error) {
	path := filepath.Join(personaMemDataRoot, "shared_contexts_"+split+".jsonl")
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open PersonaMem selector contexts: %w", err)
	}
	defer func() { _ = file.Close() }()

	result := make(map[string]int)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for scanner.Scan() {
		var row map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("decode PersonaMem selector context row: %w", err)
		}
		for contextID, raw := range row {
			var messages []personaMemMessage
			if err := json.Unmarshal(raw, &messages); err != nil {
				return nil, fmt.Errorf("decode PersonaMem selector context %s: %w", contextID, err)
			}
			if _, duplicate := result[contextID]; duplicate {
				return nil, fmt.Errorf("PersonaMem selector context %s appears more than once", contextID)
			}
			result[contextID] = len(messages)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan PersonaMem selector contexts: %w", err)
	}
	return result, nil
}

func personaMemCoreCategoryIndex(category personaMemCategory) (int, bool) {
	for index, current := range personaMemCoreCategories {
		if current == category {
			return index, true
		}
	}
	return 0, false
}

func personaMemDistanceBucket(value string) (int, error) {
	percent, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(value), "%"), 64)
	if err != nil {
		return 0, err
	}
	switch {
	case percent < 100.0/3.0:
		return 0, nil
	case percent < 200.0/3.0:
		return 1, nil
	default:
		return 2, nil
	}
}

func buildPersonaMemSelectionPools(split string, stats []*personaMemSelectionStats) ([3][]*personaMemSelectionStats, error) {
	var pools [3][]*personaMemSelectionStats
	for _, item := range stats {
		band := -1
		switch split {
		case "32k":
			switch {
			case item.EffectiveEndpointCount <= 2:
				band = 0
			case item.EffectiveEndpointCount <= 8:
				band = 1
			default:
				band = 2
			}
		case "128k":
			switch {
			case item.EffectiveEndpointCount >= 6 && item.EffectiveEndpointCount <= 7:
				band = 0
			case item.EffectiveEndpointCount >= 8 && item.EffectiveEndpointCount <= 9:
				band = 1
			case item.EffectiveEndpointCount >= 10:
				band = 2
			}
		default:
			return pools, fmt.Errorf("unsupported PersonaMem selector split %q", split)
		}
		if band >= 0 {
			pools[band] = append(pools[band], item)
		}
	}
	for index := range pools {
		sort.Slice(pools[index], func(i, j int) bool {
			return pools[index][i].ContextID < pools[index][j].ContextID
		})
	}
	return pools, nil
}

func buildPersonaMemSelectionTriples(pools [3][]*personaMemSelectionStats) []personaMemSelectionTriple {
	var result []personaMemSelectionTriple
	for _, low := range pools[0] {
		for _, middle := range pools[1] {
			if low.PersonaID == middle.PersonaID {
				continue
			}
			for _, high := range pools[2] {
				if high.PersonaID == low.PersonaID || high.PersonaID == middle.PersonaID {
					continue
				}
				triple := personaMemSelectionTriple{Contexts: [3]*personaMemSelectionStats{low, middle, high}}
				for _, item := range triple.Contexts {
					for category := range triple.CategoryCounts {
						triple.CategoryCounts[category] += item.CategoryCounts[category]
						for bucket := range triple.DistanceCounts[category] {
							triple.DistanceCounts[category][bucket] += item.DistanceCounts[category][bucket]
						}
					}
				}
				result = append(result, triple)
			}
		}
	}
	return result
}

func choosePersonaMemRepresentativeCombination(
	triples32, triples128 []personaMemSelectionTriple,
) (int, string, []string) {
	valid := 0
	bestHash := ""
	var bestIDs []string
	for _, triple32 := range triples32 {
		for _, triple128 := range triples128 {
			if personaMemTriplesSharePersona(triple32, triple128) || !personaMemCombinationMeetsCoverage(triple32, triple128) {
				continue
			}
			valid++
			ids := make([]string, 0, 6)
			for _, item := range triple32.Contexts {
				ids = append(ids, item.ContextID)
			}
			for _, item := range triple128.Contexts {
				ids = append(ids, item.ContextID)
			}
			sort.Strings(ids)
			sum := sha256.Sum256([]byte(personaMemSelectorSeed + "|" + strings.Join(ids, "|")))
			hash := hex.EncodeToString(sum[:])
			if bestHash == "" || hash < bestHash {
				bestHash = hash
				bestIDs = append([]string(nil), ids...)
			}
		}
	}
	return valid, bestHash, bestIDs
}

func personaMemTriplesSharePersona(first, second personaMemSelectionTriple) bool {
	for _, left := range first.Contexts {
		for _, right := range second.Contexts {
			if left.PersonaID == right.PersonaID {
				return true
			}
		}
	}
	return false
}

func personaMemCombinationMeetsCoverage(first, second personaMemSelectionTriple) bool {
	for category := range first.CategoryCounts {
		if first.CategoryCounts[category]+second.CategoryCounts[category] < 30 {
			return false
		}
		for bucket := range first.DistanceCounts[category] {
			if first.DistanceCounts[category][bucket]+second.DistanceCounts[category][bucket] < 3 {
				return false
			}
		}
	}
	return true
}

func samePersonaMemInts(first, second []int) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

func ensurePersonaMemBenchmarkConfig(ctx context.Context, setupState *setupResult) error {
	providers, err := setupState.store.ListProviders(ctx)
	if err != nil {
		return fmt.Errorf("list PersonaMem benchmark providers: %w", err)
	}
	agents, err := setupState.store.ListAgents(ctx)
	if err != nil {
		return fmt.Errorf("list PersonaMem benchmark agents: %w", err)
	}
	provider, hasProvider := personaMemProviderByID(providers, personaMemProviderID)
	if hasProvider && (!provider.Enabled || strings.TrimSpace(provider.APIKey) == "" || strings.TrimSpace(provider.BaseURL) == "") {
		return fmt.Errorf("PersonaMem provider %q exists but is not enabled with API key and base URL", personaMemProviderID)
	}
	hasBenchmarkAgent := false
	for _, agent := range agents {
		if agent.ID == personaMemAgentID {
			hasBenchmarkAgent = true
			break
		}
	}
	if hasProvider && hasBenchmarkAgent {
		return nil
	}

	if !hasProvider {
		apiKey := strings.TrimSpace(os.Getenv("PERSONAMEM_PROVIDER_API_KEY"))
		baseURL := strings.TrimSpace(os.Getenv("PERSONAMEM_PROVIDER_BASE_URL"))
		if apiKey == "" || baseURL == "" {
			return fmt.Errorf("PERSONAMEM_PROVIDER_API_KEY and PERSONAMEM_PROVIDER_BASE_URL are required for a fresh benchmark home")
		}
		provider := config.Provider{
			ID: personaMemProviderID, Type: "openai", Name: "PersonaMem OpenAI-compatible provider",
			Enabled: true, APIKey: apiKey, BaseURL: baseURL,
		}
		if err := setupState.store.CreateProvider(ctx, provider); err != nil {
			return fmt.Errorf("bootstrap PersonaMem provider config: %w", err)
		}
	}

	if !hasBenchmarkAgent {
		modelRef := personaMemProviderID + "/" + personaMemAnswerModelID
		agent := config.Agent{
			ID:          personaMemAgentID,
			Name:        "PersonaMem Benchmark",
			Model:       modelRef,
			ModelStrong: modelRef,
			ModelFast:   modelRef,
			Scope:       config.AgentScopeSystem,
			Enabled:     true,
		}
		// The v2 agent intentionally starts without copied soul, custom system
		// prompt, workspace, or skills so old benchmark state cannot leak in.
		if err := setupState.store.CreateAgent(ctx, agent); err != nil {
			return fmt.Errorf("bootstrap PersonaMem benchmark agent: %w", err)
		}
	}
	return nil
}

func personaMemProviderByID(providers []config.Provider, id string) (config.Provider, bool) {
	for _, provider := range providers {
		if provider.ID == id {
			return provider, true
		}
	}
	return config.Provider{}, false
}

func TestPersonaMemBenchmark(t *testing.T) {
	mode := strings.TrimSpace(os.Getenv("PERSONAMEM_MODE"))
	if mode == "" {
		t.Skip("set PERSONAMEM_MODE=inspect, smoke, or representative")
	}
	expectedHome, err := personaMemHomeForMode(mode)
	if err != nil {
		t.Fatal(err)
	}
	if got := config.StellaHome(); got != expectedHome {
		t.Fatalf("STELLA_HOME = %q, want isolated benchmark home %q", got, expectedHome)
	}
	if mode == "inspect" {
		if err := inspectPersonaMemData(); err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := config.LoadServerConfig(os.LookupEnv)
	if err != nil {
		t.Fatalf("load server config: %v", err)
	}
	// Production runner construction evaluates service-tool availability even
	// when the benchmark excludes those tools. Supply an ephemeral vault key so
	// the isolated setup follows the normal production dependency graph.
	vaultKey, err := vault.GenerateMasterIdentity()
	if err != nil {
		t.Fatalf("generate isolated benchmark vault key: %v", err)
	}
	cfg.Vault.Key = vaultKey
	ctx, cancel := context.WithCancel(context.Background())
	// representative-v2 always uses a fresh home. Production setup installs the
	// required search extensions before migrations; the legacy out-of-order
	// repair helper is only valid for cloned development databases.
	setupState, err := setup(ctx, cfg, "http://127.0.0.1:18081")
	if err != nil {
		cancel()
		t.Fatalf("setup isolated Stella: %v", err)
	}
	// PersonaMem drives only the production Fact Reflect line synchronously.
	setupState.schedulerSvc.Quiesce()
	t.Cleanup(func() {
		cancel()
		setupState.waitBackgroundTasks()
		_ = setupState.poolManager.Close()
		if setupState.embedded != nil {
			setupState.db.Close()
			_ = setupState.embedded.Stop()
		}
	})
	if err := ensurePersonaMemBenchmarkConfig(ctx, setupState); err != nil {
		t.Fatalf("bootstrap isolated PersonaMem config: %v", err)
	}

	switch mode {
	case "inspect":
		providerCfg, err := setupState.store.GetProvider(ctx, personaMemProviderID)
		if err != nil {
			t.Fatal(err)
		}
		identity, err := personaMemModelIdentityFromProvider(ctx, providerCfg)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("PersonaMem model identity: %+v\n", identity)
	case "smoke", "representative":
		if err := runPersonaMem(ctx, setupState, mode); err != nil {
			t.Fatalf("run PersonaMem %s: %v", mode, err)
		}
	default:
		t.Fatalf("unsupported PERSONAMEM_MODE %q", mode)
	}
}
