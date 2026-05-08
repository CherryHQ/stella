package channel

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestHandleModelCommandList(t *testing.T) {
	var reply string

	HandleModelCommand(ModelCommandHandler{
		Reply: func(s string) { reply = s },
		ListModels: func() []ModelOption {
			return []ModelOption{{Provider: "openai", Model: "gpt-4"}}
		},
		SwitchModel: func(string, string) error {
			t.Fatal("switch model should not be called")
			return nil
		},
	})

	if !strings.Contains(reply, "openai/gpt-4") {
		t.Fatalf("unexpected reply: %q", reply)
	}
}

func TestHandleModelCommandSwitchByName(t *testing.T) {
	var switched string
	var reply string

	HandleModelCommand(ModelCommandHandler{
		Args:  "openai/gpt-4",
		Reply: func(s string) { reply = s },
		ListModels: func() []ModelOption {
			return []ModelOption{{Provider: "openai", Model: "gpt-4"}}
		},
		SwitchModel: func(provider, model string) error {
			switched = provider + "/" + model
			return nil
		},
	})

	if switched != "openai/gpt-4" {
		t.Fatalf("unexpected switch target: %q", switched)
	}
	if !strings.Contains(reply, "Switched to openai/gpt-4") {
		t.Fatalf("unexpected reply: %q", reply)
	}
}

func TestHandleModelCommandSwitchByIndex(t *testing.T) {
	var switched string

	HandleModelCommand(ModelCommandHandler{
		Args:                "2",
		Reply:               func(string) {},
		AllowIndexSelection: true,
		ListModels: func() []ModelOption {
			return []ModelOption{
				{Provider: "openai", Model: "gpt-4"},
				{Provider: "anthropic", Model: "claude-3"},
			}
		},
		SwitchModel: func(provider, model string) error {
			switched = provider + "/" + model
			return nil
		},
	})

	if switched != "anthropic/claude-3" {
		t.Fatalf("unexpected switch target: %q", switched)
	}
}

func TestHandleModelCommandSwitchError(t *testing.T) {
	var reply string

	HandleModelCommand(ModelCommandHandler{
		Args:  "openai/gpt-4",
		Reply: func(s string) { reply = s },
		ListModels: func() []ModelOption {
			return []ModelOption{{Provider: "openai", Model: "gpt-4"}}
		},
		SwitchModel: func(string, string) error {
			return fmt.Errorf("boom")
		},
	})

	if !strings.Contains(reply, "Error switching model") {
		t.Fatalf("unexpected reply: %q", reply)
	}
}

func TestHandleAgentCommandList(t *testing.T) {
	var reply string

	HandleAgentCommand(AgentCommandHandler{
		Reply: func(s string) { reply = s },
		ListAgents: func(context.Context, IncomingMessage) ([]AgentInfo, string, error) {
			return []AgentInfo{{ID: "stella", Name: "Stella"}}, "stella", nil
		},
		SwitchAgent: func(context.Context, IncomingMessage, string) error {
			t.Fatal("switch agent should not be called")
			return nil
		},
	})

	if !strings.Contains(reply, "Stella") {
		t.Fatalf("unexpected reply: %q", reply)
	}
}
