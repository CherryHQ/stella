package embedding

import (
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
// since silently truncating would corrupt the space. When normalize is true the
// result is L2-normalized, which lets cosine similarity be read as a dot product.
func ToStorageVector(v []float32, normalize bool) ([]float32, error) {
	if len(v) > StorageDim {
		return nil, fmt.Errorf("embedding: native dim %d exceeds storage dim %d", len(v), StorageDim)
	}
	out := make([]float32, StorageDim)
	copy(out, v)
	if normalize {
		l2Normalize(out)
	}
	return out, nil
}

// l2Normalize scales v in place to unit length. A zero vector is left untouched
// (there is no meaningful direction to normalize to).
func l2Normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
}
