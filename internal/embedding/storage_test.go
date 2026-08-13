package embedding

import (
	"errors"
	"math"
	"testing"

	openai "github.com/openai/openai-go"
)

func TestAPIConfig_SpaceKeyFoldsDimension(t *testing.T) {
	// A 0 dim (model's native width) uses the bare model id.
	if got := (APIConfig{Model: "text-embedding-3-small"}).SpaceKey(); got != "text-embedding-3-small" {
		t.Errorf("native-dim key = %q, want bare model id", got)
	}
	// Pinning a dimension folds it in, so the same model at two dimensions names
	// two distinct spaces — a dim change can never alias the old corpus.
	a := (APIConfig{Model: "text-embedding-3-small", Dim: 1536}).SpaceKey()
	b := (APIConfig{Model: "text-embedding-3-small", Dim: 512}).SpaceKey()
	if a == b {
		t.Fatalf("different dims must yield different space keys, both = %q", a)
	}
	if a != "text-embedding-3-small@1536" {
		t.Errorf("key = %q, want model@dim form", a)
	}
}

func TestToStorageVector_ZeroPads(t *testing.T) {
	out, err := ToStorageVector([]float32{1, 2, 3}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != StorageDim {
		t.Fatalf("len = %d, want %d", len(out), StorageDim)
	}
	if out[0] != 1 || out[1] != 2 || out[2] != 3 {
		t.Fatalf("prefix not preserved: %v", out[:3])
	}
	for i := 3; i < StorageDim; i++ {
		if out[i] != 0 {
			t.Fatalf("expected zero pad at %d, got %f", i, out[i])
		}
	}
}

func TestToStorageVector_RejectsOversize(t *testing.T) {
	_, err := ToStorageVector(make([]float32, StorageDim+1), false)
	if err == nil {
		t.Fatal("expected error for oversize native dim")
	}
}

func TestToStorageVector_Normalizes(t *testing.T) {
	// Norm is computed over the padded vector, but zeros do not change it, so a
	// {3,4} input (norm 5) normalizes to {0.6,0.8} regardless of padding.
	out, err := ToStorageVector([]float32{3, 4}, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var sum float64
	for _, x := range out {
		sum += float64(x) * float64(x)
	}
	if math.Abs(sum-1) > 1e-6 {
		t.Fatalf("L2 norm^2 = %f, want 1", sum)
	}
	if math.Abs(float64(out[0])-0.6) > 1e-6 || math.Abs(float64(out[1])-0.8) > 1e-6 {
		t.Fatalf("normalized prefix = %v, want [0.6 0.8]", out[:2])
	}
}

func TestToStorageVector_RejectsInvalidCosineVectors(t *testing.T) {
	tests := []struct {
		name string
		vec  []float32
	}{
		{name: "empty", vec: nil},
		{name: "zero norm", vec: []float32{0, 0, 0}},
		{name: "NaN", vec: []float32{1, float32(math.NaN())}},
		{name: "positive infinity", vec: []float32{1, float32(math.Inf(1))}},
		{name: "negative infinity", vec: []float32{1, float32(math.Inf(-1))}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ToStorageVector(tt.vec, true); err == nil {
				t.Fatalf("ToStorageVector(%v) succeeded, want invalid cosine vector error", tt.vec)
			}
		})
	}
}

func TestValidateStorageVector(t *testing.T) {
	valid := make([]float32, StorageDim)
	valid[0] = 1
	if err := ValidateStorageVector(valid); err != nil {
		t.Fatalf("valid storage vector rejected: %v", err)
	}
	for name, vector := range map[string][]float32{
		"wrong dimension": {1},
		"zero norm":       make([]float32, StorageDim),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateStorageVector(vector); err == nil {
				t.Fatal("invalid storage vector accepted")
			}
		})
	}
}

func TestClassifyErr(t *testing.T) {
	cases := []struct {
		status   int
		terminal bool
	}{
		{400, true},
		{401, true},
		{403, true},
		{404, true},
		{422, true},
		{408, false},
		{429, false},
		{500, false},
		{502, false},
		{503, false},
	}
	for _, c := range cases {
		got := classifyErr(&openai.Error{StatusCode: c.status})
		if IsTerminal(got) != c.terminal {
			t.Errorf("status %d: IsTerminal=%v, want %v", c.status, IsTerminal(got), c.terminal)
		}
	}
	if classifyErr(nil) != nil {
		t.Error("nil error must classify to nil")
	}
	if IsTerminal(classifyErr(errors.New("connection refused"))) {
		t.Error("transport error must be transient, not terminal")
	}
}
