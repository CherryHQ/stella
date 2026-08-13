package embedding

import (
	"errors"
	"fmt"
	"math"
)

// StorageDim is the fixed width of the vector(1536) columns every embedding is
// stored in. The HNSW indexes require a single dimension, so all vectors — from
// any provider — are widened to this before persistence.
const StorageDim = 1536

// ToStorageVector adapts a provider's native vector to the fixed storage width.
// A vector shorter than StorageDim is zero-padded (padding with zeros leaves the
// L2 norm unchanged, so cosine distance is preserved); a longer one is rejected,
// since silently truncating would corrupt the space. Empty, zero-norm, and
// non-finite vectors have no valid cosine direction and are rejected before they
// can become storage sidecars. When normalize is true the result is L2-normalized,
// which lets cosine similarity be read as a dot product.
func ToStorageVector(v []float32, normalize bool) ([]float32, error) {
	if len(v) > StorageDim {
		return nil, fmt.Errorf("embedding: native dim %d exceeds storage dim %d", len(v), StorageDim)
	}
	normSquared, err := validateVector(v)
	if err != nil {
		return nil, err
	}
	out := make([]float32, StorageDim)
	copy(out, v)
	if normalize {
		l2Normalize(out, normSquared)
	}
	return out, nil
}

// ValidateStorageVector checks a vector that is about to be used directly by a
// vector(1536) query. QueryEmbedder is an interface, so this keeps the retrieval
// boundary safe even when an implementation does not call ToStorageVector.
func ValidateStorageVector(v []float32) error {
	if len(v) != StorageDim {
		return fmt.Errorf("embedding: storage dim %d, want %d", len(v), StorageDim)
	}
	_, err := validateVector(v)
	return err
}

// validateVector returns the squared L2 norm after enforcing the invariants
// required by cosine distance.
func validateVector(v []float32) (float64, error) {
	if len(v) == 0 {
		return 0, errors.New("embedding: vector is empty")
	}
	var sum float64
	for i, x := range v {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return 0, fmt.Errorf("embedding: vector component %d is not finite", i)
		}
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return 0, errors.New("embedding: vector has zero norm")
	}
	return sum, nil
}

// l2Normalize scales a validated vector in place to unit length.
func l2Normalize(v []float32, normSquared float64) {
	inv := float32(1 / math.Sqrt(normSquared))
	for i := range v {
		v[i] *= inv
	}
}
