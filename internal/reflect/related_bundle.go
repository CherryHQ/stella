package reflect

import (
	"context"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

const (
	defaultMaxRelatedKnowledgePerCandidate = 10
	defaultMaxRelatedSkillsPerCandidate    = 5
)

// factRelatedBundle is the host-side package that feeds fact reconciliation.
// Profile and soul are singleton inputs; only knowledge receives a catalog for
// later related discovery.
type factRelatedBundle struct {
	Profile   factSingletonBundle    `json:"profile"`
	Soul      soulSingletonBundle    `json:"soul"`
	Knowledge knowledgeRelatedBundle `json:"knowledge"`
}

type factSingletonBundle struct {
	Candidates []factCandidate `json:"candidates"`
	Current    *memory.Fact    `json:"current_singleton,omitempty"`
}

type soulSingletonBundle struct {
	Candidates        []factCandidate          `json:"candidates"`
	Current           *memory.Fact             `json:"current_singleton,omitempty"`
	ActiveConstraints []memory.ConstraintEntry `json:"active_constraints,omitempty"`
}

type knowledgeRelatedBundle struct {
	Candidates     []factCandidate             `json:"candidates"`
	Catalog        []factCatalogItem           `json:"catalog,omitempty"`
	RelatedRecords []memory.Fact               `json:"related_records,omitempty"`
	RelationHints  []knowledgeRelatedSelection `json:"relation_hints,omitempty"`
	Limits         relatedBundleLimits         `json:"limits"`
}

type relatedBundleLimits struct {
	MaxRelatedPerCandidate int `json:"max_related_per_candidate"`
}

type factCatalogItem struct {
	ID        string              `json:"id"`
	Subject   memory.FactSubject  `json:"subject"`
	Summary   string              `json:"summary"`
	Source    memory.ChangeSource `json:"source"`
	UpdatedAt time.Time           `json:"updated_at"`
	Record    memory.Fact         `json:"-"`
}

type skillCatalogItem struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	Description   string           `json:"description"`
	Scope         string           `json:"scope"`
	UpdatedAt     time.Time        `json:"updated_at"`
	ContentDigest string           `json:"content_digest,omitempty"`
	Version       int64            `json:"version"`
	Record        pkgplugins.Skill `json:"-"`
}

// reflectSkillCatalogStore is intentionally narrower than the plugin-facing
// skill store. Only Reflect-owned active user_agent skills may enter #531's
// automatic maintenance pool.
type reflectSkillCatalogStore interface {
	ListActiveReflectOwnedUserAgentSkills(ctx context.Context, userID string, agentID string) ([]pkgplugins.Skill, error)
}

type skillRelatedBundleStore interface {
	reflectSkillCatalogStore
	LoadFile(ctx context.Context, skillID string, path string) (string, error)
}

func buildFactRelatedBundle(ctx context.Context, facts memory.FactStore, constraints memory.ConstraintStore, userID string, agentID string, candidates []factCandidate) (factRelatedBundle, error) {
	profileCandidates, soulCandidates, knowledgeCandidates := splitFactCandidatesBySubject(candidates)

	profile, err := currentSingletonFact(ctx, facts, userID, agentID, memory.FactSubjectUser)
	if err != nil {
		return factRelatedBundle{}, err
	}
	soul, err := currentSingletonFact(ctx, facts, userID, agentID, memory.FactSubjectAgent)
	if err != nil {
		return factRelatedBundle{}, err
	}
	var activeConstraints []memory.ConstraintEntry
	if constraints != nil {
		activeConstraints, err = constraints.GetConstraints(ctx, userID, agentID)
		if err != nil {
			return factRelatedBundle{}, err
		}
	}
	catalog, err := buildKnowledgeRelatedCatalog(ctx, facts, userID, agentID)
	if err != nil {
		return factRelatedBundle{}, err
	}

	return factRelatedBundle{
		Profile: factSingletonBundle{
			Candidates: profileCandidates,
			Current:    profile,
		},
		Soul: soulSingletonBundle{
			Candidates:        soulCandidates,
			Current:           soul,
			ActiveConstraints: activeConstraints,
		},
		Knowledge: knowledgeRelatedBundle{
			Candidates: knowledgeCandidates,
			Catalog:    catalog,
			Limits: relatedBundleLimits{
				MaxRelatedPerCandidate: defaultMaxRelatedKnowledgePerCandidate,
			},
		},
	}, nil
}

func splitFactCandidatesBySubject(candidates []factCandidate) ([]factCandidate, []factCandidate, []factCandidate) {
	var profile []factCandidate
	var soul []factCandidate
	var knowledge []factCandidate
	for _, candidate := range candidates {
		switch candidate.Subject {
		case factSubjectUser:
			profile = append(profile, candidate)
		case factSubjectAgent:
			soul = append(soul, candidate)
		case factSubjectWorld:
			knowledge = append(knowledge, candidate)
		}
	}
	return profile, soul, knowledge
}

