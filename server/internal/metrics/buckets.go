// Package metrics decodes the histograms of custom metrics (APM-6) and
// computes percentiles from them.
//
// The SDK (sdk/Runtime/Utils/MetricHistogram.cs, MetricBuckets) aggregates
// the samples of a metric per game context and minute into counts per bucket
// key. Version 1 of the scheme: logarithmic buckets of relative width
// gamma = (1 + a) / (1 - a), a = RelativeAccuracy = 2%. Bucket i holds the
// magnitudes in (gamma^(i-1), gamma^i] and reads back as
// 2 gamma^i / (gamma + 1), within a of every value in it. Key 0 holds the
// magnitudes below 1e-9 (read as 0); other keys are sign(v) * (i + Offset)
// with i in [MinIndex, MaxIndex], so keys sort like the values.
//
// Counts add up exactly across rows (ClickHouse sumMap), so a percentile
// computed from the merged buckets of any number of rows has the exact rank
// (nearest rank) and a value within 2% of the true sample at that rank, for
// magnitudes in [1e-9, 4e15]. It is clamped to the exact min and max.
package metrics

import (
	"math"
	"sort"
)

const (
	Version          = 1
	RelativeAccuracy = 0.02
	Offset           = 1000
	MinIndex         = -600
	MaxIndex         = 900
	minMagnitude     = 1e-9
)

// Gamma is the ratio between the bounds of a bucket.
var Gamma = (1 + RelativeAccuracy) / (1 - RelativeAccuracy)

var lnGamma = math.Log(Gamma)

// ValidKey reports whether k is a key of version 1.
func ValidKey(k int) bool {
	if k == 0 {
		return true
	}
	if k < 0 {
		k = -k
	}
	return k >= Offset+MinIndex && k <= Offset+MaxIndex
}

// Key is the key of a finite value (as the SDK computes it; for tests).
func Key(v float64) int {
	a := math.Abs(v)
	if !(a >= minMagnitude) {
		return 0
	}
	l := math.Ceil(math.Log(a) / lnGamma)
	i := int(math.Max(MinIndex, math.Min(MaxIndex, l)))
	if v > 0 {
		return i + Offset
	}
	return -(i + Offset)
}

// Value is what a key reads back as.
func Value(k int) float64 {
	if k == 0 {
		return 0
	}
	i := k
	if i < 0 {
		i = -i
	}
	v := 2 * math.Pow(Gamma, float64(i-Offset)) / (Gamma + 1)
	if k < 0 {
		return -v
	}
	return v
}

// Quantiles returns the value at each quantile q (0 < q <= 1, nearest rank)
// of the histogram keys/counts, clamped to [min, max]. keys need not be
// sorted. All zeros when the histogram is empty.
func Quantiles(keys []int16, counts []uint64, min, max float64, qs ...float64) []float64 {
	out := make([]float64, len(qs))
	n := len(keys)
	if len(counts) < n {
		n = len(counts)
	}
	idx := make([]int, 0, n)
	var total uint64
	for i := 0; i < n; i++ {
		if counts[i] > 0 {
			idx = append(idx, i)
			total += counts[i]
		}
	}
	if total == 0 {
		return out
	}
	sort.Slice(idx, func(a, b int) bool { return keys[idx[a]] < keys[idx[b]] })
	for j, q := range qs {
		rank := uint64(math.Ceil(q * float64(total)))
		if rank < 1 {
			rank = 1
		}
		if rank > total {
			rank = total
		}
		var seen uint64
		v := Value(int(keys[idx[len(idx)-1]]))
		for _, i := range idx {
			seen += counts[i]
			if seen >= rank {
				v = Value(int(keys[i]))
				break
			}
		}
		if min <= max {
			v = math.Max(min, math.Min(max, v))
		}
		out[j] = v
	}
	return out
}
