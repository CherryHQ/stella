//go:build reflecteval

package reflect

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
)

type reflectEvalCase struct {
	Name            string
	Suite           string
	TargetLine      string
	LabelKind       string
	SourceQuestion  string
	SourceAnswer    string
	SelectionReason string
	Expected        json.RawMessage
	Unit            ReviewUnit
}

type reflectEvalOutput struct {
	Case                     string                         `json:"case"`
	FactGenerationPayload    *factGenerationCapturePayload  `json:"fact_generation_payload,omitempty"`
	FactGenerationToolCalls  []evalCaptureToolCall          `json:"fact_generation_tool_calls,omitempty"`
	FactCandidates           []factCandidate                `json:"fact_candidates,omitempty"`
	FactEvaluations          []factEvaluation               `json:"fact_evaluations,omitempty"`
	FactGate                 CandidateGateResult            `json:"fact_gate,omitempty"`
	SkillGenerationPayload   *skillGenerationCapturePayload `json:"skill_generation_payload,omitempty"`
	SkillGenerationToolCalls []evalCaptureToolCall          `json:"skill_generation_tool_calls,omitempty"`
	SkillCandidates          []skillCandidate               `json:"skill_candidates,omitempty"`
	SkillEvaluations         []skillEvaluation              `json:"skill_evaluations,omitempty"`
	SkillGate                CandidateGateResult            `json:"skill_gate,omitempty"`
	Errors                   map[string]string              `json:"errors,omitempty"`
	RetryAttempts            int                            `json:"retry_attempts,omitempty"`
	RetryErrors              []string                       `json:"retry_errors,omitempty"`
	StartedAt                string                         `json:"started_at"`
	FinishedAt               string                         `json:"finished_at"`
	Model                    string                         `json:"model"`
	Provider                 string                         `json:"provider"`
	Suite                    string                         `json:"suite,omitempty"`
	TargetLine               string                         `json:"target_line,omitempty"`
	LabelKind                string                         `json:"label_kind,omitempty"`
	SourceQuestion           string                         `json:"source_question,omitempty"`
	SourceAnswer             string                         `json:"source_answer,omitempty"`
	SelectionReason          string                         `json:"selection_reason,omitempty"`
	Expected                 json.RawMessage                `json:"expected,omitempty"`
	ReviewText               string                         `json:"-"`
}

type evalCaptureToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type reflectEvalRetryConfig struct {
	MaxRetries int
}

func TestReflectEvalCaseRetryRetriesTransientProviderErrors(t *testing.T) {
	attempts := 0
	result := runReflectEvalCaseWithRetry(context.Background(), reflectEvalRetryConfig{MaxRetries: 1}, func(ctx context.Context) reflectEvalOutput {
		attempts++
		if attempts == 1 {
			return reflectEvalOutput{
				Case:   "transient",
				Errors: map[string]string{"skill_generation": "generate skill candidates: provider: Post \"https://example.test\": unexpected EOF"},
			}
		}
		return reflectEvalOutput{Case: "transient"}
	})

	if attempts != 2 {
		t.Fatalf("expected two attempts, got %d", attempts)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("expected successful retry to clear errors, got %#v", result.Errors)
	}
	if result.RetryAttempts != 1 {
		t.Fatalf("expected one retry attempt, got %d", result.RetryAttempts)
	}
	if len(result.RetryErrors) != 1 || !strings.Contains(result.RetryErrors[0], "unexpected EOF") {
		t.Fatalf("expected retry error history, got %#v", result.RetryErrors)
	}
}

func TestReflectEvalCaseRetryDoesNotRetryProtocolErrors(t *testing.T) {
	attempts := 0
	result := runReflectEvalCaseWithRetry(context.Background(), reflectEvalRetryConfig{MaxRetries: 2}, func(ctx context.Context) reflectEvalOutput {
		attempts++
		return reflectEvalOutput{
			Case:   "protocol",
			Errors: map[string]string{"fact_generation": "generate fact candidates: capture protocol: missing submit_fact_generation"},
		}
	})

	if attempts != 1 {
		t.Fatalf("expected protocol error not to retry, got %d attempts", attempts)
	}
	if result.RetryAttempts != 0 {
		t.Fatalf("expected no retry attempts, got %d", result.RetryAttempts)
	}
	if len(result.Errors) == 0 {
		t.Fatalf("expected final protocol error to remain")
	}
}

