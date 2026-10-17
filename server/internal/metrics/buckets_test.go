package metrics

import (
	"math"
	"math/rand"
	"sort"
	"testing"
)

// Keys computed by the SDK (MetricBuckets.Key on Unity's Mono) for these
// values: the server must read them back the same way.
var sdkKeys = []struct {
	v, back float64
	key     int
}{
	{0, 0, 0}, {1e-10, 0, 0}, {1, 0.98, 1000}, {-1, -0.98, -1000}, {1.5, 1.5217423685056961, 1011},
	{16.6, 16.779804500984415, 1071}, {100, 101.53627338308353, 1116}, {250.5, 254.81525795437179, 1139},
	{800, 812.9694303407191, 1168}, {1234.5, 1212.8725749504042, 1178}, {60000, 61161.283518479417, 1276},
	{-42, -42.11056866554879, -1094}, {0.001, 0.0010066570786046117, 828}, {1e300, 4245340591446530.5, 1900},
	{1e-9, 9.8049258066148073e-10, 482}, {-1e-9, -9.8049258066148073e-10, -482}, {4e15, 4078856646683921.5, 1899},
	{0.5, 0.49643962914476913, 983},
}

func TestKey_MatchesTheSDK(t *testing.T) {
	for _, c := range sdkKeys {
		if got := Key(c.v); got != c.key {
			t.Errorf("Key(%v) = %d, SDK %d", c.v, got, c.key)
		}
		if got := Value(c.key); math.Abs(got-c.back) > 1e-12*math.Abs(c.back) {
			t.Errorf("Value(%d) = %v, SDK %v", c.key, got, c.back)
		}
		if !ValidKey(c.key) {
			t.Errorf("%d not valid", c.key)
		}
	}
	for _, k := range []int{1, 399, -399, 1901, -1901, 30000} {
		if ValidKey(k) {
			t.Errorf("%d valid", k)
		}
	}
}

func TestValue_IsWithinTheRelativeAccuracy(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 100000; i++ {
		v := math.Pow(10, r.Float64()*21-9)
		if r.Intn(3) == 0 {
			v = -v
		}
		back := Value(Key(v))
		if math.Abs(back-v)/math.Abs(v) > RelativeAccuracy+1e-12 {
			t.Fatalf("%v read back as %v", v, back)
		}
	}
}

// p50/p95 of merged histograms (several devices and windows) are within 2%
// of the exact percentile of all samples.
func TestQuantiles_OfMergedHistogramsAreWithinTheBound(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	merged := map[int16]uint64{}
	var all []float64
	min, max := math.Inf(1), math.Inf(-1)
	for device := 0; device < 50; device++ {
		for i := 0; i < 200; i++ {
			v := math.Exp(math.Log(700) + 0.6*r.NormFloat64()) // room switch, ms
			all = append(all, v)
			merged[int16(Key(v))]++
			min, max = math.Min(min, v), math.Max(max, v)
		}
	}
	var keys []int16
	var counts []uint64
	for k, c := range merged {
		keys = append(keys, k)
		counts = append(counts, c)
	}
	sort.Float64s(all)
	qs := []float64{0.5, 0.9, 0.95, 0.99, 1}
	got := Quantiles(keys, counts, min, max, qs...)
	for i, q := range qs {
		exact := all[int(math.Ceil(q*float64(len(all))))-1]
		if math.Abs(got[i]-exact)/exact > RelativeAccuracy+1e-12 {
			t.Errorf("q%v = %v, exact %v", q, got[i], exact)
		}
	}
	if got[4] != max {
		t.Errorf("q1 = %v, want the exact max %v", got[4], max)
	}
}

func TestQuantiles_EdgeCases(t *testing.T) {
	if q := Quantiles(nil, nil, 0, 0, 0.5); q[0] != 0 {
		t.Errorf("empty: %v", q)
	}
	// One sample: its value exactly (clamped to min = max).
	if q := Quantiles([]int16{int16(Key(123.4))}, []uint64{1}, 123.4, 123.4, 0.5, 0.95); q[0] != 123.4 || q[1] != 123.4 {
		t.Errorf("single: %v", q)
	}
	// Negative and zero values sort below positive ones.
	keys := []int16{int16(Key(5)), 0, int16(Key(-5))}
	q := Quantiles(keys, []uint64{1, 1, 1}, -5, 5, 0.33, 0.5, 1)
	if math.Abs(q[0]+5) > 0.1 || q[1] != 0 || math.Abs(q[2]-5) > 0.1 {
		t.Errorf("signs: %v", q)
	}
	// Mismatched lengths: the shorter wins, nothing panics.
	Quantiles([]int16{1000, 1001}, []uint64{1}, 0, 10, 0.5)
}
