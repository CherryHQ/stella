package runtime

import (
	"errors"
	"sync"
)

// RunnerBuildResource is deliberately narrower than Runner. A builder can
// publish a partial runner before it has a Chat implementation, while the
// cache only needs the same identity and cleanup surface for retirement.
type RunnerBuildResource interface {
	PluginContext() PluginContext
	Close() error
}

// RunnerBuildOwner keeps construction-time cleanup reachable by the cache.
// The owner starts incomplete: terminal teardown may move it to retired, but
// must not invoke cleanup until the factory has handed back a complete Runner.
// The partial resource remains owned after a failure so Close can retry without
// losing a session, tool registry, hook set, or scratch directory.
type RunnerBuildOwner struct {
	mu            sync.Mutex
	pluginContext PluginContext
	resource      RunnerBuildResource
	complete      bool
}

// NewRunnerBuildOwner captures the admission identity before the factory can
// perform slow workspace/package work.
func NewRunnerBuildOwner(pluginContext PluginContext) *RunnerBuildOwner {
	return &RunnerBuildOwner{pluginContext: pluginContext}
}

// PluginContext returns the immutable admission identity captured before build.
func (o *RunnerBuildOwner) PluginContext() PluginContext {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.pluginContext
}

// AdoptRunner publishes the partial runner before the factory reads
// project/package files. The same object becomes the returned runner on
// success, or remains the retryable cleanup owner on a failed build.
func (o *RunnerBuildOwner) AdoptRunner(resource RunnerBuildResource) error {
	if resource == nil {
		return errors.New("runner build resource is nil")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.complete {
		return errors.New("runner build owner is complete")
	}
	if o.resource != nil {
		return errors.New("runner build owner already has a resource")
	}
	o.resource = resource
	return nil
}

// Complete ends the construction window. Cache calls this before a returned
// runner enters the active or retired set, so no Close can race late attaches.
func (o *RunnerBuildOwner) Complete() {
	o.mu.Lock()
	o.complete = true
	o.mu.Unlock()
}

// Close retries the adopted partial runner until it succeeds. Callers must
// only invoke it after Complete; the cache skips incomplete retired owners.
func (o *RunnerBuildOwner) Close() error {
	o.mu.Lock()
	if !o.complete || o.resource == nil {
		o.mu.Unlock()
		return nil
	}
	resource := o.resource
	o.mu.Unlock()
	return resource.Close()
}