func TestReflectCandidateEvalManual(t *testing.T) {
	if os.Getenv("STELLA_REFLECT_EVAL") != "1" {
		t.Skip("set STELLA_REFLECT_EVAL=1 and use -tags reflecteval to run manual LLM eval")
	}

	providerName := envOrDefault("STELLA_REFLECT_EVAL_PROVIDER", "openai")
	modelID := envOrDefault("STELLA_REFLECT_EVAL_MODEL", "gpt-4o")
	stream, err := reflectEvalStream(providerName)
	if err != nil {
		t.Fatal(err)
	}
	model := ai.Model{ID: modelID, Name: modelID, API: providerName, Provider: providerName, MaxTokens: 4096}
	runner := candidateLineReviewer{Stream: stream, Model: model}

	outRoot := filepath.Join(repoRoot(t), "dist", "reflect-evals", "532", time.Now().UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(outRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Logf("writing eval output to %s", outRoot)

	filter := os.Getenv("STELLA_REFLECT_EVAL_CASE")
	caseTimeout := envDuration("STELLA_REFLECT_EVAL_CASE_TIMEOUT", 150*time.Second)
	retryConfig := reflectEvalRetryConfig{MaxRetries: envInt("STELLA_REFLECT_EVAL_MAX_RETRIES", 1)}
	cases := reflectEvalCases(t)
	var summaries []string
	for _, tc := range cases {
		if filter != "" && tc.Name != filter {
			continue
		}
		result := runReflectEvalCaseWithRetry(context.Background(), retryConfig, func(ctx context.Context) reflectEvalOutput {
			caseCtx := ctx
			cancel := func() {}
			if caseTimeout > 0 {
				caseCtx, cancel = context.WithTimeout(ctx, caseTimeout)
			}
			defer cancel()
			return runReflectEvalCase(t, caseCtx, runner, providerName, modelID, tc)
		})
		caseDir := filepath.Join(outRoot, tc.Name)
		if err := os.MkdirAll(caseDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeEvalText(t, filepath.Join(caseDir, "review_unit.txt"), tc.Unit.Text)
		writeEvalJSON(t, filepath.Join(caseDir, "result.json"), result)
		writeEvalMarkdown(t, filepath.Join(caseDir, "summary.md"), result)
		summaries = append(summaries, fmt.Sprintf("- `%s`: fact 接受=%d 拒绝=%d；skill 接受=%d 拒绝=%d；错误=%d；重试=%d",
			tc.Name,
			len(result.FactGate.Accepted), len(result.FactGate.Rejected),
			len(result.SkillGate.Accepted), len(result.SkillGate.Rejected),
			len(result.Errors),
			result.RetryAttempts,
		))
	}
	if len(summaries) == 0 {
		t.Fatalf("no eval case matched STELLA_REFLECT_EVAL_CASE=%q", filter)
	}
	writeEvalText(t, filepath.Join(outRoot, "summary.md"), "# Reflect #532 测试汇总\n\n"+strings.Join(summaries, "\n")+"\n")
}

func runReflectEvalCaseWithRetry(ctx context.Context, config reflectEvalRetryConfig, run func(context.Context) reflectEvalOutput) reflectEvalOutput {
	if config.MaxRetries < 0 {
		config.MaxRetries = 0
	}
	var retryErrors []string
	for attempt := 0; ; attempt++ {
		result := run(ctx)
		if attempt > 0 {
			result.RetryAttempts = attempt
			result.RetryErrors = append([]string(nil), retryErrors...)
		}
		if len(result.Errors) == 0 || attempt >= config.MaxRetries || !isRetryableReflectEvalError(result.Errors) {
			return result
		}
		retryErrors = append(retryErrors, summarizeReflectEvalErrors(result.Errors))
	}
}

func isRetryableReflectEvalError(errors map[string]string) bool {
	if len(errors) == 0 {
		return false
	}
	for _, msg := range errors {
		if !isRetryableReflectEvalErrorMessage(msg) {
			return false
		}
	}
	return true
}

func isRetryableReflectEvalErrorMessage(msg string) bool {
	msg = strings.ToLower(msg)
	if !strings.Contains(msg, "provider:") {
		return false
	}
	for _, marker := range []string{
		"unexpected eof",
		"context deadline exceeded",
		"client.timeout",
		"connection reset",
		"connection refused",
		"temporary failure",
		"tls handshake timeout",
		"status 429",
		"status 500",
		"status 502",
		"status 503",
		"status 504",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func summarizeReflectEvalErrors(errors map[string]string) string {
	keys := make([]string, 0, len(errors))
	for key := range errors {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+": "+errors[key])
	}
	return strings.Join(parts, "; ")
}

func runReflectEvalCase(t *testing.T, ctx context.Context, runner candidateLineReviewer, providerName, modelID string, tc reflectEvalCase) reflectEvalOutput {
	t.Helper()
	started := time.Now().UTC()
	out := reflectEvalOutput{
		Case:            tc.Name,
		Errors:          map[string]string{},
		StartedAt:       started.Format(time.RFC3339),
		Model:           modelID,
		Provider:        providerName,
		Suite:           tc.Suite,
		TargetLine:      tc.TargetLine,
		LabelKind:       tc.LabelKind,
		SourceQuestion:  tc.SourceQuestion,
		SourceAnswer:    tc.SourceAnswer,
		SelectionReason: tc.SelectionReason,
		Expected:        tc.Expected,
		ReviewText:      tc.Unit.Text,
	}

	if shouldRunEvalLine(tc.TargetLine, "fact") {
		payload, toolCalls, err := runFactGenerationForEval(ctx, runner, tc.Unit)
		out.FactGenerationToolCalls = toolCalls
		if err != nil {
			out.Errors["fact_generation"] = err.Error()
			if len(payload.Candidates) > 0 || payload.NoCandidateReason != "" {
				out.FactGenerationPayload = &payload
				out.FactCandidates = payload.Candidates
			}
		} else {
			out.FactGenerationPayload = &payload
			out.FactCandidates = payload.Candidates
			if len(payload.Candidates) > 0 {
				factEvaluations, err := runner.evaluateFactCandidates(ctx, tc.Unit, payload.Candidates)
				if err != nil {
					out.Errors["fact_evaluation"] = err.Error()
				} else {
					out.FactEvaluations = factEvaluations
					out.FactGate = gateFactCandidates(payload.Candidates, factEvaluations, factGateOptions{PrivateOneToOne: tc.Unit.PrivateOneToOne})
				}
			}
		}
	}

	if shouldRunEvalLine(tc.TargetLine, "skill") {
		payload, toolCalls, err := runSkillGenerationForEval(ctx, runner, tc.Unit)
		out.SkillGenerationToolCalls = toolCalls
		if err != nil {
			out.Errors["skill_generation"] = err.Error()
			if len(payload.Candidates) > 0 || payload.NoCandidateReason != "" {
				out.SkillGenerationPayload = &payload
				out.SkillCandidates = payload.Candidates
			}
		} else {
			out.SkillGenerationPayload = &payload
			out.SkillCandidates = payload.Candidates
			if len(payload.Candidates) > 0 {
				skillEvaluations, err := runner.evaluateSkillCandidates(ctx, tc.Unit, payload.Candidates)
				if err != nil {
					out.Errors["skill_evaluation"] = err.Error()
				} else {
					out.SkillEvaluations = skillEvaluations
					out.SkillGate = gateSkillCandidates(payload.Candidates, skillEvaluations)
				}
			}
		}
	}

	if len(out.Errors) == 0 {
		out.Errors = nil
	}
	out.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	return out
}

func runFactGenerationForEval(ctx context.Context, runner candidateLineReviewer, unit ReviewUnit) (factGenerationCapturePayload, []evalCaptureToolCall, error) {
	var payload factGenerationCapturePayload
	result, err := runner.capture(ctx, factCandidateGenerationPrompt, unit.Text, captureProtocol{
		AllowedTools: allowedCaptureTools(toolSubmitFactGeneration),
		SubmitName:   toolSubmitFactGeneration,
		PayloadsValidator: func(calls []ai.ToolCall) error {
			var decodeErr error
			payload, decodeErr = decodeFactGenerationPayloadForEval(calls)
			return decodeErr
		},
	}, factGenerationTools())
	for i := range payload.Candidates {
		if payload.Candidates[i].Ref == "" {
			payload.Candidates[i].Ref = candidateRef("fact", i)
		}
	}
	if err != nil {
		return payload, summarizeEvalToolCalls(result.ToolCalls), fmt.Errorf("generate fact candidates: %w", err)
	}
	return payload, summarizeEvalToolCalls(result.ToolCalls), nil
}

func decodeFactGenerationPayloadForEval(calls []ai.ToolCall) (factGenerationCapturePayload, error) {
	payload, err := decodeSingleCapturePayload[factGenerationCapturePayload](calls, toolSubmitFactGeneration)
	if err != nil {
		return factGenerationCapturePayload{}, err
	}
	if err := validateGenerationBatch(len(payload.Candidates), payload.NoCandidateReason); err != nil {
		return payload, err
	}
	// Keep the manual eval harness aligned with the production generation path.
	if err := validateGeneratedFactCandidates(payload.Candidates); err != nil {
		return payload, err
	}
	return payload, nil
}

func runSkillGenerationForEval(ctx context.Context, runner candidateLineReviewer, unit ReviewUnit) (skillGenerationCapturePayload, []evalCaptureToolCall, error) {
	var payload skillGenerationCapturePayload
	result, err := runner.capture(ctx, skillCandidateGenerationPrompt, unit.Text, captureProtocol{
		AllowedTools: allowedCaptureTools(toolSubmitSkillGeneration),
		SubmitName:   toolSubmitSkillGeneration,
		PayloadsValidator: func(calls []ai.ToolCall) error {
			var decodeErr error
			payload, decodeErr = decodeSkillGenerationPayloadForEval(calls)
			return decodeErr
		},
	}, skillGenerationTools())
	for i := range payload.Candidates {
		if payload.Candidates[i].Ref == "" {
			payload.Candidates[i].Ref = candidateRef("skill", i)
		}
	}
	if err != nil {
		return payload, summarizeEvalToolCalls(result.ToolCalls), fmt.Errorf("generate skill candidates: %w", err)
	}
	return payload, summarizeEvalToolCalls(result.ToolCalls), nil
}

func decodeSkillGenerationPayloadForEval(calls []ai.ToolCall) (skillGenerationCapturePayload, error) {
	payload, err := decodeSingleCapturePayload[skillGenerationCapturePayload](calls, toolSubmitSkillGeneration)
	if err != nil {
		return skillGenerationCapturePayload{}, err
	}
	if err := validateGenerationBatch(len(payload.Candidates), payload.NoCandidateReason); err != nil {
		return payload, err
	}
	// Keep the manual eval harness aligned with the production generation path.
	if err := validateGeneratedSkillCandidates(payload.Candidates); err != nil {
		return payload, err
	}
	return payload, nil
}

func summarizeEvalToolCalls(calls []ai.ToolCall) []evalCaptureToolCall {
	out := make([]evalCaptureToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, evalCaptureToolCall{
			Name:      call.Name,
			Arguments: call.Arguments,
		})
	}
	return out
}

func shouldRunEvalLine(targetLine, line string) bool {
	targetLine = strings.TrimSpace(targetLine)
	return targetLine == "" || targetLine == "both" || targetLine == line
}

func reflectEvalCases(t *testing.T) []reflectEvalCase {
	t.Helper()
	if dir := strings.TrimSpace(os.Getenv("STELLA_REFLECT_EVAL_DATASET_DIR")); dir != "" {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(repoRoot(t), dir)
		}
		return loadCuratedReflectEvalCases(t, dir)
	}
	return []reflectEvalCase{
		{
			Name: "durable_project_fact",
			Unit: ReviewUnit{
				PrivateOneToOne: true,
				FreshCount:      3,
				Text: `<fresh_conversation>
[user] 以后 Stella 这个项目里，提 issue 和 PR 的正文都按 What / Why / How / Refs 组织；不要更新 GitHub Project 字段。
[assistant] 明白，我会按这个项目约定写 issue/PR 文案，并跳过 project board 字段。
[user] 这个约定是长期的，后续相关开发都按这个来。
</fresh_conversation>
`,
			},
		},
		{
			Name: "reusable_debugging_workflow",
			Unit: ReviewUnit{
				PrivateOneToOne: true,
				FreshCount:      5,
				Text: `<fresh_conversation>
[user] 线上任务偶发失败，但日志只有 timeout。
[assistant] 我先没有直接改超时，而是加了分层诊断：记录入口参数、provider request id、stream close reason、DB 状态。
[tool_result_summary] tool=shell call_id=tc1 diagnostics showed provider stream ended before finish event while DB lease remained active
[assistant] 根据诊断，我把 provider stream error 和 capture protocol error 分开处理，并补了回归测试。
[user] 这个方法以后遇到跨 provider / DB / worker 的问题也按这个顺序来：先分层打点定位边界，再修最小根因。
</fresh_conversation>
`,
			},
		},
		{
			Name: "no_durable_signal",
			Unit: ReviewUnit{
				PrivateOneToOne: true,
				FreshCount:      2,
				Text: `<fresh_conversation>
[user] 今天天气还行，随便聊两句。
[assistant] 是，今天适合出去走走。
</fresh_conversation>
`,
			},
		},
		{
			Name: "loaded_skill_delta",
			Unit: ReviewUnit{
				PrivateOneToOne: true,
				FreshCount:      6,
				Text: `<fresh_conversation>
[assistant_tool_call] tool=skills call_id=skill-call-1 action=load name=stella-wsl-dev
[tool_result_summary] tool=skills call_id=skill-call-1 loaded_skill_content_omitted
[assistant] 我按 stella-wsl-dev 在 WSL 里跑了 mise run test，但第一次失败是因为 PowerShell 展开了 Bash 里的 $ 变量。
[assistant] 后来改成更短的 bash -lc，并把复杂正文通过文件传入，避免 PowerShell 提前展开。
[user] 这个坑应该沉淀：Windows Codex 调 WSL 时，带 $、反引号、heredoc 的命令要特别小心，尽量拆短或用临时文件。
</fresh_conversation>

<session_skill_usage>
- action=load skill=stella-wsl-dev call_id=skill-call-1
</session_skill_usage>
`,
			},
		},
		{
			Name: "long_conversation_mixed_signals",
			Unit: ReviewUnit{
				PrivateOneToOne: true,
				FreshCount:      31,
				Text: `<fresh_conversation>
[user] 早上先看一下 main 有没有更新。
[assistant] main 比本地多两个提交，我先只同步，不改业务代码。
[user] 顺手看一下昨天那个 flaky test 的日志。
[assistant] 日志里只有偶发 timeout，没有稳定复现路径。
[user] 这次先别急着修，先确认是不是 CI 机器的问题。
[assistant] 我对比了本地和 CI 的环境变量，暂时没看到直接差异。
[tool_result_summary] tool=shell call_id=long-1 CI logs show timeout happens after provider stream closes while worker lease is still active
[assistant] 初步看更像 provider stream 结束和 worker lease 释放之间的边界问题。
[user] 好，先记下来，下午再看。
[assistant] 我只记录在当前任务上下文里，不做长期沉淀。
[user] 另外 PR 描述里的截图先不用补。
[assistant] 好，这属于这个 PR 的一次性处理。
[user] 刚才那个 API mock 返回 204 也正常，不用记忆。
[assistant] 明白，这也是当前调试细节。
[user] 再看一下 reflect candidate 那块有没有漏测。
[assistant] 目前已有短对话、无信号、loaded skill delta 几类测试。
[user] 长对话也要测，10 轮以上，不然容易被中间噪音带偏。
[assistant] 我会加一个长 review window 的 eval case，混入一次性任务和少数真实信号。
[user] 对了，Stella 的企业管理接口默认走 express-ent-admin.cherryin.ai，这个是这个项目的长期环境事实，后续不要猜成 api.cherryin.ai。
[assistant] 明白，这属于 Stella 项目环境里的长期 world fact。
[user] 继续看刚才 flaky 的问题。
[assistant] 我没有直接调大 timeout，而是先加了三层诊断：raw provider event、normalized capture event、worker lease 状态。
[tool_result_summary] tool=shell call_id=long-2 diagnostics showed raw provider delta lacked finish_reason, normalized capture emitted synthetic close, worker lease stayed active
[assistant] 根据诊断，我把 provider delta 缺 finish_reason 和 capture synthetic close 分开处理，只修 parser 边界。
[user] 这个排查方法以后遇到 provider event / normalized event / worker lease 对不上的问题都按这个顺序：先对比 raw 和 normalized，再看 lease，不要先改 timeout。
[assistant] 明白，这是可复用的排查流程，适合沉淀为 skill 候选。
[user] 最后提醒一下，这次临时开的 DEBUG_REFLECT_TRACE 不要写进长期记忆。
[assistant] 明白，DEBUG_REFLECT_TRACE 是本次临时排查开关，不会沉淀。
</fresh_conversation>
`,
			},
		},
	}
}

type curatedReflectEvalCase struct {
	CaseID          string          `json:"case_id"`
	Suite           string          `json:"suite"`
	TargetLine      string          `json:"target_line"`
	LabelKind       string          `json:"label_kind"`
	SourceQuestion  string          `json:"source_question"`
	SourceAnswer    string          `json:"source_answer"`
	ReviewText      string          `json:"review_text"`
	SelectionReason string          `json:"selection_reason"`
	Expected        json.RawMessage `json:"expected"`
}

// loadCuratedReflectEvalCases reads locally curated JSONL suites. The loader is
// intentionally test-only: it lets manual evals exercise the real #532 runner
// without committing eval data or one-off harness scripts.
func loadCuratedReflectEvalCases(t *testing.T, dir string) []reflectEvalCase {
	t.Helper()
	dir = filepath.Clean(dir)
	suites := selectedEvalSuites()
	limit := envInt("STELLA_REFLECT_EVAL_LIMIT_PER_SUITE", 0)

	var cases []reflectEvalCase
	for _, suite := range suites {
		path := filepath.Join(dir, suite+".jsonl")
		fileCases := readCuratedReflectEvalJSONL(t, path)
		if limit > 0 && len(fileCases) > limit {
			fileCases = fileCases[:limit]
		}
		for _, row := range fileCases {
			cases = append(cases, reflectEvalCase{
				Name:            sanitizeEvalCaseName(row.CaseID),
				Suite:           row.Suite,
				TargetLine:      row.TargetLine,
				LabelKind:       row.LabelKind,
				SourceQuestion:  row.SourceQuestion,
				SourceAnswer:    row.SourceAnswer,
				SelectionReason: row.SelectionReason,
				Expected:        row.Expected,
				Unit: ReviewUnit{
					PrivateOneToOne: true,
					FreshCount:      countFreshConversationMessages(row.ReviewText),
					Text:            row.ReviewText,
				},
			})
		}
	}
	if len(cases) == 0 {
		t.Fatalf("no curated eval cases loaded from %s", dir)
	}
	return cases
}

func selectedEvalSuites() []string {
	if raw := strings.TrimSpace(os.Getenv("STELLA_REFLECT_EVAL_SUITES")); raw != "" {
		parts := strings.Split(raw, ",")
		var suites []string
		for _, part := range parts {
			if suite := strings.TrimSpace(part); suite != "" {
				suites = append(suites, suite)
			}
		}
		return suites
	}
	return []string{
		"fact_user_positive",
		"fact_user_negative",
		"fact_world_negative",
		"skill_negative",
		"fact_agent_negative",
	}
}

func readCuratedReflectEvalJSONL(t *testing.T, path string) []curatedReflectEvalCase {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open curated eval jsonl %s: %v", path, err)
	}
	defer f.Close()

	var cases []curatedReflectEvalCase
	scanner := bufio.NewScanner(f)
	// Some review windows are intentionally long; raise the Scanner limit above
	// the default 64 KiB so large JSONL records still parse locally.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row curatedReflectEvalCase
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("decode %s:%d: %v", path, lineNo, err)
		}
		if strings.TrimSpace(row.CaseID) == "" || strings.TrimSpace(row.ReviewText) == "" {
			t.Fatalf("invalid curated eval row %s:%d: missing case_id or review_text", path, lineNo)
		}
		cases = append(cases, row)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan curated eval jsonl %s: %v", path, err)
	}
	return cases
}

