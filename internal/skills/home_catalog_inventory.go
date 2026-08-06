package skills

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// StorageHomeCatalogInventory supplies catalog identities from ready Home
// registry records only. It deliberately has no content or lifecycle-table
// dependency: PostgreSQL is an identity inventory at this boundary.
type StorageHomeCatalogInventory struct{ q *sqlc.Queries }

func NewStorageHomeCatalogInventory(q *sqlc.Queries) (*StorageHomeCatalogInventory, error) {
	if q == nil {
		return nil, errors.New("skills: storage Home catalog inventory queries are required")
	}
	return &StorageHomeCatalogInventory{q: q}, nil
}

func (i *StorageHomeCatalogInventory) ListRoots(ctx context.Context) ([]HomeCatalogRoot, error) {
	if i == nil || i.q == nil {
		return nil, errors.New("skills: storage Home catalog inventory is unavailable")
	}
	rows, err := i.q.ListReadyStorageHomeCatalogRoots(ctx)
	if err != nil {
		return nil, fmt.Errorf("skills: list ready storage Home catalog roots: %w", err)
	}
	roots := make([]HomeCatalogRoot, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		root, include, err := homeCatalogRootFromStorageHome(homeCatalogInventoryRecord{
			homeKind:         row.HomeKind,
			principalKind:    row.PrincipalKind.String,
			principalID:      row.PrincipalID.String,
			agentID:          row.AgentID.String,
			hasPrincipalKind: row.PrincipalKind.Valid,
			hasPrincipalID:   row.PrincipalID.Valid,
			hasAgentID:       row.AgentID.Valid,
		})
		if err != nil {
			return nil, fmt.Errorf("skills: invalid ready storage Home catalog identity: %w", err)
		}
		if !include {
			continue
		}
		key, err := encodeFilesystemSkillID(root.Scope, root.UserID, root.AgentID, "inventory")
		if err != nil {
			return nil, fmt.Errorf("skills: invalid ready storage Home catalog identity: %w", err)
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		roots = append(roots, root)
	}
	sort.Slice(roots, func(a, b int) bool {
		if roots[a].Scope != roots[b].Scope {
			return roots[a].Scope < roots[b].Scope
		}
		if roots[a].UserID != roots[b].UserID {
			return roots[a].UserID < roots[b].UserID
		}
		return roots[a].AgentID < roots[b].AgentID
	})
	return roots, nil
}

type homeCatalogInventoryRecord struct {
	homeKind, principalKind, principalID, agentID string
	hasPrincipalKind, hasPrincipalID, hasAgentID  bool
}

func homeCatalogRootFromStorageHome(row homeCatalogInventoryRecord) (HomeCatalogRoot, bool, error) {
	noPrincipal := !row.hasPrincipalKind && !row.hasPrincipalID
	switch row.homeKind {
	case "system_skill":
		if !noPrincipal || row.hasAgentID {
			return HomeCatalogRoot{}, false, errors.New("system_skill has owners")
		}
		root := HomeCatalogRoot{Scope: "system"}
		_, err := homeCatalogSkillRoot(root)
		return root, true, err
	case "system_agent_skill":
		if !noPrincipal || !row.hasAgentID {
			return HomeCatalogRoot{}, false, errors.New("system_agent_skill identity is incomplete")
		}
		root := HomeCatalogRoot{Scope: "system_agent", AgentID: row.agentID}
		_, err := homeCatalogSkillRoot(root)
		return root, true, err
	case "principal":
		if !row.hasPrincipalKind || !row.hasPrincipalID || row.hasAgentID {
			return HomeCatalogRoot{}, false, errors.New("principal Home identity is incomplete")
		}
		if row.principalKind == "group" {
			return HomeCatalogRoot{}, false, nil
		}
		if row.principalKind != "user" {
			return HomeCatalogRoot{}, false, errors.New("principal Home has an invalid principal kind")
		}
		root := HomeCatalogRoot{Scope: "user", UserID: row.principalID}
		_, err := homeCatalogSkillRoot(root)
		return root, true, err
	case "agent":
		if !row.hasPrincipalKind || !row.hasPrincipalID || !row.hasAgentID {
			return HomeCatalogRoot{}, false, errors.New("agent Home identity is incomplete")
		}
		if row.principalKind == "group" {
			return HomeCatalogRoot{}, false, nil
		}
		if row.principalKind != "user" {
			return HomeCatalogRoot{}, false, errors.New("agent Home has an invalid principal kind")
		}
		root := HomeCatalogRoot{Scope: "user_agent", UserID: row.principalID, AgentID: row.agentID}
		_, err := homeCatalogSkillRoot(root)
		return root, true, err
	default:
		return HomeCatalogRoot{}, false, errors.New("unsupported Home kind")
	}
}

var _ HomeCatalogInventory = (*StorageHomeCatalogInventory)(nil)
