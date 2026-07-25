package groupingest

import (
	"context"
	"fmt"
	"strings"
)

// RuntimeInjector adds the current cached Group Facts to one group turn. The
// cached runner prompt stays immutable; every turn starts from that base prompt.
type RuntimeInjector struct {
	cache *GroupFactCache
}

func NewRuntimeInjector(cache *GroupFactCache) (*RuntimeInjector, error) {
	if cache == nil {
		return nil, fmt.Errorf("group fact cache is required")
	}
	return &RuntimeInjector{cache: cache}, nil
}

func (i *RuntimeInjector) Inject(ctx context.Context, groupID, system string) (string, error) {
	if groupID == "" {
		return system, nil
	}
	block, err := i.cache.GetPromptBlock(ctx, groupID)
	if err != nil {
		return "", fmt.Errorf("load Group Facts: %w", err)
	}
	if block == "" {
		return system, nil
	}
	system = strings.TrimRight(system, "\n")
	if system == "" {
		return block, nil
	}
	return system + "\n\n" + block, nil
}