func sanitizeEvalCaseName(name string) string {
	name = strings.TrimSpace(name)
	replacer := strings.NewReplacer(":", "__", "/", "_", "\\", "_", " ", "_")
	return replacer.Replace(name)
}

func countFreshConversationMessages(text string) int {
	count := 0
	for _, marker := range []string{"\n[user]", "\n[assistant]", "\n[tool_result_summary]", "\n[assistant_tool_call]"} {
		count += strings.Count(text, marker)
	}
	if strings.Contains(text, "<fresh_conversation>") && count == 0 {
		return 1
	}
	return count
}

func envInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func envDuration(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	if value, err := time.ParseDuration(raw); err == nil {
		return value
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return time.Duration(value) * time.Second
}

func reflectEvalStream(providerName string) (providers.StreamFunc, error) {
	switch providerName {
	case "openai":
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("OPENAI_API_KEY is required for STELLA_REFLECT_EVAL_PROVIDER=openai")
		}
		return openAICompatNonStreamingStream(openAICompatConfig{
			APIKey:  apiKey,
			BaseURL: envOrDefault("OPENAI_BASE_URL", "https://api.openai.com/v1"),
		}), nil
	default:
		return nil, fmt.Errorf("unsupported STELLA_REFLECT_EVAL_PROVIDER %q", providerName)
	}
}

