//go:build personamemeval

package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/tools"
)

type benchmarkRunnerControl interface {
	BenchmarkToolNames() []string
	BenchmarkRegisterTool(tools.Tool)
	BenchmarkSetChatTimeout(time.Duration)
	BenchmarkSetTemperature(float64)
	BenchmarkSetStream(providers.StreamFunc) error
}

// BenchmarkOverrideRunnerStream replaces the cached benchmark runner stream
// after it has been prepared with the production prompt and memory tool.
func (rt *Runtime) BenchmarkOverrideRunnerStream(
	ctx context.Context,
	info session.Info,
	model string,
	stream providers.StreamFunc,
) error {
	_, runner, err := rt.getOrCreateRunner(ctx, info, model, nil)
	if err != nil {
		return err
	}
	controlled, ok := runner.(benchmarkRunnerControl)
	if !ok {
		return fmt.Errorf("benchmark runner control is unavailable")
	}
	return controlled.BenchmarkSetStream(stream)
}

// BenchmarkPrepareRunner creates the production runner, fixes its sampling
// settings, installs benchmark-safe replacement tools, and returns all tool
// names so the caller can exclude anything outside the measured memory path.
func (rt *Runtime) BenchmarkPrepareRunner(
	ctx context.Context,
	info session.Info,
	model string,
	temperature float64,
	chatTimeout time.Duration,
	replacementTools ...tools.Tool,
) ([]string, error) {
	_, runner, err := rt.getOrCreateRunner(ctx, info, model, nil)
	if err != nil {
		return nil, err
	}
	controlled, ok := runner.(benchmarkRunnerControl)
	if !ok {
		return nil, fmt.Errorf("benchmark runner control is unavailable")
	}
	controlled.BenchmarkSetTemperature(temperature)
	controlled.BenchmarkSetChatTimeout(chatTimeout)
	for _, tool := range replacementTools {
		controlled.BenchmarkRegisterTool(tool)
	}
	return controlled.BenchmarkToolNames(), nil
}
