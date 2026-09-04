package embed

import "math"

// Quantize maps a unit-normalized vector to int8 with a single per-vector
// scale, so v[i] is recovered as float32(q[i]) * scale to within scale/2.
//
// Scoring stays MIXED: the query stays float32 and only the stored vector is
// quantized. Quantizing both would compound the error for no further memory
// saving, since the query vector is one transient allocation.
func Quantize(v []float32) ([]int8, float32) {
	var max float32
	for _, x := range v {
		if a := float32(math.Abs(float64(x))); a > max {
			max = a
		}
	}
	q := make([]int8, len(v))
	if max == 0 {
		return q, 0
	}
	scale := max / 127
	for i, x := range v {
		r := math.Round(float64(x / scale))
		if r > 127 {
			r = 127
		}
		if r < -128 {
			r = -128
		}
		q[i] = int8(r)
	}
	return q, scale
}

// scoreAgainst returns the cosine of query q against stored chunk i, reading
// whichever representation this store holds.
func (s *Store) scoreAgainst(q []float32, i int) float32 {
	if s.quant == quantF32 {
		return dot(q, s.vecAt(i))
	}
	row := s.vecI8[i*s.dim : (i+1)*s.dim]
	var sum float32
	for j, x := range row {
		sum += q[j] * float32(x)
	}
	return sum * s.scales[i]
}