type openAICompatConfig struct {
	BaseURL string
	APIKey  string
}

func openAICompatNonStreamingStream(cfg openAICompatConfig) providers.StreamFunc {
	client := &http.Client{Timeout: 90 * time.Second}
	return func(ctx context.Context, model ai.Model, aiCtx ai.Context, opts ai.StreamOptions) (providers.AssistantEventStream, error) {
		out := providers.NewChannelEventStream(8)
		go func() {
			out.Emit(ai.EventStart{})
			stopReason, err := callOpenAICompatChat(ctx, client, cfg, model, aiCtx, opts, out)
			if err != nil {
				out.Emit(ai.EventError{Err: err})
				out.Finish(nil)
				return
			}
			out.Emit(ai.EventStop{Reason: stopReason})
			out.Finish(nil)
		}()
		return out, nil
	}
}

func callOpenAICompatChat(ctx context.Context, client *http.Client, cfg openAICompatConfig, model ai.Model, aiCtx ai.Context, opts ai.StreamOptions, out *providers.ChannelEventStream) (ai.StopReason, error) {
	payload := map[string]any{
		"model":       model.Name,
		"messages":    openAICompatMessages(aiCtx),
		"tools":       openAICompatTools(aiCtx.Tools),
		"temperature": 0,
	}
	if opts.MaxTokens != nil {
		payload["max_tokens"] = *opts.MaxTokens
	} else if model.MaxTokens > 0 {
		payload["max_tokens"] = model.MaxTokens
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ai.StopReasonError, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ai.StopReasonError, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return ai.StopReasonError, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return ai.StopReasonError, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ai.StopReasonError, fmt.Errorf("chat completions status %d: %s", resp.StatusCode, truncateEvalError(data))
	}

	var parsed openAICompatChatResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ai.StopReasonError, fmt.Errorf("decode chat completions response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return ai.StopReasonError, fmt.Errorf("chat completions response has no choices")
	}
	message := parsed.Choices[0].Message
	if message.Content != "" {
		out.Emit(ai.EventTextDelta{Text: message.Content})
	}
	for _, call := range message.ToolCalls {
		out.Emit(ai.EventToolCallDelta{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		})
	}
	if len(message.ToolCalls) > 0 || parsed.Choices[0].FinishReason == "tool_calls" {
		return ai.StopReasonToolUse, nil
	}
	return ai.StopReasonStop, nil
}

