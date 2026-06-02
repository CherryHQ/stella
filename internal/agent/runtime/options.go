package runtime

// Option configures a single Chat call.
type Option func(*chatOptions)

type chatOptions struct {
	model          string
	systemOverride string
	excludedTools  []string
}

// WithModel overrides the model for this Chat call.
func WithModel(model string) Option {
	return func(o *chatOptions) {
		o.model = model
	}
}

// WithSystemOverride overrides the system prompt for this Chat call.
func WithSystemOverride(system string) Option {
	return func(o *chatOptions) {
		o.systemOverride = system
	}
}

// WithExcludedTools hides the named tools for this Chat call.
func WithExcludedTools(names ...string) Option {
	return func(o *chatOptions) {
		o.excludedTools = append(o.excludedTools, names...)
	}
}