func currentSingletonFact(ctx context.Context, facts memory.FactStore, userID string, agentID string, subject memory.FactSubject) (*memory.Fact, error) {
	if facts == nil {
		return nil, nil
	}
	rows, err := facts.ListActiveFacts(ctx, userID, agentID, subject)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	current := rows[0]
	return &current, nil
}

func buildKnowledgeRelatedCatalog(ctx context.Context, facts memory.FactStore, userID string, agentID string) ([]factCatalogItem, error) {
	if facts == nil {
		return nil, nil
	}
	rows, err := facts.ListActiveFacts(ctx, userID, agentID, memory.FactSubjectWorld)
	if err != nil {
		return nil, err
	}
	catalog := make([]factCatalogItem, 0, len(rows))
	for _, fact := range rows {
		if fact.Source != memory.SourceReflect {
			continue
		}
		catalog = append(catalog, factCatalogItem{
			ID:        fact.ID,
			Subject:   fact.Subject,
			Summary:   fact.Content,
			Source:    fact.Source,
			UpdatedAt: fact.UpdatedAt,
			Record:    fact,
		})
	}
	return catalog, nil
}

func attachKnowledgeRelatedRecords(bundle knowledgeRelatedBundle, selections []knowledgeRelatedSelection) (knowledgeRelatedBundle, error) {
	if err := validateKnowledgeRelatedDiscovery(bundle.Candidates, bundle.Catalog, selections, bundle.Limits.MaxRelatedPerCandidate); err != nil {
		return knowledgeRelatedBundle{}, err
	}
	bundle.RelationHints = append([]knowledgeRelatedSelection(nil), selections...)
	byID := make(map[string]memory.Fact, len(bundle.Catalog))
	for _, item := range bundle.Catalog {
		byID[item.ID] = item.Record
	}
	seen := map[string]struct{}{}
	for _, selection := range selections {
		for _, hint := range selection.Related {
			if _, ok := seen[hint.FactID]; ok {
				continue
			}
			bundle.RelatedRecords = append(bundle.RelatedRecords, byID[hint.FactID])
			seen[hint.FactID] = struct{}{}
		}
	}
	return bundle, nil
}

func buildSkillRelatedCatalog(ctx context.Context, store reflectSkillCatalogStore, userID string, agentID string) ([]skillCatalogItem, error) {
	if store == nil {
		return nil, nil
	}
	rows, err := store.ListActiveReflectOwnedUserAgentSkills(ctx, userID, agentID)
	if err != nil {
		return nil, err
	}
	catalog := make([]skillCatalogItem, 0, len(rows))
	for _, skill := range rows {
		catalog = append(catalog, skillCatalogItem{
			ID:            skill.ID,
			Name:          skill.Name,
			Description:   skill.Description,
			Scope:         skill.Scope,
			UpdatedAt:     skill.UpdatedAt,
			ContentDigest: skill.ContentDigest,
			Version:       skill.Version,
			Record:        skill,
		})
	}
	return catalog, nil
}

func buildSkillRelatedBundle(ctx context.Context, store skillRelatedBundleStore, userID string, agentID string, candidates []skillCandidate, selections []skillRelatedSelection) (skillRelatedBundle, error) {
	catalog, err := buildSkillRelatedCatalog(ctx, store, userID, agentID)
	if err != nil {
		return skillRelatedBundle{}, err
	}
	limits := relatedBundleLimits{MaxRelatedPerCandidate: defaultMaxRelatedSkillsPerCandidate}
	if err := validateSkillRelatedDiscovery(candidates, catalog, selections, limits.MaxRelatedPerCandidate); err != nil {
		return skillRelatedBundle{}, err
	}

	byID := make(map[string]skillCatalogItem, len(catalog))
	for _, item := range catalog {
		byID[item.ID] = item
	}
	bundle := skillRelatedBundle{
		Candidates:    candidates,
		RelationHints: append([]skillRelatedSelection(nil), selections...),
		Limits:        limits,
	}
	seen := map[string]struct{}{}
	for _, selection := range selections {
		for _, hint := range selection.Related {
			if _, ok := seen[hint.SkillID]; ok {
				continue
			}
			item := byID[hint.SkillID]
			content, err := store.LoadFile(ctx, item.ID, pkgplugins.SkillMainFile)
			if err != nil {
				return skillRelatedBundle{}, err
			}
			bundle.RelatedRecords = append(bundle.RelatedRecords, skillRelatedRecord{
				Skill:           item.Record,
				ContentDigest:   item.ContentDigest,
				MainFileContent: content,
			})
			seen[hint.SkillID] = struct{}{}
		}
	}
	return bundle, nil
}