type openAICompatChatResponse struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

func openAICompatMessages(aiCtx ai.Context) []map[string]string {
	messages := make([]map[string]string, 0, 1+len(aiCtx.Messages))
	if aiCtx.System != "" {
		messages = append(messages, map[string]string{"role": "system", "content": aiCtx.System})
	}
	for _, msg := range aiCtx.Messages {
		role := "user"
		if r := messageRoleForEval(msg); r != "" {
			role = r
		}
		messages = append(messages, map[string]string{"role": role, "content": messageTextForEval(msg)})
	}
	return messages
}

func openAICompatTools(tools []ai.ToolDefinition) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.InputSchema,
			},
		})
	}
	return out
}

func messageRoleForEval(msg ai.Message) string {
	switch msg.(type) {
	case ai.UserMessage:
		return "user"
	case ai.AssistantMessage:
		return "assistant"
	default:
		return "user"
	}
}

func messageTextForEval(msg ai.Message) string {
	if text := memory.MessageText(msg); text != "" {
		return text
	}
	return fmt.Sprint(msg)
}

func truncateEvalError(data []byte) string {
	text := string(data)
	if len(text) <= 1000 {
		return text
	}
	return text[:1000] + "... [truncated]"
}

func writeEvalJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeEvalText(t, path, string(data)+"\n")
}

