package groupingest_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/groupingest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/pkg/ai"
)

func TestStructuredGroupMemorySupportsAllGroupTopologies(t *testing.T) {
	tests := []struct {
		name   string
		humans []string
		agents []string
	}{
		{name: "one user multiple agents", humans: []string{"alice"}, agents: []string{"agent-a", "agent-b"}},
		{name: "multiple users one agent", humans: []string{"alice", "bob"}, agents: []string{"agent-a"}},
		{name: "multiple users multiple agents", humans: []string{"alice", "bob"}, agents: []string{"agent-a", "agent-b"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, q := openTestDB(t)
			ctx := context.Background()
			events := eventlog.NewStore(db)
			platformGroupID := "topology-" + strings.ReplaceAll(tc.name, " ", "-")

			for _, humanID := range tc.humans {
				appendTopologyMessage(t, events, platformGroupID, eventlog.ActorHuman, humanID, "public human "+humanID)
			}
			for _, agentID := range tc.agents {
				appendTopologyMessage(t, events, platformGroupID, eventlog.ActorAgent, agentID, "public agent "+agentID)
			}
			trigger := appendTopologyMessage(t, events, platformGroupID, eventlog.ActorHuman, tc.humans[0], "current trigger")

			provider, err := lcm.New(db, nil, nil)
			if err != nil {
				t.Fatalf("new LCM provider: %v", err)
			}

			// One durable Group Fact is shared by every Agent; public events still
			// enter a separate origin-backed LCM timeline for each Agent.
			_, err = memorywrite.ApplyGroupFactPlan(ctx, db, q, memorywrite.ApplyGroupFactPlanRequest{
				GroupID:   trigger.GroupID,
				Pipeline:  "topology-test",
				Watermark: trigger.Seq,
				Plan: memory.GroupFactPlan{Operations: []memory.GroupFactOperation{{
					Action:     memory.GroupFactActionCreate,
					Subject:    memory.GroupFactSubjectGroup,
					NewContent: "Production changes require two approvals.",
				}}},
			})
			if err != nil {
				t.Fatalf("seed Group Fact: %v", err)
			}
			cache, err := groupingest.NewGroupFactCache(provider, groupingest.GroupFactCacheOptions{})
			if err != nil {
				t.Fatalf("new Group Fact cache: %v", err)
			}
			injector, err := groupingest.NewRuntimeInjector(cache)
			if err != nil {
				t.Fatalf("new runtime injector: %v", err)
			}

			var sharedPrompt string
			for _, agentID := range tc.agents {
				session := memory.Session{
					ID:      agentID + ":group:" + trigger.GroupID,
					UserID:  trigger.GroupID,
					AgentID: agentID,
					GroupID: trigger.GroupID,
				}
				if err := provider.SyncGroupEventsBefore(ctx, session, trigger.Seq); err != nil {
					t.Fatalf("%s sync bootstrap: %v", agentID, err)
				}
				history, err := provider.Assemble(ctx, session, 100_000, 6)
				if err != nil {
					t.Fatalf("%s assemble bootstrap: %v", agentID, err)
				}
				historyText := groupHistoryText(history)
				for _, humanID := range tc.humans {
					if !strings.Contains(historyText, "public human "+humanID) {
						t.Fatalf("%s bootstrap missed human %s:\n%s", agentID, humanID, historyText)
					}
				}
				for _, publicAgentID := range tc.agents {
					contains := strings.Contains(historyText, "public agent "+publicAgentID)
					if publicAgentID == agentID && contains {
						t.Fatalf("%s bootstrap duplicated its own public output:\n%s", agentID, historyText)
					}
					if publicAgentID != agentID && !contains {
						t.Fatalf("%s bootstrap missed other Agent %s:\n%s", agentID, publicAgentID, historyText)
					}
				}

				reply := "reply from " + agentID
				if err := provider.AppendGroupTurn(
					ctx,
					session,
					trigger.Message.ID,
					ai.UserMessage{Content: "unused fallback"},
					ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: reply}}},
				); err != nil {
					t.Fatalf("%s append successful turn: %v", agentID, err)
				}
				if err := provider.CommitGroupCursor(ctx, session, trigger.Seq); err != nil {
					t.Fatalf("%s commit cursor: %v", agentID, err)
				}
				// Durable-dispatch retries must neither duplicate the trigger nor
				// append a second private continuation.
				if err := provider.AppendGroupTurn(
					ctx,
					session,
					trigger.Message.ID,
					ai.UserMessage{Content: "unused fallback"},
					ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "duplicate"}}},
				); err != nil {
					t.Fatalf("%s retry successful turn: %v", agentID, err)
				}
				history, err = provider.Assemble(ctx, session, 100_000, 6)
				if err != nil {
					t.Fatalf("%s assemble committed turn: %v", agentID, err)
				}
				historyText = groupHistoryText(history)
				if strings.Count(historyText, "current trigger") != 1 || strings.Count(historyText, reply) != 1 {
					t.Fatalf("%s committed history is not idempotent:\n%s", agentID, historyText)
				}

				prompt, err := injector.Inject(ctx, trigger.GroupID, "base")
				if err != nil {
					t.Fatalf("%s inject Group Facts: %v", agentID, err)
				}
				if !strings.Contains(prompt, "Production changes require two approvals.") {
					t.Fatalf("%s prompt missed shared Group Fact:\n%s", agentID, prompt)
				}
				if sharedPrompt == "" {
					sharedPrompt = prompt
				} else if prompt != sharedPrompt {
					t.Fatalf("Agents received different Group Fact prompts:\n%s\n---\n%s", sharedPrompt, prompt)
				}
			}
		})
	}
}

func appendTopologyMessage(
	t *testing.T,
	store *eventlog.Store,
	platformGroupID string,
	actorType eventlog.ActorType,
	actorID string,
	content string,
) eventlog.AppendResult {
	t.Helper()
	result, err := store.AppendGroupMessage(context.Background(), eventlog.Message{
		Platform:          "test",
		PlatformGroupID:   platformGroupID,
		ActorType:         actorType,
		ActorID:           actorID,
		ActorDisplayName:  strings.ToUpper(actorID),
		Content:           content,
		PlatformMessageID: fmt.Sprintf("%s-%s-%s", platformGroupID, actorType, actorID+"-"+content),
	})
	if err != nil {
		t.Fatalf("append %s %s: %v", actorType, actorID, err)
	}
	return result
}

func groupHistoryText(messages []ai.Message) string {
	var values []string
	for _, message := range messages {
		values = append(values, memory.MessageText(message))
	}
	return strings.Join(values, "\n")
}
