package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
)

const probeFailedHint = "MCP probe failed"

// validateCatalogTools rejects names that collide after the public export
// normalization. A partial catalog would make the model-facing tool set
// nondeterministic, so one bad entry invalidates the whole probe.
func validateCatalogTools(reg Registration, catalog []CatalogTool) error {
	seen := make(map[string]struct{}, len(catalog))
	for _, item := range catalog {
		name := exportedToolName(reg, item.Name)
		if name == "" {
			return fmt.Errorf("mcp: invalid discovered tool name")
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("mcp: discovered catalog has duplicate exported tool name")
		}
		seen[name] = struct{}{}
	}
	return nil
}

// ProbeFile performs one disposable tools/list against a file-backed MCP
// declaration. File registrations have no common plugin_config child or
// revision, so their observation belongs to this response and must not be
// written through the legacy common observation tables.
func (s *Service) ProbeFile(ctx context.Context, reg Registration, authority authz.Authority) (Registration, error) {
	if s == nil || !reg.IsFile() {
		return Registration{}, errPluginCredentialsUnavailable
	}
	if _, err := FileCredentialOwner(reg, authority); err != nil {
		return Registration{}, err
	}
	timeout := s.probeTimeout
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	session := NewFileSession(s)
	if s.fileSessionFactory != nil {
		session = s.fileSessionFactory(s)
	}
	defer func() { _ = session.Close() }()
	if err := session.Prepare(probeCtx, []Registration{reg}, authority); err != nil {
		return fileProbeFailure(reg, err), nil
	}
	conn, err := session.borrow(probeCtx, reg, authority)
	if err != nil {
		return fileProbeFailure(reg, err), nil
	}
	client, err := conn.get()
	if err != nil {
		return fileProbeFailure(reg, err), nil
	}
	remote, err := client.ListTools(probeCtx)
	if err != nil {
		return fileProbeFailure(reg, err), nil
	}
	catalog := make([]CatalogTool, 0, len(remote))
	for _, tool := range remote {
		catalog = append(catalog, CatalogTool{
			Name: tool.Name, Description: tool.Description,
			InputSchema: cloneSchema(toolInputSchema(tool.InputSchema)),
			Annotations: annotationsSchema(tool.Annotations),
		})
	}
	if err := validateCatalogTools(reg, catalog); err != nil {
		return Registration{}, err
	}
	reg.Status = StatusOK
	reg.StatusError = ""
	reg.ProbedAt = time.Now().UTC()
	reg.Tools = catalog
	return reg, nil
}

func fileProbeFailure(reg Registration, err error) Registration {
	reg.Status = StatusError
	reg.StatusError = probeFailedHint
	if isCredentialRejection(err) || errors.Is(err, errFileMCPGrantRevoked) || errors.Is(err, authz.ErrForbidden) {
		reg.Status = StatusNeedsAuth
		reg.StatusError = credentialRejectedHint
	}
	reg.ProbedAt = time.Now().UTC()
	reg.Tools = []CatalogTool{}
	return reg
}