func writeEvalMarkdown(t *testing.T, path string, result reflectEvalOutput) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", result.Case)
	fmt.Fprintf(&b, "- provider: `%s`\n- model: `%s`\n", result.Provider, result.Model)
	if result.Suite != "" {
		fmt.Fprintf(&b, "- suite: `%s`\n- 测试线: `%s`\n- 标签: `%s`\n", result.Suite, result.TargetLine, result.LabelKind)
	}
	if result.RetryAttempts > 0 {
		fmt.Fprintf(&b, "- 重试次数: `%d`\n", result.RetryAttempts)
	}
	fmt.Fprintf(&b, "- fact 候选: %d，接受: %d，拒绝: %d\n", len(result.FactCandidates), len(result.FactGate.Accepted), len(result.FactGate.Rejected))
	fmt.Fprintf(&b, "- skill 候选: %d，接受: %d，拒绝: %d\n\n", len(result.SkillCandidates), len(result.SkillGate.Accepted), len(result.SkillGate.Rejected))
	if strings.TrimSpace(result.ReviewText) != "" {
		b.WriteString("## 输入\n\n")
		fmt.Fprintf(&b, "```text\n%s\n```\n\n", truncateMarkdownBlock(result.ReviewText, 2000))
	}
	if result.SourceQuestion != "" || result.SourceAnswer != "" || len(result.Expected) > 0 {
		b.WriteString("## 预期\n\n")
		if result.SourceQuestion != "" {
			fmt.Fprintf(&b, "- 来源问题: %s\n", result.SourceQuestion)
		}
		if result.SourceAnswer != "" {
			fmt.Fprintf(&b, "- 来源答案: %s\n", result.SourceAnswer)
		}
		if result.SelectionReason != "" {
			fmt.Fprintf(&b, "- 选入原因: %s\n", result.SelectionReason)
		}
		if len(result.Expected) > 0 {
			fmt.Fprintf(&b, "\n```json\n%s\n```\n\n", prettyRawJSON(result.Expected))
		}
	}
	if len(result.Errors) > 0 {
		b.WriteString("## 错误\n\n")
		for stage, msg := range result.Errors {
			fmt.Fprintf(&b, "- `%s`: %s\n", stage, msg)
		}
		b.WriteString("\n")
	}
	if len(result.RetryErrors) > 0 {
		b.WriteString("## 重试记录\n\n")
		for i, msg := range result.RetryErrors {
			fmt.Fprintf(&b, "- 第 %d 次失败: %s\n", i+1, msg)
		}
		b.WriteString("\n")
	}
	writeFactEvalMarkdown(&b, result)
	writeSkillEvalMarkdown(&b, result)
	b.WriteString("## 人工复核\n\n")
	b.WriteString("- 结论: `todo`\n")
	b.WriteString("- 理由: \n")
	b.WriteString("- 错误类型: \n")
	writeEvalText(t, path, b.String())
}

