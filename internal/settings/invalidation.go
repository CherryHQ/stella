package settings

// DeferredRunnerInvalidator delays the pool-manager dependency until the
// composition root has finished building the runtime graph. Calls happen only
// after setup, so a missing target is still a hard failure rather than a silent
// no-op.
type DeferredRunnerInvalidator struct{ target RunnerInvalidator }

func NewDeferredRunnerInvalidator() *DeferredRunnerInvalidator     { return &DeferredRunnerInvalidator{} }
func (d *DeferredRunnerInvalidator) Bind(target RunnerInvalidator) { d.target = target }
func (d *DeferredRunnerInvalidator) InvalidateUser(id string) error {
	if d.target == nil {
		return ErrUnavailable
	}
	return d.target.InvalidateUser(id)
}

func (d *DeferredRunnerInvalidator) InvalidateUserAgent(user, agent string) error {
	if d.target == nil {
		return ErrUnavailable
	}
	return d.target.InvalidateUserAgent(user, agent)
}

func (d *DeferredRunnerInvalidator) InvalidateAgent(id string) error {
	if d.target == nil {
		return ErrUnavailable
	}
	return d.target.InvalidateAgent(id)
}

func (d *DeferredRunnerInvalidator) InvalidateAll() error {
	if d.target == nil {
		return ErrUnavailable
	}
	return d.target.InvalidateAll()
}
