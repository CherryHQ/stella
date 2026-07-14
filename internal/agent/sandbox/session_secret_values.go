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

// Add appends values to the recorded set, retaining the existing ones and
// skipping empties and duplicates. Live refresh uses it to register rotated
// OAuth secret values (access/refresh tokens, client id) for redaction without
// dropping the values captured at session start.
func (s *SessionSecretValues) Add(values ...string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]struct{}, len(s.values))
	for _, v := range s.values {
		seen[v] = struct{}{}
	}
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		s.values = append(s.values, v)
	}
}

func (s *SessionSecretValues) Values() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.values...)
}
