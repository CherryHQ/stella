package searchrank

import "testing"

func TestRankWeightsFieldsAndReturnsMatchesOnly(t *testing.T) {
	docs := []Document{
		{
			ID: "deploy",
			Fields: []Field{
				{Name: "name", Text: "deploy", Weight: 1},
				{Name: "description", Text: "Reusable rollout and release checklist", Weight: 3},
			},
		},
		{
			ID: "notes",
			Fields: []Field{
				{Name: "name", Text: "notes", Weight: 1},
				{Name: "description", Text: "Meeting notes archive", Weight: 3},
			},
		},
		{
			ID: "debug",
			Fields: []Field{
				{Name: "name", Text: "debug", Weight: 1},
				{Name: "description", Text: "Investigate runtime failures", Weight: 3},
			},
		},
	}

	results := Rank("release checklist", docs, 10)

	if len(results) != 1 {
		t.Fatalf("results = %#v, want one match", results)
	}
	if results[0].ID != "deploy" {
		t.Fatalf("top result = %q, want deploy", results[0].ID)
	}
	if results[0].Score <= 0 {
		t.Fatalf("score = %f, want positive", results[0].Score)
	}
	if got := results[0].MatchedFields; len(got) != 1 || got[0] != "description" {
		t.Fatalf("matched fields = %#v, want description", got)
	}
	if results[0].Snippet == "" {
		t.Fatal("expected non-empty snippet")
	}
}

func TestRankUsesFieldWeightForOrdering(t *testing.T) {
	docs := []Document{
		{
			ID: "weak-name",
			Fields: []Field{
				{Name: "name", Text: "migration", Weight: 1},
				{Name: "description", Text: "General project notes", Weight: 3},
			},
		},
		{
			ID: "strong-description",
			Fields: []Field{
				{Name: "name", Text: "db", Weight: 1},
				{Name: "description", Text: "Database migration migration migration playbook", Weight: 3},
			},
		},
	}

	results := Rank("migration", docs, 10)

	if len(results) != 2 {
		t.Fatalf("results = %#v, want two matches", results)
	}
	if results[0].ID != "strong-description" {
		t.Fatalf("top result = %q, want strong-description", results[0].ID)
	}
}

func TestRankAppliesLimitAndStableTieBreak(t *testing.T) {
	docs := []Document{
		{ID: "b", Fields: []Field{{Name: "content", Text: "same term", Weight: 1}}},
		{ID: "a", Fields: []Field{{Name: "content", Text: "same term", Weight: 1}}},
		{ID: "c", Fields: []Field{{Name: "content", Text: "same term", Weight: 1}}},
	}

	results := Rank("term", docs, 2)

	if len(results) != 2 {
		t.Fatalf("results = %#v, want two limited matches", results)
	}
	if results[0].ID != "a" || results[1].ID != "b" {
		t.Fatalf("stable order = %#v, want a then b", results)
	}
}

func TestRankReturnsNilForBlankQuery(t *testing.T) {
	results := Rank("   ", []Document{
		{ID: "x", Fields: []Field{{Name: "content", Text: "anything", Weight: 1}}},
	}, 10)

	if results != nil {
		t.Fatalf("results = %#v, want nil", results)
	}
}
