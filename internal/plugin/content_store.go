package plugin

import (
	"errors"
	"strings"
	"sync"

	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

// ContentStore serializes publication and future reachability scans for one
// package CAS root. The pointer is shared by shallow transaction-bound Service
// copies; never embed this mutex in Service itself.
type ContentStore struct {
	root string
	mu   sync.Mutex
}

func NewContentStore(root string) (*ContentStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("plugin: content store root is required")
	}
	return &ContentStore{root: root}, nil
}

// withPublished holds the store lock from staging through the caller's
// database CAS. This gives GC a single ordering rule: Store, then admission.
// The callback must stay short after publication and must not perform unrelated
// file I/O.
func (s *ContentStore) withPublished(source string, fn func(agentpackage.PublishedPackage) error) error {
	if s == nil || fn == nil {
		return errors.New("plugin: content store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	published, err := agentpackage.PublishDirectory(source, s.root)
	if err != nil {
		return err
	}
	return fn(published)
}