func writeFactEvalMarkdown(b *strings.Builder, result reflectEvalOutput) {
	if result.TargetLine != "" && !shouldRunEvalLine(result.TargetLine, "fact") {
		return
	}
	b.WriteString("## Fact 输出\n\n")
	if len(result.FactCandidates) == 0 {
		if result.FactGenerationPayload != nil && result.FactGenerationPayload.NoCandidateReason != "" {
			fmt.Fprintf(b, "- 无候选原因: %s\n\n", result.FactGenerationPayload.NoCandidateReason)
		}
		b.WriteString("没有 fact 候选。\n\n")
		return
	}
	evals := factEvalMap(result.FactEvaluations)
	gates := gateDecisionMap(result.FactGate)
	for _, candidate := range result.FactCandidates {
		fmt.Fprintf(b, "### `%s`\n\n", candidate.Ref)
		fmt.Fprintf(b, "- subject: `%s`\n", candidate.Subject)
		fmt.Fprintf(b, "- 内容: %s\n", candidate.Content)
		fmt.Fprintf(b, "- 预期效果: %s\n", candidate.ExpectedEffect)
		if hint := candidate.HandoffHints.KnowledgeSearchQueryHint; hint != "" {
			fmt.Fprintf(b, "- knowledge 搜索提示: `%s`\n", hint)
		}
		writeGateDecisionMarkdown(b, gates[candidate.Ref])
		if evaluation, ok := evals[candidate.Ref]; ok {
			writeScoresMarkdown(b, evaluation.Scores, evaluation.Rationale)
		}
		writeFactEvidenceMarkdown(b, candidate.Evidence)
	}
}

