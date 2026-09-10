//go:build personamemeval

package agent

import (
	"sort"
	"time"

	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/tools"
)

// BenchmarkToolNames returns a stable list so an end-to-end benchmark can hide
// every unrelated tool while retaining the production memory query tool.
func (r *runner) BenchmarkToolNames() []string {
	if r.tools == nil {
		return nil
	}
	names := r.tools.BuiltinNames()
	sort.Strings(names)
	return names
}

// BenchmarkSetTemperature changes only the build-tagged benchmark runner.
func (r *runner) BenchmarkSetTemperature(value float64) {
	r.streamOptions.Temperature = &value
}

// BenchmarkSetChatTimeout bounds one benchmark question without changing the
// production runner defaults.
func (r *runner) BenchmarkSetChatTimeout(value time.Duration) {
	r.chatTimeout = value
}

// BenchmarkRegisterTool replaces a tool by name in the cached runner registry.
func (r *runner) BenchmarkRegisterTool(tool tools.Tool) {
	r.tools.Register(tool)
}

// BenchmarkSetStream swaps the benchmark runner's provider stream while
// preserving its production prompt and tool registry.
func (r *runner) BenchmarkSetStream(stream providers.StreamFunc) error {
	coreRunner, err := newAgentRunner(stream, r.tools, r.model, r.streamOptions, r.system, r.hookSet, r.toolLifecycle, r.canonicalImages)
	if err != nil {
		return err
	}
	r.stream = stream
	r.runner = coreRunner
	return nil
}
