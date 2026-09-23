package handlers

import (
	"math"
	"testing"
)

func TestCheckedPlanDurationConversions(t *testing.T) {
	if got, ok := checkedHoursToSeconds(0.5); !ok || got != 1800 {
		t.Fatalf("half-hour conversion = (%d, %v), want (1800, true)", got, ok)
	}
	for _, hours := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1), math.MaxFloat64, 1e16, 1e-10} {
		if got, ok := checkedHoursToSeconds(hours); ok {
			t.Errorf("checkedHoursToSeconds(%v) = %d, accepted invalid input", hours, got)
		}
	}
}

func TestCheckedPlanDataConversions(t *testing.T) {
	if got, ok := checkedGigabytesToBytes(1); !ok || got != 1<<30 {
		t.Fatalf("one-GB conversion = (%d, %v), want (1073741824, true)", got, ok)
	}
	if got, ok := checkedGigabytesToBytes(0); !ok || got != 0 {
		t.Fatalf("zero-GB conversion = (%d, %v), want (0, true)", got, ok)
	}
	for _, gigabytes := range []float64{-1, 1e-12, math.NaN(), math.Inf(1), math.Inf(-1), math.MaxFloat64} {
		if got, ok := checkedGigabytesToBytes(gigabytes); ok {
			t.Errorf("checkedGigabytesToBytes(%v) = %d, accepted invalid input", gigabytes, got)
		}
	}
}