func writeSkillEvalMarkdown(b *strings.Builder, result reflectEvalOutput) {
	if result.TargetLine != "" && !shouldRunEvalLine(result.TargetLine, "skill") {
		return
	}
	b.WriteString("## Skill 输出\n\n")
	if len(result.SkillCandidates) == 0 {
		if result.SkillGenerationPayload != nil && result.SkillGenerationPayload.NoCandidateReason != "" {
			fmt.Fprintf(b, "- 无候选原因: %s\n\n", result.SkillGenerationPayload.NoCandidateReason)
		}
		b.WriteString("没有 skill 候选。\n\n")
		return
	}
	evals := skillEvalMap(result.SkillEvaluations)
	gates := gateDecisionMap(result.SkillGate)
	for _, candidate := range result.SkillCandidates {
		fmt.Fprintf(b, "### `%s`\n\n", candidate.Ref)
		fmt.Fprintf(b, "- 摘要: %s\n", candidate.Learning.Summary)
		fmt.Fprintf(b, "- 可复用增量: %s\n", candidate.Learning.ReusableDelta)
		fmt.Fprintf(b, "- 搜索提示: `%s`\n", candidate.HandoffHints.SearchQueryHint)
		writeGateDecisionMarkdown(b, gates[candidate.Ref])
		if evaluation, ok := evals[candidate.Ref]; ok {
			writeScoresMarkdown(b, evaluation.Scores, evaluation.Rationale)
		}
		writeSkillEvidenceMarkdown(b, candidate.Evidence)
		writeStringListMarkdown(b, "步骤", candidate.Procedure.Steps)
		writeStringListMarkdown(b, "验证", candidate.Procedure.Verification)
	}
}

func writeGateDecisionMarkdown(b *strings.Builder, decision CandidateGateDecision) {
	if decision.Ref == "" {
		b.WriteString("- gate: `未评估`\n")
		return
	}
	status := "接受"
	if decision.Reason != "" {
		status = "拒绝"
	}
	fmt.Fprintf(b, "- gate: `%s`，总分: `%.3f`", status, decision.NormalizedOverall)
	if decision.Reason != "" {
		fmt.Fprintf(b, "，原因: `%s`", decision.Reason)
	}
	b.WriteString("\n")
}

func writeScoresMarkdown(b *strings.Builder, scores map[string]int, rationale string) {
	keys := make([]string, 0, len(scores))
	for key := range scores {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	b.WriteString("- 分数:")
	for _, key := range keys {
		fmt.Fprintf(b, " `%s=%d`", key, scores[key])
	}
	b.WriteString("\n")
	if rationale != "" {
		fmt.Fprintf(b, "- 评分理由: %s\n", rationale)
	}
}

func writeFactEvidenceMarkdown(b *strings.Builder, evidence []factEvidence) {
	for i, item := range evidence {
		fmt.Fprintf(b, "- 证据 %d: `%s` %s\n", i+1, item.SourceType, item.Reason)
		fmt.Fprintf(b, "  - 来源: %s\n", item.Source)
	}
	b.WriteString("\n")
}

func writeSkillEvidenceMarkdown(b *strings.Builder, evidence []skillEvidence) {
	for i, item := range evidence {
		fmt.Fprintf(b, "- 证据 %d: `%s` %s\n", i+1, item.SignalType, item.Reason)
		fmt.Fprintf(b, "  - 来源: %s\n", item.Source)
	}
}

func writeStringListMarkdown(b *strings.Builder, title string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(b, "- %s:\n", title)
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			fmt.Fprintf(b, "  - %s\n", value)
		}
	}
	b.WriteString("\n")
}

func factEvalMap(evaluations []factEvaluation) map[CandidateRef]factEvaluation {
	out := make(map[CandidateRef]factEvaluation, len(evaluations))
	for _, evaluation := range evaluations {
		out[evaluation.Ref] = evaluation
	}
	return out
}

func skillEvalMap(evaluations []skillEvaluation) map[CandidateRef]skillEvaluation {
	out := make(map[CandidateRef]skillEvaluation, len(evaluations))
	for _, evaluation := range evaluations {
		out[evaluation.Ref] = evaluation
	}
	return out
}

func gateDecisionMap(result CandidateGateResult) map[CandidateRef]CandidateGateDecision {
	out := make(map[CandidateRef]CandidateGateDecision, len(result.Accepted)+len(result.Rejected))
	for _, decision := range result.Accepted {
		out[decision.Ref] = decision
	}
	for _, decision := range result.Rejected {
		out[decision.Ref] = decision
	}
	return out
}

func prettyRawJSON(raw json.RawMessage) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(data)
}

func truncateMarkdownBlock(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "\n... [truncated; see review_unit.txt for full input]"
}

func writeEvalText(t *testing.T, path string, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		next := filepath.Dir(wd)
		if next == wd {
			t.Fatal("could not find repo root")
		}
		wd = next
	}
}
