package memory

import (
	"context"
	"fmt"

	"github.com/vaayne/anna/internal/config"
)

// UserMemoryStore provides per-user-per-agent memory access.
type UserMemoryStore struct {
	store config.Store
}

// NewUserMemoryStore creates a new UserMemoryStore.
func NewUserMemoryStore(store config.Store) *UserMemoryStore {
	return &UserMemoryStore{store: store}
}

// Get returns the current memory content for a user-agent pair.
// Returns empty string if no memory exists.
func (s *UserMemoryStore) Get(ctx context.Context, userID int64, agentID string) (string, error) {
	content, err := s.store.GetUserAgentMemory(ctx, userID, agentID)
	if err != nil {
		return "", fmt.Errorf("get user agent memory: %w", err)
	}
	return content, nil
}

// Set replaces the memory content for a user-agent pair.
func (s *UserMemoryStore) Set(ctx context.Context, userID int64, agentID, content string) error {
	if err := s.store.SetUserAgentMemory(ctx, userID, agentID, content); err != nil {
		return fmt.Errorf("set user agent memory: %w", err)
	}
	return nil
}
