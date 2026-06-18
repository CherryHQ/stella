package tasks

import (
	"errors"
	"testing"
)

func TestParsePlanContent(t *testing.T) {
	c, err := parsePlanContent(`{"items":[{"id":"a","title":"A"}]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(c.Items) != 1 || c.Items[0].ID != "a" {
		t.Fatalf("unexpected content: %+v", c)
	}

	if _, err := parsePlanContent(""); err != nil {
		t.Fatalf("empty string should parse to empty content, got %v", err)
	}

	if _, err := parsePlanContent("{not json"); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("malformed json should be ErrInvalidPlan, got %v", err)
	}
}

func TestValidatePlan_Valid(t *testing.T) {
	cases := map[string]PlanContent{
		"direct empty role": {Items: []PlanItem{
			{ID: "only", Title: "Do the thing"},
		}},
		"direct explicit role": {Items: []PlanItem{
			{ID: "only", Title: "Do the thing", Role: PlanRoleDirect},
		}},
		"structured design-impl-verify": {Items: []PlanItem{
			{ID: "d", Title: "Design", Role: PlanRoleDesign},
			{ID: "i", Title: "Impl", Role: PlanRoleImpl, Deps: []string{"d"}},
			{ID: "v", Title: "Verify", Role: PlanRoleVerify, Deps: []string{"i"}},
		}},
		"structured transitive verify": {Items: []PlanItem{
			{ID: "i", Title: "Impl", Role: PlanRoleImpl},
			{ID: "mid", Title: "Mid", Role: PlanRoleDesign, Deps: []string{"i"}},
			{ID: "v", Title: "Verify", Role: PlanRoleVerify, Deps: []string{"mid"}},
		}},
		"two impls each verified": {Items: []PlanItem{
			{ID: "i1", Title: "Impl 1", Role: PlanRoleImpl},
			{ID: "i2", Title: "Impl 2", Role: PlanRoleImpl},
			{ID: "v", Title: "Verify both", Role: PlanRoleVerify, Deps: []string{"i1", "i2"}},
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validatePlan(c); err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
		})
	}
}

func TestValidatePlan_Invalid(t *testing.T) {
	cases := map[string]PlanContent{
		"empty": {Items: nil},
		"empty id": {Items: []PlanItem{
			{ID: "", Title: "no id"},
		}},
		"empty title": {Items: []PlanItem{
			{ID: "a", Title: ""},
		}},
		"duplicate id": {Items: []PlanItem{
			{ID: "a", Title: "A", Role: PlanRoleImpl},
			{ID: "a", Title: "A2", Role: PlanRoleVerify, Deps: []string{"a"}},
		}},
		"self dep": {Items: []PlanItem{
			{ID: "a", Title: "A", Role: PlanRoleImpl, Deps: []string{"a"}},
			{ID: "v", Title: "V", Role: PlanRoleVerify, Deps: []string{"a"}},
		}},
		"dangling dep": {Items: []PlanItem{
			{ID: "a", Title: "A", Role: PlanRoleImpl, Deps: []string{"ghost"}},
			{ID: "v", Title: "V", Role: PlanRoleVerify, Deps: []string{"a"}},
		}},
		"cycle": {Items: []PlanItem{
			{ID: "a", Title: "A", Role: PlanRoleImpl, Deps: []string{"b"}},
			{ID: "b", Title: "B", Role: PlanRoleImpl, Deps: []string{"a"}},
			{ID: "v", Title: "V", Role: PlanRoleVerify, Deps: []string{"a", "b"}},
		}},
		"single item bad role": {Items: []PlanItem{
			{ID: "a", Title: "A", Role: PlanRoleImpl},
		}},
		"single item with deps": {Items: []PlanItem{
			{ID: "a", Title: "A", Deps: []string{"a"}},
		}},
		"structured missing role": {Items: []PlanItem{
			{ID: "a", Title: "A", Role: PlanRoleImpl},
			{ID: "b", Title: "B"},
		}},
		"structured no impl": {Items: []PlanItem{
			{ID: "d", Title: "Design", Role: PlanRoleDesign},
			{ID: "v", Title: "Verify", Role: PlanRoleVerify, Deps: []string{"d"}},
		}},
		"impl without downstream verify": {Items: []PlanItem{
			{ID: "i1", Title: "Impl 1", Role: PlanRoleImpl},
			{ID: "i2", Title: "Impl 2", Role: PlanRoleImpl},
			{ID: "v", Title: "Verify", Role: PlanRoleVerify, Deps: []string{"i1"}},
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validatePlan(c); !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("expected ErrInvalidPlan, got %v", err)
			}
		})
	}
}
