package sandbox

import "sync"

// SessionSecretValues carries secret env values injected at session start to tool
// output redaction without marking benign runner env (PATH, HOME, etc.) sensitive.
type SessionSecretValues struct {
	mu     sync.RWMutex
	values []string
}

func NewSessionSecretValues() *SessionSecretValues {
	return &SessionSecretValues{}
}

func (s *SessionSecretValues) Set(values []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values = append([]string(nil), values...)
}

func (s *SessionSecretValues) Values() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.values...)
}
