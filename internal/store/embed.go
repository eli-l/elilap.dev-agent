package store

import (
	"encoding/binary"
	"fmt"
	"math"
)

// encodeVector packs an embedding into the little-endian float32 bytes the
// chunk_vectors.vec column holds.
//
// Little-endian is the on-disk format on every platform this agent runs on,
// and it keeps the blob byte-for-byte comparable with what a reader expects
// from the same corpus on another machine.
func encodeVector(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(f))
	}
	return buf
}

// decodeVector unpacks a stored vector. dim is the dimension recorded next to
// the blob; it must match the blob's length, or the row is corrupt.
func decodeVector(raw []byte, dim int) ([]float32, error) {
	if dim <= 0 {
		return nil, fmt.Errorf("the stored vector has dimension %d: it is not a vector", dim)
	}
	if len(raw) != 4*dim {
		return nil, fmt.Errorf("the stored vector is %d bytes but its dimension is %d: the row is corrupt", len(raw), dim)
	}
	out := make([]float32, dim)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
	}
	return out, nil
}

// cosineSimilarity returns the cosine similarity of a and b, in [-1, 1].
// Vectors of different lengths, and the zero vector, score 0: there is no
// direction to compare, so the pair carries no information.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		x := float64(a[i])
		y := float64(b[i])
		dot += x * y
		normA += x * x
		normB += y * y
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
