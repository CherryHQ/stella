package library

import (
	"context"
	"errors"
	"fmt"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/tools"
)

// ToolName is the provider-facing function name. Keep it compatible with the
// common OpenAI function-name contract; product documentation may still refer
// to the conceptual operation as library.search.
const ToolName = "library_search"

// Tool exposes the single read-only Library retrieval operation. Identity and
// scope are deliberately absent from its arguments and come only from runtime.
type Tool struct {
	spec    LibraryActionTool
	service *Service
}

// NewTool builds one generated library action tool over the Library service.
func NewTool(service *Service, spec LibraryActionTool) *Tool {
	return &Tool{spec: spec, service: service}
}

// RuntimeActionTools is the existing retrieval surface. Phase 1 declares the
// management contracts but intentionally does not register them before their
// service adapters land in Phase 3.
func RuntimeActionTools() []LibraryActionTool {
	for _, spec := range LibraryActionTools() {
		if spec.Name == ToolName {
			return []LibraryActionTool{spec}
		}
	}
	return nil
}

func (t *Tool) Definition() tools.Definition { return t.spec.Definition("") }

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.service == nil {
		return "", ErrServiceUnavailable
	}
	// Strict decoding is what keeps a manually crafted call from selecting
	// scope, identity, files, or fetch modes: the generated input type declares
	// the only two fields this tool has, and anything else is refused before
	// identity is even looked up.
	var input LibrarySearchInput
	if err := tools.DecodeInputStrict(args, &input, []string{"query"}); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidSearch, err)
	}

	identity, err := authz.ToolIdentity(ctx, ToolName)
	if err != nil {
		return "", err
	}
	authority, err := identity.ToAuthority()
	if err != nil {
		return "", authz.MapToolError(ToolName, "", err)
	}
	hits, err := t.service.Search(ctx, authority, input.Query, input.Limit)
	if err != nil {
		if errors.Is(err, ErrForbidden) {
			return "", fmt.Errorf("this run is not authorized to search the Library")
		}
		return "", err
	}
	return tools.MarshalResult(map[string]any{"results": hits})
}
