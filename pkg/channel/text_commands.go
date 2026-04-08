package channel

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ModelCommandHandler provides the callbacks needed to serve /model in text UIs.
type ModelCommandHandler struct {
	Args                string
	Reply               func(string)
	ListModels          func() []ModelOption
	SwitchModel         func(provider, model string) error
	FormatList          func([]IndexedModel, string) string
	AllowIndexSelection bool
	OnSwitched          func(ModelOption)
}

// HandleModelCommand runs the shared /model flow for text-based channels.
func HandleModelCommand(cfg ModelCommandHandler) {
	query := ParseModelArgs(cfg.Args)
	models := cfg.ListModels()
	formatList := cfg.FormatList
	if formatList == nil {
		formatList = FormatModelList
	}

	if query == "" {
		if len(models) == 0 {
			cfg.Reply("No models configured.")
			return
		}
		cfg.Reply(formatList(IndexModels(models), ""))
		return
	}

	if strings.Contains(query, "/") {
		switchModelByName(cfg, models, query)
		return
	}

	if cfg.AllowIndexSelection {
		if idx, err := strconv.Atoi(query); err == nil {
			switchModelByIndex(cfg, models, idx)
			return
		}
	}

	filtered := FilterModels(models, query)
	if len(filtered) == 0 {
		cfg.Reply(fmt.Sprintf("No models matching %q.", query))
		return
	}
	cfg.Reply(formatList(filtered, query))
}

func switchModelByName(cfg ModelCommandHandler, models []ModelOption, name string) {
	selected, ok := FindModelByName(models, name)
	if !ok {
		cfg.Reply(fmt.Sprintf("Unknown model %q, use /model to list available models.", name))
		return
	}
	confirmModelSwitch(cfg, selected)
}

func switchModelByIndex(cfg ModelCommandHandler, models []ModelOption, idx int) {
	if idx < 1 || idx > len(models) {
		cfg.Reply(fmt.Sprintf("Invalid selection, use a number between 1 and %d.", len(models)))
		return
	}
	confirmModelSwitch(cfg, models[idx-1])
}

func confirmModelSwitch(cfg ModelCommandHandler, selected ModelOption) {
	if err := cfg.SwitchModel(selected.Provider, selected.Model); err != nil {
		cfg.Reply(fmt.Sprintf("Error switching model: %v", err))
		return
	}
	if cfg.OnSwitched != nil {
		cfg.OnSwitched(selected)
	}
	cfg.Reply(fmt.Sprintf("Switched to %s/%s. Session reset.", selected.Provider, selected.Model))
}

// AgentCommandHandler provides the callbacks needed to serve /agent in text UIs.
type AgentCommandHandler struct {
	Ctx         context.Context
	Incoming    IncomingMessage
	Args        string
	Reply       func(string)
	ListAgents  func(context.Context, IncomingMessage) ([]AgentInfo, string, error)
	SwitchAgent func(context.Context, IncomingMessage, string) error
	FormatList  func([]IndexedAgent, string) string
	OnSwitched  func(string)
}

// HandleAgentCommand runs the shared /agent flow for text-based channels.
func HandleAgentCommand(cfg AgentCommandHandler) {
	if cfg.Ctx == nil {
		cfg.Ctx = context.Background()
	}
	formatList := cfg.FormatList
	if formatList == nil {
		formatList = FormatAgentList
	}

	if cfg.Args != "" {
		if err := cfg.SwitchAgent(cfg.Ctx, cfg.Incoming, cfg.Args); err != nil {
			cfg.Reply(fmt.Sprintf("Error switching agent: %v", err))
			return
		}
		if cfg.OnSwitched != nil {
			cfg.OnSwitched(cfg.Args)
		}
		cfg.Reply(fmt.Sprintf("Switched to agent: %s", cfg.Args))
		return
	}

	agents, currentAgentID, err := cfg.ListAgents(cfg.Ctx, cfg.Incoming)
	if err != nil {
		cfg.Reply(fmt.Sprintf("Error listing agents: %v", err))
		return
	}
	cfg.Reply(formatList(IndexAgents(agents), currentAgentID))
}
