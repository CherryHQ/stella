package plugin

import (
	"errors"
	"strings"
	"sync"
)

// ContentStore is the migration-only source for legacy package bytes. The
// filesystem resource runtime never reads this CAS; after the migration marker
// is complete the store is no longer opened by startup.
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

func validStoreDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}
