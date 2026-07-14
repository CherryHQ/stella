package sandbox

import "sync"

// OAuthEnvBindings records, for one session, the set of sandbox env var names
// that were injected from an OAuth bundle at session creation. It is the origin
// state that lets RefreshSessionEnv rotate exactly those vars while leaving an
// explicit vault override of the same name untouched (injectSessionEnv skips
// OAuth injection when the vault already provides the var, so an overridden var
// is never recorded here). It is reset on each (re)build so a recreated session
// rebinds cleanly, and is concurrency-safe because refresh reads it while a
// session recreation may be writing it.
type OAuthEnvBindings struct {
	mu    sync.RWMutex
	names map[string]struct{}
}

// NewOAuthEnvBindings returns an empty binding set.
func NewOAuthEnvBindings() *OAuthEnvBindings {
	return &OAuthEnvBindings{}
}

// Set replaces the recorded env var names with names. A nil holder is a no-op so
// callers that never wired one degrade to "nothing is refreshable".
func (b *OAuthEnvBindings) Set(names []string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(names) == 0 {
		b.names = nil
		return
	}
	b.names = make(map[string]struct{}, len(names))
	for _, n := range names {
		b.names[n] = struct{}{}
	}
}

// Has reports whether envVar was recorded as OAuth-injected. A nil holder or an
// unrecorded var returns false, so refresh skips it.
func (b *OAuthEnvBindings) Has(envVar string) bool {
	if b == nil {
		return false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.names[envVar]
	return ok
}
