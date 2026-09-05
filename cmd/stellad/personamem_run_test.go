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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/reflect"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/tools"
	openaiprovider "github.com/CherryHQ/stella/plugins/providers/openai"
)

const (
	personaMemAnswerModelID         = "deepseek/deepseek-v4-flash"
	personaMemProviderID            = "openai"
	personaMemQAPolicy              = "frozen-session-v1"
	personaMemRunRevision           = "v2"
	personaMemSelectorSeed          = "stella-personamem-representative-v2"
	personaMemSelectorHash          = "000f922f3ed2b743856b5536d52ba713eed6dce771f55cb735c6ab2726145e15"
	personaMemMaxAttempts           = 3
	personaMemQAToolBudget          = 8
	personaMemQATimeout             = 90 * time.Second
	personaMemReflectTimeout        = 4 * time.Minute
	personaMemReflectMaxAttempts    = 6
	personaMemSystemPrefix          = "Current user persona:"
	personaMemAnswerPrompt          = "Find the most appropriate model response and give your final answer (a), (b), (c), or (d) after the special token <final_answer>."
	personaMemDefaultSnapshotStatus = "unverified-alias"
)

var (
	personaMemRepositoryRoot = mustPersonaMemRepositoryRoot()
	personaMemBenchmarkRoot  = filepath.Join(personaMemRepositoryRoot, "dist", "benchmarks", "personamem")
	personaMemDataRoot       = personaMemPathFromEnv("PERSONAMEM_DATA_ROOT", filepath.Join(personaMemBenchmarkRoot, "data"))
	personaMemRunsRoot       = filepath.Join(personaMemBenchmarkRoot, "runs")
	// Keep the physical home path short enough for PostgreSQL's 107-byte Unix
	// socket limit while retaining all benchmark state inside this worktree.
	personaMemHomesRoot      = filepath.Join(personaMemBenchmarkRoot, "h")
	personaMemAgentID        = uuid.NewSHA1(uuid.NameSpaceURL, []byte("stella/personamem/benchmark-agent/v2")).String()
	errPersonaMemEmptyAnswer = errors.New("PersonaMem QA runner returned an empty answer")
)

func personaMemPathFromEnv(name string, fallback string) string {
	if path := strings.TrimSpace(os.Getenv(name)); path != "" {
		return filepath.Clean(path)
	}
	return fallback
}

func mustPersonaMemRepositoryRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		panic(fmt.Sprintf("resolve PersonaMem working directory: %v", err))
	}
	root, err := findPersonaMemRepositoryRoot(cwd)
	if err != nil {
		panic(err)
	}
	return root
}

func findPersonaMemRepositoryRoot(start string) (string, error) {
	directory, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve PersonaMem start path: %w", err)
	}
	for {
		if info, statErr := os.Stat(filepath.Join(directory, "go.mod")); statErr == nil && !info.IsDir() {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", fmt.Errorf("find Stella repository root from %s", start)
		}
		directory = parent
	}
}

func personaMemRunName(mode string) (string, error) {
	switch mode {
	case "inspect", "smoke", "representative":
		return mode + "-" + personaMemRunRevision, nil
	default:
		return "", fmt.Errorf("unsupported PERSONAMEM_MODE %q", mode)
	}
}

func personaMemHomeForMode(mode string) (string, error) {
	var prefix string
	switch mode {
	case "inspect":
		prefix = "i"
	case "smoke":
		prefix = "s"
	case "representative":
		prefix = "r"
	default:
		return "", fmt.Errorf("unsupported PERSONAMEM_MODE %q", mode)
	}
	return filepath.Join(personaMemHomesRoot, prefix+"-"+personaMemRunRevision), nil
}

type personaMemCategory string

const (
	personaMemRecall         personaMemCategory = "recall"
	personaMemLatest         personaMemCategory = "latest"
	personaMemEvolution      personaMemCategory = "evolution"
	personaMemRevisit        personaMemCategory = "revisit"
	personaMemGeneralization personaMemCategory = "generalization"
	personaMemRecommendation personaMemCategory = "recommendation"
	personaMemSuggestIdeas   personaMemCategory = "suggest_new_ideas"
)

var personaMemCoreCategories = []personaMemCategory{
	personaMemRecall,
	personaMemLatest,
	personaMemEvolution,
	personaMemRevisit,
}

var personaMemExtendedCategories = []personaMemCategory{
	personaMemGeneralization,
	personaMemRecommendation,
	personaMemSuggestIdeas,
}

var personaMemCategories = []personaMemCategory{
	personaMemRecall,
	personaMemLatest,
	personaMemEvolution,
	personaMemRevisit,
	personaMemGeneralization,
	personaMemRecommendation,
	personaMemSuggestIdeas,
}

var personaMemExpectedDatasetSHA256 = map[string]string{
	"questions_128k.csv":         "f0e137c3167fadbffbce5be2786105283c3299972c4ac1f158939155fd1578a7",
	"questions_32k.csv":          "cccd34cf53e0bc4d9536c04cff5ca045156d9a4e227e83327112482840bbc93c",
	"shared_contexts_128k.jsonl": "733cc009e84a138b386c9e40adea741565db01074f73af4058fd039b42951726",
	"shared_contexts_32k.jsonl":  "217247ebfec9e8442fc53570c795ab69f21aad08745f7de78d9beab51b122d4a",
}

type personaMemSampleSpec struct {
	Split                  string                     `json:"split"`
	ContextID              string                     `json:"shared_context_id"`
	PersonaID              string                     `json:"persona_id"`
	RawEndpointCount       int                        `json:"raw_endpoint_count"`
	EffectiveEndpointCount int                        `json:"effective_endpoint_count"`
	CategoryCounts         map[personaMemCategory]int `json:"category_counts"`
}

var personaMemSelectedSpecs = []personaMemSampleSpec{
	{
		Split:            "32k",
		ContextID:        "f56dc82b80270d4a2d45ddff57844726783aa968c7ede0114de9894b782c8ac9",
		PersonaID:        "13",
		RawEndpointCount: 1, EffectiveEndpointCount: 1,
		CategoryCounts: map[personaMemCategory]int{
			personaMemRecall: 7, personaMemRevisit: 2,
			personaMemGeneralization: 3, personaMemRecommendation: 1, personaMemSuggestIdeas: 4,
		},
	},
	{
		Split:            "32k",
		ContextID:        "1621543a17bd464feaf4ba73d83d579d0160987536bde8725ac29131ecf235c3",
		PersonaID:        "2",
		RawEndpointCount: 8, EffectiveEndpointCount: 8,
		CategoryCounts: map[personaMemCategory]int{
			personaMemRecall: 6, personaMemEvolution: 10, personaMemRevisit: 6,
			personaMemGeneralization: 4, personaMemSuggestIdeas: 2,
		},
	},
	{
		Split:            "32k",
		ContextID:        "7797915581c01170c2a46616041687c09d59240b417898659034cd31cdc8cebf",
		PersonaID:        "10",
		RawEndpointCount: 10, EffectiveEndpointCount: 10,
		CategoryCounts: map[personaMemCategory]int{
			personaMemRecall: 6, personaMemEvolution: 5, personaMemRevisit: 4,
			personaMemGeneralization: 2, personaMemRecommendation: 1, personaMemSuggestIdeas: 3,
		},
	},
	{
		Split:            "128k",
		ContextID:        "bb7f46b01bc93add9dc337aa563a8a9bbc4a8245adcffc5a527e6a389dd81743",
		PersonaID:        "5",
		RawEndpointCount: 8, EffectiveEndpointCount: 7,
		CategoryCounts: map[personaMemCategory]int{
			personaMemRecall: 3, personaMemLatest: 23, personaMemEvolution: 10, personaMemRevisit: 6,
			personaMemGeneralization: 3, personaMemRecommendation: 6, personaMemSuggestIdeas: 13,
		},
	},
	{
		Split:            "128k",
		ContextID:        "5fa9c2b9e3d3f5e476d2a59210ae5d74f94da5f9aa062b7ccdfc650e936944ad",
		PersonaID:        "4",
		RawEndpointCount: 8, EffectiveEndpointCount: 8,
		CategoryCounts: map[personaMemCategory]int{
			personaMemRecall: 3, personaMemLatest: 14, personaMemEvolution: 4, personaMemRevisit: 7,
			personaMemGeneralization: 5, personaMemRecommendation: 9, personaMemSuggestIdeas: 10,
		},
	},
	{
		Split:            "128k",
		ContextID:        "8d42d864b2b012e2711741b70e107e978aa678ae002d3743070412174e566b51",
		PersonaID:        "15",
		RawEndpointCount: 13, EffectiveEndpointCount: 13,
		CategoryCounts: map[personaMemCategory]int{
			personaMemRecall: 5, personaMemLatest: 13, personaMemEvolution: 8, personaMemRevisit: 6,
			personaMemGeneralization: 3, personaMemRecommendation: 1, personaMemSuggestIdeas: 10,
		},
	},
}

var personaMemExpectedSplitCounts = map[string]map[personaMemCategory]int{
	"32k": {
		personaMemRecall: 146, personaMemLatest: 0, personaMemEvolution: 139, personaMemRevisit: 99,
		personaMemGeneralization: 57, personaMemRecommendation: 55, personaMemSuggestIdeas: 93,
	},
	"128k": {
		personaMemRecall: 171, personaMemLatest: 866, personaMemEvolution: 341, personaMemRevisit: 269,
		personaMemGeneralization: 213, personaMemRecommendation: 349, personaMemSuggestIdeas: 518,
	},
	"1M": {
		personaMemRecall: 144, personaMemLatest: 768, personaMemEvolution: 225, personaMemRevisit: 235,
		personaMemGeneralization: 295, personaMemRecommendation: 280, personaMemSuggestIdeas: 727,
	},
}

var personaMemExpectedHashes = map[string]string{
	"questions_32k.csv":          "cccd34cf53e0bc4d9536c04cff5ca045156d9a4e227e83327112482840bbc93c",
	"shared_contexts_32k.jsonl":  "217247ebfec9e8442fc53570c795ab69f21aad08745f7de78d9beab51b122d4a",
	"questions_128k.csv":         "f0e137c3167fadbffbce5be2786105283c3299972c4ac1f158939155fd1578a7",
	"shared_contexts_128k.jsonl": "733cc009e84a138b386c9e40adea741565db01074f73af4058fd039b42951726",
	"questions_1M.csv":           "f24db37c1ef49e8f3cc6585da3d7e1638bb9bfb32c95df4de065e4666e448e1f",
}

type personaMemMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type personaMemQuestion struct {
	PersonaID   string             `json:"persona_id"`
	QuestionID  string             `json:"question_id"`
	Type        string             `json:"question_type"`
	Category    personaMemCategory `json:"category"`
	Topic       string             `json:"topic"`
	Question    string             `json:"question"`
	Gold        string             `json:"gold"`
	Options     string             `json:"options"`
	ContextID   string             `json:"shared_context_id"`
	RawEndpoint int                `json:"raw_endpoint"`
	Endpoint    int                `json:"effective_endpoint"`
}

type personaMemSample struct {
	Spec          personaMemSampleSpec
	Messages      []personaMemMessage
	MessageBlocks []int
	PersonaSeed   string
	SystemCount   int
	Questions     []personaMemQuestion
}

type personaMemDataAudit struct {
	SHA256        string
	CategoryCount map[personaMemCategory]int
}

type personaMemMemoryCall struct {
	Action      string `json:"action"`
	Query       string `json:"query,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	Outcome     string `json:"outcome"`
	ResultBytes int    `json:"result_bytes"`
	Error       string `json:"error,omitempty"`
}

type personaMemAnswerRecord struct {
	Split           string                 `json:"split"`
	ContextID       string                 `json:"shared_context_id"`
	PersonaID       string                 `json:"persona_id"`
	Endpoint        int                    `json:"endpoint"`
	QuestionID      string                 `json:"question_id"`
	QuestionType    string                 `json:"question_type"`
	Category        personaMemCategory     `json:"category"`
	Question        string                 `json:"question"`
	Gold            string                 `json:"gold"`
	Prediction      string                 `json:"prediction,omitempty"`
	ExtractedAnswer string                 `json:"extracted_answer,omitempty"`
	Completed       bool                   `json:"completed"`
	Correct         bool                   `json:"correct"`
	Attempts        int                    `json:"attempts"`
	ToolsUsed       []string               `json:"tools_used,omitempty"`
	MemoryCalls     []personaMemMemoryCall `json:"memory_calls,omitempty"`
	Error           string                 `json:"error,omitempty"`
	AnsweredAt      time.Time              `json:"answered_at"`
}

type personaMemEndpointState struct {
	Endpoint       int              `json:"endpoint"`
	MemoryVersion  int64            `json:"memory_version"`
	Profile        string           `json:"profile"`
	Knowledge      []memory.Fact    `json:"knowledge"`
	FactWatermarks map[string]int64 `json:"fact_watermarks"`
	PairDigest     string           `json:"pair_digest"`
	CapturedAt     time.Time        `json:"captured_at"`
}

type personaMemContextCheckpoint struct {
	Split                 string                              `json:"split"`
	ContextID             string                              `json:"shared_context_id"`
	PersonaID             string                              `json:"persona_id"`
	UserID                string                              `json:"user_id"`
	ProfileSeeded         bool                                `json:"profile_seeded"`
	LastIngestedExclusive int                                 `json:"last_ingested_exclusive"`
	LastCompletedEndpoint int                                 `json:"last_completed_endpoint"`
	FactWatermarks        map[string]int64                    `json:"fact_watermarks"`
	EndpointStates        map[string]*personaMemEndpointState `json:"endpoint_states"`
	PendingQuestion       *memoryBenchmarkPendingQuestion     `json:"pending_question,omitempty"`
	Answers               map[string]*personaMemAnswerRecord  `json:"answers"`
	PairDigest            string                              `json:"pair_digest,omitempty"`
	ResetRequired         bool                                `json:"reset_required"`
}

type personaMemCheckpoint struct {
	Version                       int                                     `json:"version"`
	Mode                          string                                  `json:"mode"`
	QAPolicy                      string                                  `json:"qa_policy"`
	DatasetSHA256                 map[string]string                       `json:"dataset_sha256"`
	SelectorSeed                  string                                  `json:"selector_seed"`
	SelectorHash                  string                                  `json:"selector_hash"`
	SelectionSHA256               string                                  `json:"selection_sha256"`
	CoreQuestionCount             int                                     `json:"core_question_count"`
	ExtendedQuestionCount         int                                     `json:"extended_question_count"`
	AnswerModel                   string                                  `json:"answer_model"`
	Model                         personaMemModelIdentity                 `json:"model"`
	ReflectReviewerTimeoutSeconds int64                                   `json:"reflect_reviewer_timeout_seconds"`
	ReflectPreWriteMaxAttempts    int                                     `json:"reflect_pre_write_max_attempts"`
	StartedAt                     time.Time                               `json:"started_at"`
	UpdatedAt                     time.Time                               `json:"updated_at"`
	Contexts                      map[string]*personaMemContextCheckpoint `json:"contexts"`
	RunDirectory                  string                                  `json:"run_directory"`
}

type personaMemModelIdentity struct {
	ProviderHost           string                               `json:"provider_host"`
	ProviderEndpointSHA256 string                               `json:"provider_endpoint_sha256"`
	RequestedModel         string                               `json:"requested_model"`
	RouterMetadata         []memoryBenchmarkRemoteModelMetadata `json:"router_metadata"`
	SnapshotStatus         string                               `json:"snapshot_status"`
	SnapshotVerified       bool                                 `json:"snapshot_verified"`
	Note                   string                               `json:"note"`
}

type personaMemRunManifest struct {
	Version                       int                     `json:"version"`
	Mode                          string                  `json:"mode"`
	QAPolicy                      string                  `json:"qa_policy"`
	RepositoryCommit              string                  `json:"repository_commit"`
	AgentID                       string                  `json:"agent_id"`
	DatasetSHA256                 map[string]string       `json:"dataset_sha256"`
	SelectorSeed                  string                  `json:"selector_seed"`
	SelectorHash                  string                  `json:"selector_hash"`
	SelectionSHA256               string                  `json:"selection_sha256"`
	CoreQuestionCount             int                     `json:"core_question_count"`
	ExtendedQuestionCount         int                     `json:"extended_question_count"`
	SelectedContexts              []personaMemSampleSpec  `json:"selected_contexts"`
	Model                         personaMemModelIdentity `json:"model"`
	Thinking                      string                  `json:"thinking"`
	Temperature                   float64                 `json:"temperature"`
	MemoryToolBudget              int                     `json:"memory_tool_budget"`
	MemoryCallAudit               string                  `json:"memory_call_audit"`
	ReflectReviewerTimeoutSeconds int64                   `json:"reflect_reviewer_timeout_seconds"`
	ReflectPreWriteMaxAttempts    int                     `json:"reflect_pre_write_max_attempts"`
	FactLine                      bool                    `json:"fact_line"`
	SkillLine                     bool                    `json:"skill_line"`
	Curator                       bool                    `json:"curator"`
	ProfileSeedPolicy             string                  `json:"profile_seed_policy"`
	EndpointSlicePolicy           string                  `json:"endpoint_slice_policy"`
	CreatedAt                     time.Time               `json:"created_at"`
}

type personaMemScore struct {
	Correct   int      `json:"correct"`
	Completed int      `json:"completed"`
	Expected  int      `json:"expected"`
	Accuracy  *float64 `json:"accuracy,omitempty"`
}

type personaMemScoreReport struct {
	Overall            personaMemScore                        `json:"overall"`
	CoreCategories     map[personaMemCategory]personaMemScore `json:"core_categories"`
	CoreMacroAccuracy  *float64                               `json:"core_macro_accuracy,omitempty"`
	ExtendedCategories map[personaMemCategory]personaMemScore `json:"extended_categories"`
	GeneratedAt        time.Time                              `json:"generated_at"`
}

type personaMemAuditMemoryTool struct {
	inner tools.Tool
	mu    sync.Mutex
	calls []personaMemMemoryCall
}

func (tool *personaMemAuditMemoryTool) Definition() tools.Definition {
	return tool.inner.Definition()
}

func (tool *personaMemAuditMemoryTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	result, err := tool.inner.Execute(ctx, args)
	outcome := "executed"
	// The frozen wrapper returns a successful sentinel for attempts denied
	// before backend execution, so preserve that distinction in artifacts.
	if err == nil && result == memoryBenchmarkBudgetExhaustedMessage {
		outcome = "budget_denied"
	}
	call := personaMemMemoryCall{
		Action:      personaMemStringArg(args, "action"),
		Query:       personaMemStringArg(args, "query"),
		Limit:       memoryBenchmarkIntArg(args, "limit", 0),
		Outcome:     outcome,
		ResultBytes: len(result),
	}
	if err != nil {
		call.Error = err.Error()
	}
	tool.mu.Lock()
	tool.calls = append(tool.calls, call)
	tool.mu.Unlock()
	return result, err
}

func personaMemStringArg(args map[string]any, key string) string {
	value, ok := args[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func (tool *personaMemAuditMemoryTool) Calls() []personaMemMemoryCall {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	return append([]personaMemMemoryCall(nil), tool.calls...)
}

func normalizePersonaMemQuestionType(value string) (personaMemCategory, bool) {
	switch strings.TrimSpace(value) {
	case "recall_user_shared_facts", "recalling_facts_mentioned_by_the_user":
		return personaMemRecall, true
	case "acknowledge_latest_user_preferences", "acknowledge_latest_preferences":
		return personaMemLatest, true
	case "track_full_preference_evolution", "track_full_preference_updates":
		return personaMemEvolution, true
	case "recalling_the_reasons_behind_previous_updates", "revisit_reasons_behind_preference_updates":
		return personaMemRevisit, true
	case "generalize_to_new_scenarios", "generalizing_to_new_scenarios":
		return personaMemGeneralization, true
	case "provide_preference_aligned_recommendations":
		return personaMemRecommendation, true
	case "suggest_new_ideas":
		return personaMemSuggestIdeas, true
	default:
		return "", false
	}
}

func loadPersonaMemDataset(specs []personaMemSampleSpec) ([]personaMemSample, map[string]string, error) {
	samples := make([]personaMemSample, 0, len(specs))
	hashes := make(map[string]string, len(specs)*2)
	for _, spec := range specs {
		questionName := "questions_" + spec.Split + ".csv"
		contextName := "shared_contexts_" + spec.Split + ".jsonl"
		questions, audit, err := loadPersonaMemQuestions(filepath.Join(personaMemDataRoot, questionName), spec.ContextID)
		if err != nil {
			return nil, nil, err
		}
		messages, contextSHA, err := loadPersonaMemContext(filepath.Join(personaMemDataRoot, contextName), spec.ContextID)
		if err != nil {
			return nil, nil, err
		}
		hashes[questionName] = audit.SHA256
		hashes[contextName] = contextSHA
		sample, err := buildPersonaMemSample(spec, questions, messages)
		if err != nil {
			return nil, nil, fmt.Errorf("validate PersonaMem %s/%s: %w", spec.Split, spec.ContextID, err)
		}
		samples = append(samples, sample)
	}
	if err := validatePersonaMemHashes(hashes); err != nil {
		return nil, nil, err
	}
	return samples, hashes, nil
}

func loadPersonaMemQuestions(path, contextID string) ([]personaMemQuestion, personaMemDataAudit, error) {
	payload, err := os.Open(path)
	if err != nil {
		return nil, personaMemDataAudit{}, fmt.Errorf("open PersonaMem questions: %w", err)
	}
	defer func() { _ = payload.Close() }()
	hash := sha256.New()
	reader := csv.NewReader(io.TeeReader(payload, hash))
	reader.ReuseRecord = true
	header, err := reader.Read()
	if err != nil {
		return nil, personaMemDataAudit{}, fmt.Errorf("read PersonaMem question header: %w", err)
	}
	columns := make(map[string]int, len(header))
	for index, name := range header {
		columns[name] = index
	}
	required := []string{"persona_id", "question_id", "question_type", "topic", "user_question_or_message", "correct_answer", "all_options", "shared_context_id", "end_index_in_shared_context"}
	for _, name := range required {
		if _, ok := columns[name]; !ok {
			return nil, personaMemDataAudit{}, fmt.Errorf("PersonaMem questions lack column %q", name)
		}
	}
	audit := personaMemDataAudit{CategoryCount: make(map[personaMemCategory]int)}
	var selected []personaMemQuestion
	for rowNumber := 2; ; rowNumber++ {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, personaMemDataAudit{}, fmt.Errorf("read PersonaMem question row %d: %w", rowNumber, err)
		}
		category, target := normalizePersonaMemQuestionType(row[columns["question_type"]])
		if !target {
			continue
		}
		audit.CategoryCount[category]++
		if row[columns["shared_context_id"]] != contextID {
			continue
		}
		endpoint, err := strconv.Atoi(row[columns["end_index_in_shared_context"]])
		if err != nil {
			return nil, personaMemDataAudit{}, fmt.Errorf("row %d endpoint: %w", rowNumber, err)
		}
		selected = append(selected, personaMemQuestion{
			PersonaID: row[columns["persona_id"]], QuestionID: row[columns["question_id"]],
			Type: row[columns["question_type"]], Category: category, Topic: row[columns["topic"]],
			Question: row[columns["user_question_or_message"]], Gold: row[columns["correct_answer"]],
			Options: row[columns["all_options"]], ContextID: contextID,
			RawEndpoint: endpoint, Endpoint: endpoint,
		})
	}
	audit.SHA256 = hex.EncodeToString(hash.Sum(nil))
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].Endpoint == selected[j].Endpoint {
			return selected[i].QuestionID < selected[j].QuestionID
		}
		return selected[i].Endpoint < selected[j].Endpoint
	})
	return selected, audit, nil
}

func loadPersonaMemContext(path, contextID string) ([]personaMemMessage, string, error) {
	sha, err := personaMemSHA256File(path)
	if err != nil {
		return nil, "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("open PersonaMem contexts: %w", err)
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	var found []personaMemMessage
	for scanner.Scan() {
		var row map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, "", fmt.Errorf("decode PersonaMem context row: %w", err)
		}
		raw, ok := row[contextID]
		if !ok {
			continue
		}
		if found != nil {
			return nil, "", fmt.Errorf("PersonaMem context %s appears more than once", contextID)
		}
		if err := json.Unmarshal(raw, &found); err != nil {
			return nil, "", fmt.Errorf("decode PersonaMem context %s: %w", contextID, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, "", fmt.Errorf("scan PersonaMem contexts: %w", err)
	}
	if found == nil {
		return nil, "", fmt.Errorf("PersonaMem context %s is missing", contextID)
	}
	return found, sha, nil
}

func personaMemSHA256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func buildPersonaMemSample(spec personaMemSampleSpec, questions []personaMemQuestion, messages []personaMemMessage) (personaMemSample, error) {
	if len(questions) == 0 || len(messages) == 0 {
		return personaMemSample{}, fmt.Errorf("questions and messages are required")
	}
	categoryCounts := make(map[personaMemCategory]int)
	endpoints := make(map[int]struct{})
	rawEndpoints := make(map[int]struct{})
	for index := range questions {
		questions[index].Endpoint = personaMemPythonSliceEnd(questions[index].RawEndpoint, len(messages))
	}
	sort.Slice(questions, func(i, j int) bool {
		if questions[i].Endpoint == questions[j].Endpoint {
			return questions[i].QuestionID < questions[j].QuestionID
		}
		return questions[i].Endpoint < questions[j].Endpoint
	})
	for _, question := range questions {
		if question.PersonaID != spec.PersonaID {
			return personaMemSample{}, fmt.Errorf("question %s persona=%s, want %s", question.QuestionID, question.PersonaID, spec.PersonaID)
		}
		if question.Endpoint <= 0 || question.Endpoint > len(messages) {
			return personaMemSample{}, fmt.Errorf("question %s endpoint=%d outside context length %d", question.QuestionID, question.Endpoint, len(messages))
		}
		if messages[question.Endpoint-1].Role == "system" {
			return personaMemSample{}, fmt.Errorf("question %s endpoint ends on a system boundary", question.QuestionID)
		}
		if normalizePersonaMemGold(question.Gold) == "" {
			return personaMemSample{}, fmt.Errorf("question %s has invalid gold %q", question.QuestionID, question.Gold)
		}
		if strings.TrimSpace(question.Options) == "" {
			return personaMemSample{}, fmt.Errorf("question %s has empty options", question.QuestionID)
		}
		categoryCounts[question.Category]++
		endpoints[question.Endpoint] = struct{}{}
		rawEndpoints[question.RawEndpoint] = struct{}{}
	}
	for _, category := range personaMemCategories {
		if categoryCounts[category] != spec.CategoryCounts[category] {
			return personaMemSample{}, fmt.Errorf("%s count=%d, want %d", category, categoryCounts[category], spec.CategoryCounts[category])
		}
	}
	if len(rawEndpoints) != spec.RawEndpointCount || len(endpoints) != spec.EffectiveEndpointCount {
		return personaMemSample{}, fmt.Errorf("raw/effective endpoint counts=%d/%d, want %d/%d",
			len(rawEndpoints), len(endpoints), spec.RawEndpointCount, spec.EffectiveEndpointCount)
	}

	blocks := make([]int, len(messages))
	block := 0
	persona := ""
	for index, message := range messages {
		switch message.Role {
		case "system":
			block++
			if !strings.HasPrefix(strings.TrimSpace(message.Content), personaMemSystemPrefix) {
				return personaMemSample{}, fmt.Errorf("system message %d lacks persona prefix", index)
			}
			if persona == "" {
				persona = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(message.Content), personaMemSystemPrefix))
			} else if message.Content != messages[0].Content {
				return personaMemSample{}, fmt.Errorf("system persona at message %d differs from the first copy", index)
			}
		case "user", "assistant":
			if block == 0 {
				return personaMemSample{}, fmt.Errorf("message %d appears before the first system boundary", index)
			}
		default:
			return personaMemSample{}, fmt.Errorf("message %d has unsupported role %q", index, message.Role)
		}
		blocks[index] = block
	}
	if block == 0 || persona == "" {
		return personaMemSample{}, fmt.Errorf("context has no usable system persona")
	}
	return personaMemSample{
		Spec: spec, Messages: messages, MessageBlocks: blocks,
		PersonaSeed: persona, SystemCount: block, Questions: questions,
	}, nil
}

func personaMemPythonSliceEnd(endpoint, length int) int {
	if endpoint < 0 {
		endpoint = length + endpoint
	}
	if endpoint < 0 {
		return 0
	}
	if endpoint > length {
		return length
	}
	return endpoint
}

func validatePersonaMemHashes(hashes map[string]string) error {
	for name, actual := range hashes {
		if expected := personaMemExpectedHashes[name]; expected != "" && actual != expected {
			return fmt.Errorf("PersonaMem %s SHA256=%s, want %s", name, actual, expected)
		}
	}
	return nil
}

var (
	personaMemParenOption = regexp.MustCompile(`\(([a-d])\)`)
	personaMemWordOption  = regexp.MustCompile(`\b([a-d])\b`)
)

func extractPersonaMemAnswer(prediction, gold string) (bool, string) {
	correct := normalizePersonaMemGold(gold)
	full := prediction
	cleaned := strings.TrimSpace(prediction)
	if index := strings.LastIndex(cleaned, "<final_answer>"); index >= 0 {
		cleaned = strings.TrimSpace(cleaned[index+len("<final_answer>"):])
	}
	if strings.HasSuffix(cleaned, "</final_answer>") {
		cleaned = strings.TrimSpace(strings.TrimSuffix(cleaned, "</final_answer>"))
	}
	cleanedOptions := personaMemOptionSet(cleaned)
	if cleanedOptions[correct] && len(cleanedOptions) == 1 {
		return true, cleaned
	}
	options := personaMemOptionSet(full)
	return options[correct] && len(options) == 1, cleaned
}

func normalizePersonaMemGold(value string) string {
	value = strings.ToLower(strings.Trim(value, "() \t\r\n"))
	if len(value) == 1 && value[0] >= 'a' && value[0] <= 'd' {
		return value
	}
	return ""
}

func personaMemOptionSet(value string) map[string]bool {
	value = strings.ToLower(value)
	matches := personaMemParenOption.FindAllStringSubmatch(value, -1)
	if len(matches) == 0 {
		matches = personaMemWordOption.FindAllStringSubmatch(value, -1)
	}
	result := make(map[string]bool, len(matches))
	for _, match := range matches {
		result[match[1]] = true
	}
	return result
}

func buildPersonaMemQuestionPrompt(question personaMemQuestion) string {
	return question.Question + "\n\n" + personaMemAnswerPrompt + "\n\n" + question.Options
}

func personaMemEndpoints(sample personaMemSample) []int {
	set := make(map[int]struct{})
	for _, question := range sample.Questions {
		set[question.Endpoint] = struct{}{}
	}
	result := make([]int, 0, len(set))
	for endpoint := range set {
		result = append(result, endpoint)
	}
	sort.Ints(result)
	return result
}

func personaMemQuestionsAt(sample personaMemSample, endpoint int) []personaMemQuestion {
	var result []personaMemQuestion
	for _, question := range sample.Questions {
		if question.Endpoint == endpoint {
			result = append(result, question)
		}
	}
	return result
}

func personaMemSelectionSHA256(samples []personaMemSample) string {
	records := make([]string, 0)
	for _, sample := range samples {
		for _, question := range sample.Questions {
			records = append(records, strings.Join([]string{
				sample.Spec.Split,
				sample.Spec.ContextID,
				question.QuestionID,
				string(question.Category),
				strconv.Itoa(question.RawEndpoint),
				strconv.Itoa(question.Endpoint),
			}, "|"))
		}
	}
	sort.Strings(records)
	sum := sha256.Sum256([]byte(strings.Join(records, "\n")))
	return hex.EncodeToString(sum[:])
}

func personaMemQuestionGroupCounts(samples []personaMemSample) (int, int, error) {
	core := 0
	extended := 0
	for _, sample := range samples {
		for _, question := range sample.Questions {
			switch {
			case personaMemIsCoreCategory(question.Category):
				core++
			case personaMemIsExtendedCategory(question.Category):
				extended++
			default:
				return 0, 0, fmt.Errorf("unsupported PersonaMem category %q", question.Category)
			}
		}
	}
	return core, extended, nil
}

func runPersonaMem(ctx context.Context, setupState *setupResult, mode string) error {
	samples, datasetSHA, err := loadPersonaMemDataset(personaMemSelectedSpecs)
	if err != nil {
		return err
	}
	if !sameStringMap(datasetSHA, personaMemExpectedDatasetSHA256) {
		return fmt.Errorf("PersonaMem dataset SHA-256 does not match the fixed v1 evaluation corpus")
	}
	if mode == "smoke" {
		samples = personaMemSmokeSamples(samples)
	}
	selectionSHA := personaMemSelectionSHA256(samples)
	coreQuestionCount, extendedQuestionCount, err := personaMemQuestionGroupCounts(samples)
	if err != nil {
		return err
	}
	agentCfg, err := configureMemoryBenchmarkAgent(
		ctx,
		setupState,
		personaMemAgentID,
		"PersonaMem Benchmark",
		personaMemProviderID,
		personaMemAnswerModelID,
	)
	if err != nil {
		return err
	}
	providerCfg, err := setupState.store.GetProvider(ctx, personaMemProviderID)
	if err != nil {
		return fmt.Errorf("get provider %q: %w", personaMemProviderID, err)
	}
	if !providerCfg.Enabled || strings.TrimSpace(providerCfg.APIKey) == "" || strings.TrimSpace(providerCfg.BaseURL) == "" {
		return fmt.Errorf("provider %q is not enabled with API key and base URL", personaMemProviderID)
	}
	benchmarkSnapshot, err := setupState.snapshotLoader.Snapshot(ctx, agentCfg.ID)
	if err != nil {
		return fmt.Errorf("load PersonaMem credential-aware snapshot: %w", err)
	}
	resolvedCreds := benchmarkSnapshot.ResolveProviderCreds(personaMemProviderID)
	if resolvedCreds.Type != providerCfg.Type || resolvedCreds.APIKey != providerCfg.APIKey ||
		strings.TrimRight(resolvedCreds.BaseURL, "/") != strings.TrimRight(providerCfg.BaseURL, "/") {
		return fmt.Errorf("PersonaMem benchmark agent has a provider credential override; use an isolated home without agent-specific credentials")
	}
	personaMemStream := openaiprovider.NewDeepSeekThinkingDisabledBenchmarkStream(openaiprovider.Config{
		APIKey: providerCfg.APIKey, BaseURL: providerCfg.BaseURL,
	})
	if err := probePersonaMemModel(ctx, providerCfg, personaMemStream); err != nil {
		return fmt.Errorf("probe PersonaMem model %q: %w", personaMemAnswerModelID, err)
	}
	modelIdentity, err := personaMemModelIdentityFromProvider(ctx, providerCfg)
	if err != nil {
		return err
	}

	runName, err := personaMemRunName(mode)
	if err != nil {
		return err
	}
	runDir := filepath.Join(personaMemRunsRoot, runName)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return fmt.Errorf("create PersonaMem run directory: %w", err)
	}
	checkpointPath := filepath.Join(runDir, "checkpoint.json")
	checkpoint, err := loadOrCreatePersonaMemCheckpoint(
		checkpointPath,
		runDir,
		mode,
		modelIdentity,
		datasetSHA,
		selectionSHA,
		coreQuestionCount,
		extendedQuestionCount,
	)
	if err != nil {
		return err
	}
	if err := writePersonaMemManifest(
		runDir,
		mode,
		datasetSHA,
		samples,
		selectionSHA,
		coreQuestionCount,
		extendedQuestionCount,
		agentCfg.ID,
		modelIdentity,
	); err != nil {
		return err
	}

	reflectSvc := reflect.New(reflect.Config{
		StateStore: pluginhost.NewScopedStateStore(setupState.pluginHost.StateStore(), "reflect"),
		Memory:     setupState.mem,
		Store:      setupState.store,
		Snapshots:  setupState.snapshotLoader,
		Providers: func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
			if api != personaMemProviderID {
				return nil, fmt.Errorf("PersonaMem expected provider %q, got %q", personaMemProviderID, api)
			}
			base := openaiprovider.NewDeepSeekThinkingDisabledBenchmarkStream(openaiprovider.Config{
				APIKey: apiKey, BaseURL: baseURL,
			})
			return personaMemReflectStream(base), nil
		},
	})

	for _, sample := range samples {
		key := sample.Spec.Split + ":" + sample.Spec.ContextID
		state := checkpoint.Contexts[key]
		newState := state == nil
		if state == nil {
			state = newPersonaMemContextCheckpoint(mode, sample)
			checkpoint.Contexts[key] = state
		}
		if err := normalizePersonaMemContextCheckpoint(state); err != nil {
			return fmt.Errorf("checkpoint %s: %w", key, err)
		}
		if mode == "smoke" || newState || state.ResetRequired {
			if err := resetPersonaMemContext(ctx, setupState, checkpointPath, checkpoint, state, sample, agentCfg.ID); err != nil {
				return err
			}
		} else {
			if err := ensureMemoryBenchmarkUser(ctx, setupState, state.UserID, agentCfg.ID, "PersonaMem persona "+sample.Spec.PersonaID); err != nil {
				return err
			}
			if state.PendingQuestion != nil {
				if err := recoverMemoryBenchmarkPendingQuestion(ctx, setupState, state.UserID, agentCfg.ID, state.PendingQuestion); err != nil {
					return err
				}
				state.PendingQuestion = nil
				if err := savePersonaMemCheckpoint(checkpointPath, checkpoint); err != nil {
					return err
				}
			}
			if state.PairDigest != "" {
				current, err := personaMemPairDigest(ctx, setupState, state.UserID, agentCfg.ID)
				if err != nil {
					return err
				}
				if current != state.PairDigest {
					state.ResetRequired = true
					_ = savePersonaMemCheckpoint(checkpointPath, checkpoint)
					return fmt.Errorf("PersonaMem %s persisted pair state differs from checkpoint; rerun to rebuild it", key)
				}
			}
		}

		for _, endpoint := range personaMemEndpoints(sample) {
			if endpoint > state.LastCompletedEndpoint {
				if err := ingestPersonaMemEndpoint(ctx, setupState, reflectSvc, checkpointPath, checkpoint, state, sample, agentCfg.ID, endpoint); err != nil {
					return err
				}
			}
			for _, question := range personaMemQuestionsAt(sample, endpoint) {
				if state.Answers[question.QuestionID] != nil {
					continue
				}
				record, err := answerPersonaMemQuestionWithRetry(ctx, setupState, checkpointPath, checkpoint, state, sample, question, agentCfg.ID, personaMemStream)
				if err != nil {
					return err
				}
				state.Answers[question.QuestionID] = record
				if err := savePersonaMemCheckpoint(checkpointPath, checkpoint); err != nil {
					return err
				}
				if err := writePersonaMemArtifacts(runDir, checkpoint, samples); err != nil {
					return err
				}
			}
			current, err := personaMemPairDigest(ctx, setupState, state.UserID, agentCfg.ID)
			if err != nil {
				return err
			}
			if current != state.PairDigest {
				state.ResetRequired = true
				_ = savePersonaMemCheckpoint(checkpointPath, checkpoint)
				return fmt.Errorf("PersonaMem %s endpoint %d changed frozen pair state during QA", key, endpoint)
			}
			fmt.Printf("personamem endpoint complete: split=%s context=%s endpoint=%d questions=%d\n",
				sample.Spec.Split, sample.Spec.ContextID[:12], endpoint, len(personaMemQuestionsAt(sample, endpoint)))
		}
	}
	if err := savePersonaMemCheckpoint(checkpointPath, checkpoint); err != nil {
		return err
	}
	if err := writePersonaMemArtifacts(runDir, checkpoint, samples); err != nil {
		return err
	}
	if err := verifyNoPersonaMemQASessions(ctx, setupState, mode); err != nil {
		return err
	}
	fmt.Printf("PersonaMem %s artifacts: %s\n", mode, runDir)
	return nil
}

func personaMemReflectStream(base providers.StreamFunc) providers.StreamFunc {
	return func(ctx context.Context, model ai.Model, aiCtx ai.Context, opts ai.StreamOptions) (providers.AssistantEventStream, error) {
		// PersonaMem's dense 128k sessions can exceed the production reviewer's
		// two-minute provider budget. Only this local benchmark stream receives
		// the larger timeout; production Reflect and benchmark QA are unchanged.
		if opts.Timeout < personaMemReflectTimeout {
			opts.Timeout = personaMemReflectTimeout
		}
		return base(ctx, model, aiCtx, opts)
	}
}

func personaMemSmokeSamples(samples []personaMemSample) []personaMemSample {
	bestSample := -1
	bestEndpoint := 0
	for sampleIndex, sample := range samples {
		for _, endpoint := range personaMemEndpoints(sample) {
			questions := personaMemQuestionsAt(sample, endpoint)
			if !personaMemQuestionsContainCoreAndExtended(questions) {
				continue
			}
			if bestSample == -1 || endpoint < bestEndpoint {
				bestSample = sampleIndex
				bestEndpoint = endpoint
			}
		}
	}
	if bestSample == -1 {
		return samples
	}
	sample := samples[bestSample]
	questions := personaMemQuestionsAt(sample, bestEndpoint)
	var coreQuestion, extendedQuestion *personaMemQuestion
	for index := range questions {
		if coreQuestion == nil && personaMemIsCoreCategory(questions[index].Category) {
			coreQuestion = &questions[index]
		}
		if extendedQuestion == nil && personaMemIsExtendedCategory(questions[index].Category) {
			extendedQuestion = &questions[index]
		}
	}
	if coreQuestion == nil || extendedQuestion == nil {
		return samples
	}
	sample.Questions = []personaMemQuestion{*coreQuestion, *extendedQuestion}
	return []personaMemSample{sample}
}

func personaMemQuestionsContainCoreAndExtended(questions []personaMemQuestion) bool {
	hasCore := false
	hasExtended := false
	for _, question := range questions {
		hasCore = hasCore || personaMemIsCoreCategory(question.Category)
		hasExtended = hasExtended || personaMemIsExtendedCategory(question.Category)
	}
	return hasCore && hasExtended
}

func personaMemIsCoreCategory(category personaMemCategory) bool {
	switch category {
	case personaMemRecall, personaMemLatest, personaMemEvolution, personaMemRevisit:
		return true
	default:
		return false
	}
}

func personaMemIsExtendedCategory(category personaMemCategory) bool {
	switch category {
	case personaMemGeneralization, personaMemRecommendation, personaMemSuggestIdeas:
		return true
	default:
		return false
	}
}

func newPersonaMemContextCheckpoint(mode string, sample personaMemSample) *personaMemContextCheckpoint {
	return &personaMemContextCheckpoint{
		Split: sample.Spec.Split, ContextID: sample.Spec.ContextID, PersonaID: sample.Spec.PersonaID,
		UserID:         deterministicPersonaMemUserID(mode, sample.Spec.Split, sample.Spec.ContextID),
		FactWatermarks: make(map[string]int64), EndpointStates: make(map[string]*personaMemEndpointState),
		Answers: make(map[string]*personaMemAnswerRecord), ResetRequired: true,
	}
}

func normalizePersonaMemContextCheckpoint(state *personaMemContextCheckpoint) error {
	if state == nil || state.UserID == "" || state.ContextID == "" || state.Split == "" {
		return fmt.Errorf("incomplete context identity")
	}
	if state.FactWatermarks == nil {
		state.FactWatermarks = make(map[string]int64)
	}
	if state.EndpointStates == nil {
		state.EndpointStates = make(map[string]*personaMemEndpointState)
	}
	if state.Answers == nil {
		state.Answers = make(map[string]*personaMemAnswerRecord)
	}
	for questionID, answer := range state.Answers {
		if answer == nil {
			return fmt.Errorf("answer %s is nil", questionID)
		}
		if err := normalizePersonaMemMemoryCallOutcomes(answer.MemoryCalls); err != nil {
			return fmt.Errorf("answer %s memory audit: %w", questionID, err)
		}
	}
	return nil
}

// normalizePersonaMemMemoryCallOutcomes upgrades records produced before the
// audit distinguished real backend execution from host-side budget denial.
func normalizePersonaMemMemoryCallOutcomes(calls []personaMemMemoryCall) error {
	legacy := make([]bool, len(calls))
	missing := 0
	existingDenied := 0
	for index := range calls {
		switch calls[index].Outcome {
		case "":
			legacy[index] = true
			calls[index].Outcome = "executed"
			missing++
		case "executed":
		case "budget_denied":
			existingDenied++
		default:
			return fmt.Errorf("unknown outcome %q", calls[index].Outcome)
		}
	}
	if missing == 0 || len(calls) <= personaMemQAToolBudget {
		return nil
	}

	denied := len(calls) - personaMemQAToolBudget - existingDenied
	if denied < 0 {
		return fmt.Errorf("budget-denied calls exceed attempted calls")
	}
	for index := len(calls) - 1; index >= 0 && denied > 0; index-- {
		call := &calls[index]
		if legacy[index] && call.Error == "" && call.ResultBytes == len(memoryBenchmarkBudgetExhaustedMessage) {
			call.Outcome = "budget_denied"
			denied--
		}
	}
	if denied != 0 {
		return fmt.Errorf("cannot identify %d legacy budget-denied calls", denied)
	}
	return nil
}

func resetPersonaMemContext(
	ctx context.Context,
	setupState *setupResult,
	checkpointPath string,
	checkpoint *personaMemCheckpoint,
	state *personaMemContextCheckpoint,
	sample personaMemSample,
	agentID string,
) error {
	state.ResetRequired = true
	if err := savePersonaMemCheckpoint(checkpointPath, checkpoint); err != nil {
		return err
	}
	if err := resetMemoryBenchmarkPair(ctx, setupState, state.UserID, agentID, "PersonaMem persona "+sample.Spec.PersonaID); err != nil {
		return err
	}
	state.ProfileSeeded = false
	state.LastIngestedExclusive = 0
	state.LastCompletedEndpoint = 0
	state.FactWatermarks = make(map[string]int64)
	state.EndpointStates = make(map[string]*personaMemEndpointState)
	state.PendingQuestion = nil
	state.Answers = make(map[string]*personaMemAnswerRecord)
	state.PairDigest = ""
	writeCtx := authz.WithUserID(ctx, state.UserID)
	writeCtx = authz.WithAgentID(writeCtx, agentID)
	profiles, ok := setupState.mem.(memory.ProfileStore)
	if !ok {
		return fmt.Errorf("memory provider does not support PersonaMem profile seed")
	}
	if err := profiles.SetProfile(writeCtx, state.UserID, agentID, sample.PersonaSeed); err != nil {
		return fmt.Errorf("seed PersonaMem profile: %w", err)
	}
	if err := verifyPersonaMemManualProfileSeed(writeCtx, setupState.mem, state.UserID, agentID, sample.PersonaSeed); err != nil {
		return err
	}
	state.ProfileSeeded = true
	state.ResetRequired = false
	return savePersonaMemCheckpoint(checkpointPath, checkpoint)
}

func verifyPersonaMemManualProfileSeed(ctx context.Context, provider memory.Provider, userID, agentID, want string) error {
	facts, ok := provider.(memory.FactStore)
	if !ok {
		return fmt.Errorf("memory provider does not support fact reads")
	}
	profiles, err := facts.ListActiveFacts(ctx, userID, agentID, memory.FactSubjectUser)
	if err != nil {
		return fmt.Errorf("read PersonaMem profile seed: %w", err)
	}
	if len(profiles) != 1 || profiles[0].Content != want || profiles[0].Source != memory.SourceManual {
		return fmt.Errorf("PersonaMem profile seed did not persist as one manual user fact")
	}
	return nil
}

func ingestPersonaMemEndpoint(
	ctx context.Context,
	setupState *setupResult,
	reflectSvc *reflect.Service,
	checkpointPath string,
	checkpoint *personaMemCheckpoint,
	state *personaMemContextCheckpoint,
	sample personaMemSample,
	agentID string,
	endpoint int,
) error {
	if endpoint <= state.LastIngestedExclusive || endpoint > len(sample.Messages) {
		return fmt.Errorf("PersonaMem endpoint %d cannot follow exclusive index %d", endpoint, state.LastIngestedExclusive)
	}
	state.ResetRequired = true
	if err := savePersonaMemCheckpoint(checkpointPath, checkpoint); err != nil {
		return err
	}
	touched, err := appendPersonaMemRange(ctx, setupState, checkpoint.Mode, state, sample, agentID, state.LastIngestedExclusive, endpoint)
	if err != nil {
		return err
	}
	snapshot, err := setupState.snapshotLoader.Snapshot(ctx, agentID)
	if err != nil {
		return fmt.Errorf("load PersonaMem agent snapshot: %w", err)
	}
	for _, scope := range touched {
		for round := 1; ; round++ {
			result, err := reviewPersonaMemFactLineWithRetry(ctx, reflectSvc, snapshot, scope)
			if err != nil {
				return fmt.Errorf("PersonaMem fact reflect %s round %d: %w", scope.ID, round, err)
			}
			state.FactWatermarks[scope.ID] = result.WatermarkAfter
			fmt.Printf("personamem reflect: split=%s context=%s endpoint=%d session=%s round=%d fresh=%d generated=%d accepted=%d writes=%d noops=%d calls=%d truncated=%t watermark=%d\n",
				sample.Spec.Split, sample.Spec.ContextID[:12], endpoint, scope.ID, round, result.FreshCount,
				result.Generated, result.Accepted, result.Writes, result.Noops, result.LLMCalls, result.Truncated, result.WatermarkAfter)
			if !result.Truncated {
				break
			}
			if result.WatermarkAfter <= result.WatermarkBefore {
				return fmt.Errorf("PersonaMem fact reflect %s made no watermark progress", scope.ID)
			}
		}
	}
	state.LastIngestedExclusive = endpoint
	state.LastCompletedEndpoint = endpoint
	endpointState, err := capturePersonaMemEndpointState(ctx, setupState, state, agentID, endpoint)
	if err != nil {
		return err
	}
	state.PairDigest = endpointState.PairDigest
	state.EndpointStates[strconv.Itoa(endpoint)] = endpointState
	state.ResetRequired = false
	return savePersonaMemCheckpoint(checkpointPath, checkpoint)
}

func reviewPersonaMemFactLineWithRetry(
	ctx context.Context,
	reflectSvc *reflect.Service,
	snapshot *config.Snapshot,
	scope memory.Session,
) (reflect.BenchmarkFactReviewResult, error) {
	var lastErr error
	for attempt := 1; attempt <= personaMemReflectMaxAttempts; attempt++ {
		result, err := reflectSvc.BenchmarkReviewFactLine(ctx, snapshot, scope)
		if err == nil {
			return result, nil
		}
		lastErr = err
		// Only provider/protocol failures explicitly marked as pre-write-safe may
		// retry. Ambiguous write-stage failures still rebuild the whole pair.
		if !reflect.BenchmarkFactReviewRetrySafe(err) || !isMemoryBenchmarkTransient(err) || attempt == personaMemReflectMaxAttempts {
			break
		}
		fmt.Printf("personamem reflect pre-write retry: session=%s attempt=%d error=%v\n", scope.ID, attempt, err)
		time.Sleep(time.Duration(attempt*2) * time.Second)
	}
	return reflect.BenchmarkFactReviewResult{}, lastErr
}

func appendPersonaMemRange(
	ctx context.Context,
	setupState *setupResult,
	mode string,
	state *personaMemContextCheckpoint,
	sample personaMemSample,
	agentID string,
	start, end int,
) ([]memory.Session, error) {
	sessionManager, ok := setupState.mem.(memory.SessionManager)
	if !ok {
		return nil, fmt.Errorf("memory provider does not support sessions")
	}
	type appendBatch struct {
		scope    memory.Session
		block    int
		messages []ai.Message
	}
	var batches []appendBatch
	for index := start; index < end; index++ {
		message := sample.Messages[index]
		if message.Role == "system" {
			continue
		}
		block := sample.MessageBlocks[index]
		sessionID := personaMemHistorySessionID(mode, sample, block)
		scope := memory.Session{ID: sessionID, UserID: state.UserID, AgentID: agentID, Channel: string(session.ChannelCLI)}
		if len(batches) == 0 || batches[len(batches)-1].block != block {
			batches = append(batches, appendBatch{scope: scope, block: block})
		}
		var converted ai.Message
		if message.Role == "user" {
			converted = ai.UserMessage{Content: message.Content}
		} else {
			converted = ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: message.Content}}}
		}
		batches[len(batches)-1].messages = append(batches[len(batches)-1].messages, converted)
	}
	touched := make([]memory.Session, 0, len(batches))
	for _, batch := range batches {
		writeCtx := authz.WithUserID(ctx, state.UserID)
		writeCtx = authz.WithAgentID(writeCtx, agentID)
		if err := sessionManager.SaveInfo(writeCtx, memory.SessionInfo{
			ID: batch.scope.ID, UserID: state.UserID, AgentID: agentID,
			Channel: string(session.ChannelCLI), Kind: string(session.KindChat),
			Title: fmt.Sprintf("PersonaMem %s block %d", sample.Spec.ContextID[:12], batch.block),
		}); err != nil {
			return nil, fmt.Errorf("create PersonaMem history session %s: %w", batch.scope.ID, err)
		}
		if err := setupState.mem.Append(writeCtx, batch.scope, batch.messages...); err != nil {
			return nil, fmt.Errorf("append PersonaMem history %s: %w", batch.scope.ID, err)
		}
		touched = append(touched, batch.scope)
	}
	if len(touched) == 0 {
		return nil, fmt.Errorf("PersonaMem range [%d,%d) contains no user or assistant messages", start, end)
	}
	return touched, nil
}

func personaMemHistorySessionID(mode string, sample personaMemSample, block int) string {
	return fmt.Sprintf("personamem-%s-%s-%s-b%02d", mode, sample.Spec.Split, sample.Spec.ContextID[:12], block)
}

func deterministicPersonaMemUserID(mode, split, contextID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("stella-personamem-v1:"+mode+":"+split+":"+contextID)).String()
}

func answerPersonaMemQuestionWithRetry(
	ctx context.Context,
	setupState *setupResult,
	checkpointPath string,
	checkpoint *personaMemCheckpoint,
	state *personaMemContextCheckpoint,
	sample personaMemSample,
	question personaMemQuestion,
	agentID string,
	stream providers.StreamFunc,
) (*personaMemAnswerRecord, error) {
	var lastErr error
	for attempt := 1; attempt <= personaMemMaxAttempts; attempt++ {
		record, err := answerPersonaMemQuestion(ctx, setupState, checkpointPath, checkpoint, state, sample, question, agentID, stream, attempt)
		if err == nil {
			return record, nil
		}
		lastErr = err
		if !isPersonaMemQATransient(err) {
			return nil, fmt.Errorf("answer PersonaMem question %s: %w", question.QuestionID, err)
		}
		if attempt < personaMemMaxAttempts {
			time.Sleep(time.Duration(attempt*2) * time.Second)
		}
	}
	return &personaMemAnswerRecord{
		Split: sample.Spec.Split, ContextID: sample.Spec.ContextID, PersonaID: sample.Spec.PersonaID,
		Endpoint: question.Endpoint, QuestionID: question.QuestionID, QuestionType: question.Type,
		Category: question.Category, Question: question.Question, Gold: question.Gold,
		Completed: false, Correct: false, Attempts: personaMemMaxAttempts,
		Error: lastErr.Error(), AnsweredAt: time.Now().UTC(),
	}, nil
}

func isPersonaMemQATransient(err error) bool {
	// An empty collected answer has no durable effects after the temporary QA
	// session cleanup, so a fresh attempt is safe just like a provider timeout.
	return errors.Is(err, errPersonaMemEmptyAnswer) || isMemoryBenchmarkTransient(err)
}

func answerPersonaMemQuestion(
	ctx context.Context,
	setupState *setupResult,
	checkpointPath string,
	checkpoint *personaMemCheckpoint,
	state *personaMemContextCheckpoint,
	sample personaMemSample,
	question personaMemQuestion,
	agentID string,
	providerStream providers.StreamFunc,
	attempt int,
) (_ *personaMemAnswerRecord, returnErr error) {
	usage, err := snapshotMemoryBenchmarkKnowledgeUsage(ctx, setupState, state.UserID, agentID)
	if err != nil {
		return nil, err
	}
	qaSessionID := fmt.Sprintf("personamem-%s-qa-%s-%s-a%d", checkpoint.Mode, sample.Spec.ContextID[:12], question.QuestionID, attempt)
	state.PendingQuestion = &memoryBenchmarkPendingQuestion{QAIndex: question.Endpoint, SessionID: qaSessionID, Usage: usage}
	if err := savePersonaMemCheckpoint(checkpointPath, checkpoint); err != nil {
		return nil, err
	}
	defer func() {
		cleanupErr := recoverMemoryBenchmarkPendingQuestion(ctx, setupState, state.UserID, agentID, state.PendingQuestion)
		if cleanupErr == nil {
			state.PendingQuestion = nil
			cleanupErr = savePersonaMemCheckpoint(checkpointPath, checkpoint)
		}
		if cleanupErr != nil {
			returnErr = errors.Join(returnErr, cleanupErr)
		}
	}()

	info := session.NewInfo(qaSessionID, agentID, state.UserID, string(session.ChannelCLI), session.KindChat, "", time.Now().UTC())
	record, err := info.Record()
	if err != nil {
		return nil, err
	}
	writeCtx := authz.WithUserID(ctx, state.UserID)
	writeCtx = authz.WithAgentID(writeCtx, agentID)
	sessionManager, ok := setupState.mem.(memory.SessionManager)
	if !ok {
		return nil, fmt.Errorf("memory provider does not support QA sessions")
	}
	if err := sessionManager.SaveInfo(writeCtx, record); err != nil {
		return nil, fmt.Errorf("create PersonaMem QA session: %w", err)
	}
	service := setupState.poolManager.GetService(agentID)
	if service == nil {
		return nil, fmt.Errorf("agent service %s is unavailable", agentID)
	}
	modelRef := personaMemProviderID + "/" + personaMemAnswerModelID
	frozenMemory := newFrozenMemoryBenchmarkTool(setupState.mem, qaSessionID, personaMemQAToolBudget)
	auditedMemory := &personaMemAuditMemoryTool{inner: frozenMemory}
	toolNames, err := service.Runtime.BenchmarkPrepareRunner(writeCtx, info, modelRef, 0, personaMemQATimeout, auditedMemory)
	if err != nil {
		return nil, fmt.Errorf("prepare PersonaMem QA runner: %w", err)
	}
	if err := service.Runtime.BenchmarkOverrideRunnerStream(writeCtx, info, modelRef, providerStream); err != nil {
		return nil, fmt.Errorf("install PersonaMem non-thinking stream: %w", err)
	}
	var excluded []string
	memoryFound := false
	for _, name := range toolNames {
		if name == memoryBenchmarkToolName {
			memoryFound = true
			continue
		}
		excluded = append(excluded, name)
	}
	if !memoryFound {
		return nil, fmt.Errorf("production memory tool is not registered")
	}
	stream := service.Runtime.Chat(writeCtx, info, buildPersonaMemQuestionPrompt(question),
		runtime.WithModel(modelRef), runtime.WithExcludedTools(excluded...))
	prediction, toolsUsed, err := collectMemoryBenchmarkAnswer(stream)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(prediction) == "" {
		return nil, errPersonaMemEmptyAnswer
	}
	correct, extracted := extractPersonaMemAnswer(prediction, question.Gold)
	return &personaMemAnswerRecord{
		Split: sample.Spec.Split, ContextID: sample.Spec.ContextID, PersonaID: sample.Spec.PersonaID,
		Endpoint: question.Endpoint, QuestionID: question.QuestionID, QuestionType: question.Type,
		Category: question.Category, Question: question.Question, Gold: question.Gold,
		Prediction: strings.TrimSpace(prediction), ExtractedAnswer: extracted,
		Completed: true, Correct: correct, Attempts: attempt, ToolsUsed: toolsUsed,
		MemoryCalls: auditedMemory.Calls(), AnsweredAt: time.Now().UTC(),
	}, nil
}

func capturePersonaMemEndpointState(
	ctx context.Context,
	setupState *setupResult,
	state *personaMemContextCheckpoint,
	agentID string,
	endpoint int,
) (*personaMemEndpointState, error) {
	readCtx := authz.WithUserID(ctx, state.UserID)
	readCtx = authz.WithAgentID(readCtx, agentID)
	profiles, ok := setupState.mem.(memory.ProfileStore)
	if !ok {
		return nil, fmt.Errorf("memory provider does not support profile reads")
	}
	profile, err := profiles.GetProfile(readCtx, state.UserID, agentID)
	if err != nil {
		return nil, fmt.Errorf("read PersonaMem endpoint profile: %w", err)
	}
	facts, ok := setupState.mem.(memory.FactStore)
	if !ok {
		return nil, fmt.Errorf("memory provider does not support fact reads")
	}
	knowledge, err := facts.ListActiveFacts(readCtx, state.UserID, agentID, memory.FactSubjectWorld)
	if err != nil {
		return nil, fmt.Errorf("read PersonaMem endpoint knowledge: %w", err)
	}
	sort.Slice(knowledge, func(i, j int) bool { return knowledge[i].ID < knowledge[j].ID })
	var version int64
	if err := setupState.db.QueryRow(ctx, `SELECT version FROM ctx_agent_memory WHERE user_id=$1 AND agent_id=$2`, state.UserID, agentID).Scan(&version); err != nil {
		return nil, fmt.Errorf("read PersonaMem memory version: %w", err)
	}
	digest, err := personaMemPairDigest(ctx, setupState, state.UserID, agentID)
	if err != nil {
		return nil, err
	}
	watermarks := make(map[string]int64, len(state.FactWatermarks))
	for sessionID, mark := range state.FactWatermarks {
		watermarks[sessionID] = mark
	}
	return &personaMemEndpointState{
		Endpoint: endpoint, MemoryVersion: version, Profile: profile, Knowledge: knowledge,
		FactWatermarks: watermarks, PairDigest: digest, CapturedAt: time.Now().UTC(),
	}, nil
}

func personaMemPairDigest(ctx context.Context, setupState *setupResult, userID, agentID string) (string, error) {
	base, err := memoryBenchmarkPairDigest(ctx, setupState, userID, agentID)
	if err != nil {
		return "", err
	}
	queries := []string{
		`SELECT COALESCE(jsonb_agg(jsonb_build_object('session_id', c.session_id, 'channel', c.channel, 'kind', c.kind) ORDER BY c.session_id)::text, '[]') FROM ctx_conversation c WHERE c.user_id=$1 AND c.agent_id=$2`,
		`SELECT COALESCE(jsonb_agg(jsonb_build_object('session_id', c.session_id, 'seq', m.seq, 'role', m.role, 'event_type', m.event_type, 'content', m.content) ORDER BY c.session_id, m.seq)::text, '[]') FROM ctx_message m JOIN ctx_conversation c ON c.id=m.conversation_id WHERE c.user_id=$1 AND c.agent_id=$2`,
		`SELECT COALESCE(jsonb_agg(jsonb_build_object('plugin_id', p.plugin_id, 'scope_id', p.scope_id, 'state_key', p.state_key, 'value', p.value) ORDER BY p.plugin_id, p.scope_id, p.state_key)::text, '[]') FROM plugin_state p WHERE p.scope_kind='session' AND p.scope_id IN (SELECT c.session_id FROM ctx_conversation c WHERE c.user_id=$1 AND c.agent_id=$2)`,
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(base))
	for _, query := range queries {
		var payload string
		if err := setupState.db.QueryRow(ctx, query, userID, agentID).Scan(&payload); err != nil {
			return "", fmt.Errorf("digest PersonaMem pair state: %w", err)
		}
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(payload))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func verifyNoPersonaMemQASessions(ctx context.Context, setupState *setupResult, mode string) error {
	pattern := "personamem-" + mode + "-qa-%"
	checks := []struct {
		name  string
		query string
	}{
		{name: "conversation", query: `SELECT COUNT(*) FROM ctx_conversation WHERE session_id LIKE $1`},
		{name: "snapshot", query: `SELECT COUNT(*) FROM ctx_agent_memory_snapshot WHERE session_id LIKE $1`},
		{name: "session plugin state", query: `SELECT COUNT(*) FROM plugin_state WHERE scope_kind='session' AND scope_id LIKE $1`},
	}
	for _, check := range checks {
		var count int
		if err := setupState.db.QueryRow(ctx, check.query, pattern).Scan(&count); err != nil {
			return fmt.Errorf("count residual PersonaMem QA %s rows: %w", check.name, err)
		}
		if count != 0 {
			return fmt.Errorf("residual PersonaMem QA %s rows: %d", check.name, count)
		}
	}
	return nil
}

func personaMemModelIdentityFromProvider(ctx context.Context, providerCfg config.Provider) (personaMemModelIdentity, error) {
	parsed, err := url.Parse(providerCfg.BaseURL)
	if err != nil {
		return personaMemModelIdentity{}, fmt.Errorf("parse PersonaMem provider endpoint: %w", err)
	}
	metadata, err := inspectMemoryBenchmarkRemoteModelMetadata(ctx, providerCfg, personaMemAnswerModelID)
	if err != nil {
		return personaMemModelIdentity{}, fmt.Errorf("inspect PersonaMem model metadata: %w", err)
	}
	snapshotStatus, snapshotVerified, err := personaMemSnapshotStatusFromEnv()
	if err != nil {
		return personaMemModelIdentity{}, err
	}
	endpoint := strings.TrimRight(providerCfg.BaseURL, "/")
	endpointSHA := sha256.Sum256([]byte(endpoint))
	note := "The provider alias is not immutable; router metadata does not expose an independent revision ID."
	if snapshotVerified {
		note = "The run operator supplied an explicit snapshot status; router metadata still does not expose an independent revision ID."
	}
	return personaMemModelIdentity{
		ProviderHost: parsed.Hostname(), ProviderEndpointSHA256: hex.EncodeToString(endpointSHA[:]),
		RequestedModel: personaMemAnswerModelID, RouterMetadata: metadata,
		SnapshotStatus: snapshotStatus, SnapshotVerified: snapshotVerified, Note: note,
	}, nil
}

func personaMemSnapshotStatusFromEnv() (string, bool, error) {
	status := strings.TrimSpace(os.Getenv("PERSONAMEM_MODEL_SNAPSHOT_STATUS"))
	if status == "" {
		return personaMemDefaultSnapshotStatus, false, nil
	}
	if len(status) > 128 || strings.ContainsAny(status, "\r\n") {
		return "", false, fmt.Errorf("PERSONAMEM_MODEL_SNAPSHOT_STATUS must be a single line no longer than 128 bytes")
	}
	return status, true, nil
}

func probePersonaMemModel(ctx context.Context, providerCfg config.Provider, stream providers.StreamFunc) error {
	var lastErr error
	for attempt := 1; attempt <= personaMemMaxAttempts; attempt++ {
		if err := probePersonaMemModelOnce(ctx, providerCfg, stream); err == nil {
			return nil
		} else {
			lastErr = err
			if !isMemoryBenchmarkTransient(err) {
				return err
			}
		}
		if attempt < personaMemMaxAttempts {
			time.Sleep(time.Duration(attempt*2) * time.Second)
		}
	}
	return fmt.Errorf("model probe failed after %d attempts: %w", personaMemMaxAttempts, lastErr)
}

func probePersonaMemModelOnce(ctx context.Context, providerCfg config.Provider, stream providers.StreamFunc) error {
	temperature := 0.0
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	message, err := providers.Complete(probeCtx, ai.Model{
		ID: personaMemAnswerModelID, Name: personaMemAnswerModelID, API: providerCfg.Type,
		Provider: providerCfg.ID, BaseURL: providerCfg.BaseURL,
	}, ai.Context{Messages: []ai.Message{ai.UserMessage{Content: "Reply with exactly OK."}}}, ai.CompleteOptions{
		StreamOptions: ai.StreamOptions{Temperature: &temperature, Timeout: 2 * time.Minute},
	}, stream)
	if err != nil {
		return err
	}
	if message.StopReason == ai.StopReasonError {
		return fmt.Errorf("provider: %s", message.ErrorMessage)
	}
	return nil
}

func loadOrCreatePersonaMemCheckpoint(
	path, runDir, mode string,
	model personaMemModelIdentity,
	datasetSHA map[string]string,
	selectionSHA string,
	coreQuestionCount, extendedQuestionCount int,
) (*personaMemCheckpoint, error) {
	payload, err := os.ReadFile(path)
	if err == nil {
		var checkpoint personaMemCheckpoint
		if err := json.Unmarshal(payload, &checkpoint); err != nil {
			return nil, fmt.Errorf("decode PersonaMem checkpoint: %w", err)
		}
		if checkpoint.Version != 3 || checkpoint.Mode != mode || checkpoint.QAPolicy != personaMemQAPolicy ||
			checkpoint.AnswerModel != personaMemAnswerModelID || !sameStringMap(checkpoint.DatasetSHA256, datasetSHA) ||
			!samePersonaMemModelIdentity(checkpoint.Model, model) ||
			checkpoint.SelectorSeed != personaMemSelectorSeed || checkpoint.SelectorHash != personaMemSelectorHash ||
			checkpoint.SelectionSHA256 != selectionSHA || checkpoint.CoreQuestionCount != coreQuestionCount ||
			checkpoint.ExtendedQuestionCount != extendedQuestionCount ||
			checkpoint.ReflectReviewerTimeoutSeconds != int64(personaMemReflectTimeout/time.Second) ||
			checkpoint.ReflectPreWriteMaxAttempts != personaMemReflectMaxAttempts ||
			filepath.Clean(checkpoint.RunDirectory) != filepath.Clean(runDir) {
			return nil, fmt.Errorf("PersonaMem checkpoint configuration does not match this run")
		}
		if checkpoint.Contexts == nil {
			checkpoint.Contexts = make(map[string]*personaMemContextCheckpoint)
		}
		return &checkpoint, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	now := time.Now().UTC()
	checkpoint := &personaMemCheckpoint{
		Version: 3, Mode: mode, QAPolicy: personaMemQAPolicy,
		DatasetSHA256: cloneStringMap(datasetSHA),
		SelectorSeed:  personaMemSelectorSeed, SelectorHash: personaMemSelectorHash,
		SelectionSHA256: selectionSHA, CoreQuestionCount: coreQuestionCount,
		ExtendedQuestionCount: extendedQuestionCount, AnswerModel: personaMemAnswerModelID, Model: model,
		ReflectReviewerTimeoutSeconds: int64(personaMemReflectTimeout / time.Second),
		ReflectPreWriteMaxAttempts:    personaMemReflectMaxAttempts,
		StartedAt:                     now, UpdatedAt: now, Contexts: make(map[string]*personaMemContextCheckpoint),
		RunDirectory: runDir,
	}
	if err := savePersonaMemCheckpoint(path, checkpoint); err != nil {
		return nil, err
	}
	return checkpoint, nil
}

func samePersonaMemModelIdentity(first, second personaMemModelIdentity) bool {
	if first.ProviderHost != second.ProviderHost ||
		first.ProviderEndpointSHA256 != second.ProviderEndpointSHA256 ||
		first.RequestedModel != second.RequestedModel ||
		first.SnapshotStatus != second.SnapshotStatus ||
		first.SnapshotVerified != second.SnapshotVerified ||
		first.Note != second.Note ||
		len(first.RouterMetadata) != len(second.RouterMetadata) {
		return false
	}
	for index := range first.RouterMetadata {
		if first.RouterMetadata[index] != second.RouterMetadata[index] {
			return false
		}
	}
	return true
}

func savePersonaMemCheckpoint(path string, checkpoint *personaMemCheckpoint) error {
	checkpoint.UpdatedAt = time.Now().UTC()
	return writeMemoryBenchmarkJSONAtomic(path, checkpoint)
}

func writePersonaMemManifest(
	runDir, mode string,
	datasetSHA map[string]string,
	samples []personaMemSample,
	selectionSHA string,
	coreQuestionCount, extendedQuestionCount int,
	agentID string,
	model personaMemModelIdentity,
) error {
	commit := "unknown"
	if output, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		commit = strings.TrimSpace(string(output))
	}
	selected := make([]personaMemSampleSpec, 0, len(samples))
	for _, sample := range samples {
		spec := sample.Spec
		spec.CategoryCounts = make(map[personaMemCategory]int)
		for _, question := range sample.Questions {
			spec.CategoryCounts[question.Category]++
		}
		spec.RawEndpointCount = 0
		spec.EffectiveEndpointCount = len(personaMemEndpoints(sample))
		rawEndpoints := make(map[int]struct{})
		for _, question := range sample.Questions {
			rawEndpoints[question.RawEndpoint] = struct{}{}
		}
		spec.RawEndpointCount = len(rawEndpoints)
		selected = append(selected, spec)
	}
	manifest := personaMemRunManifest{
		Version: 2, Mode: mode, QAPolicy: personaMemQAPolicy, RepositoryCommit: commit, AgentID: agentID,
		DatasetSHA256: cloneStringMap(datasetSHA),
		SelectorSeed:  personaMemSelectorSeed, SelectorHash: personaMemSelectorHash,
		SelectionSHA256: selectionSHA, CoreQuestionCount: coreQuestionCount,
		ExtendedQuestionCount: extendedQuestionCount, SelectedContexts: selected, Model: model,
		Thinking: "disabled", Temperature: 0,
		MemoryToolBudget:              personaMemQAToolBudget,
		MemoryCallAudit:               "memory_calls records model attempts; outcome distinguishes executed from host-side budget denial",
		ReflectReviewerTimeoutSeconds: int64(personaMemReflectTimeout / time.Second),
		ReflectPreWriteMaxAttempts:    personaMemReflectMaxAttempts,
		FactLine:                      true, SkillLine: false, Curator: false,
		ProfileSeedPolicy:   "first identical system persona -> one manual subject=user singleton; repeated copies are session boundaries only",
		EndpointSlicePolicy: "exclusive context[:end_index_in_shared_context]",
		CreatedAt:           time.Now().UTC(),
	}
	return writeMemoryBenchmarkJSONAtomic(filepath.Join(runDir, "manifest.json"), manifest)
}

func writePersonaMemArtifacts(runDir string, checkpoint *personaMemCheckpoint, samples []personaMemSample) error {
	answers := make([]*personaMemAnswerRecord, 0)
	endpointStates := make(map[string]map[string]*personaMemEndpointState)
	for key, state := range checkpoint.Contexts {
		for _, answer := range state.Answers {
			answers = append(answers, answer)
		}
		endpointStates[key] = state.EndpointStates
	}
	sort.Slice(answers, func(i, j int) bool {
		if answers[i].Split != answers[j].Split {
			return answers[i].Split < answers[j].Split
		}
		if answers[i].ContextID != answers[j].ContextID {
			return answers[i].ContextID < answers[j].ContextID
		}
		if answers[i].Endpoint != answers[j].Endpoint {
			return answers[i].Endpoint < answers[j].Endpoint
		}
		return answers[i].QuestionID < answers[j].QuestionID
	})
	if err := writeMemoryBenchmarkJSONAtomic(filepath.Join(runDir, "answers.json"), answers); err != nil {
		return err
	}
	if err := writeMemoryBenchmarkJSONAtomic(filepath.Join(runDir, "endpoint_states.json"), endpointStates); err != nil {
		return err
	}
	report := buildPersonaMemScoreReport(samples, answers)
	return writeMemoryBenchmarkJSONAtomic(filepath.Join(runDir, "scores.json"), report)
}

func buildPersonaMemScoreReport(samples []personaMemSample, answers []*personaMemAnswerRecord) personaMemScoreReport {
	report := personaMemScoreReport{
		CoreCategories:     make(map[personaMemCategory]personaMemScore),
		ExtendedCategories: make(map[personaMemCategory]personaMemScore),
		GeneratedAt:        time.Now().UTC(),
	}
	for _, category := range personaMemCoreCategories {
		report.CoreCategories[category] = personaMemScore{}
	}
	for _, category := range personaMemExtendedCategories {
		report.ExtendedCategories[category] = personaMemScore{}
	}
	expectedCategories := make(map[string]personaMemCategory)
	for _, sample := range samples {
		for _, question := range sample.Questions {
			scores := personaMemCategoryScores(&report, question.Category)
			if scores == nil {
				continue
			}
			score := scores[question.Category]
			score.Expected++
			scores[question.Category] = score
			report.Overall.Expected++
			expectedCategories[question.QuestionID] = question.Category
		}
	}
	for _, answer := range answers {
		expectedCategory, ok := expectedCategories[answer.QuestionID]
		if !ok || expectedCategory != answer.Category {
			continue
		}
		scores := personaMemCategoryScores(&report, answer.Category)
		if scores == nil {
			continue
		}
		score := scores[answer.Category]
		if answer.Completed {
			score.Completed++
			report.Overall.Completed++
			if answer.Correct {
				score.Correct++
				report.Overall.Correct++
			}
		}
		scores[answer.Category] = score
	}
	macroTotal := 0.0
	macroAvailable := true
	for _, category := range personaMemCoreCategories {
		score := report.CoreCategories[category]
		score.Accuracy = personaMemAccuracy(score.Correct, score.Completed)
		report.CoreCategories[category] = score
		if score.Accuracy == nil {
			macroAvailable = false
		} else {
			macroTotal += *score.Accuracy
		}
	}
	if macroAvailable {
		value := macroTotal / float64(len(personaMemCoreCategories))
		report.CoreMacroAccuracy = &value
	}
	for _, category := range personaMemExtendedCategories {
		score := report.ExtendedCategories[category]
		score.Accuracy = personaMemAccuracy(score.Correct, score.Completed)
		report.ExtendedCategories[category] = score
	}
	report.Overall.Accuracy = personaMemAccuracy(report.Overall.Correct, report.Overall.Completed)
	return report
}

func personaMemCategoryScores(
	report *personaMemScoreReport,
	category personaMemCategory,
) map[personaMemCategory]personaMemScore {
	if personaMemIsCoreCategory(category) {
		return report.CoreCategories
	}
	if personaMemIsExtendedCategory(category) {
		return report.ExtendedCategories
	}
	return nil
}

func personaMemAccuracy(correct, completed int) *float64 {
	if completed == 0 {
		return nil
	}
	value := float64(correct) / float64(completed)
	return &value
}

func auditPersonaMemQuestionSplits() (map[string]personaMemDataAudit, error) {
	result := make(map[string]personaMemDataAudit, len(personaMemExpectedSplitCounts))
	for _, split := range []string{"32k", "128k", "1M"} {
		path := filepath.Join(personaMemDataRoot, "questions_"+split+".csv")
		_, audit, err := loadPersonaMemQuestions(path, "__no_selected_context__")
		if err != nil {
			return nil, err
		}
		if expected := personaMemExpectedHashes[filepath.Base(path)]; audit.SHA256 != expected {
			return nil, fmt.Errorf("PersonaMem %s question SHA256=%s, want %s", split, audit.SHA256, expected)
		}
		for _, category := range personaMemCategories {
			if got, want := audit.CategoryCount[category], personaMemExpectedSplitCounts[split][category]; got != want {
				return nil, fmt.Errorf("PersonaMem %s %s count=%d, want %d", split, category, got, want)
			}
		}
		result[split] = audit
	}
	return result, nil
}

func inspectPersonaMemData() error {
	audits, err := auditPersonaMemQuestionSplits()
	if err != nil {
		return err
	}
	samples, hashes, err := loadPersonaMemDataset(personaMemSelectedSpecs)
	if err != nil {
		return err
	}
	fmt.Println("PersonaMem target split audit:")
	for _, split := range []string{"32k", "128k", "1M"} {
		fmt.Printf("  %s: recall=%d latest=%d evolution=%d revisit=%d generalization=%d recommendation=%d suggest_new_ideas=%d sha256=%s\n", split,
			audits[split].CategoryCount[personaMemRecall], audits[split].CategoryCount[personaMemLatest],
			audits[split].CategoryCount[personaMemEvolution], audits[split].CategoryCount[personaMemRevisit],
			audits[split].CategoryCount[personaMemGeneralization], audits[split].CategoryCount[personaMemRecommendation],
			audits[split].CategoryCount[personaMemSuggestIdeas], audits[split].SHA256)
	}
	for _, sample := range samples {
		fmt.Printf("PersonaMem selected: split=%s context=%s persona=%s messages=%d systems=%d endpoints=%v questions=%d\n",
			sample.Spec.Split, sample.Spec.ContextID, sample.Spec.PersonaID, len(sample.Messages), sample.SystemCount,
			personaMemEndpoints(sample), len(sample.Questions))
	}
	payload, _ := json.MarshalIndent(hashes, "", "  ")
	fmt.Printf("PersonaMem selected hashes: %s\n", payload)
	core, extended, err := personaMemQuestionGroupCounts(samples)
	if err != nil {
		return err
	}
	fmt.Printf("PersonaMem selected contract: core=%d extended=%d total=%d selection_sha256=%s\n",
		core, extended, core+extended, personaMemSelectionSHA256(samples))
	return nil
}

func sameStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
