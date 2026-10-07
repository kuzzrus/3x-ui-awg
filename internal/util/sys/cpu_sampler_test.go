package sys

import (
	"errors"
	"math"
	"testing"
)

func TestCPUSamplerSmoothsAfterTheBaselineSample(t *testing.T) {
	readings := []float64{90, 50, 10, 10}
	next := 0
	s := &CPUSampler{raw: func() (float64, error) {
		v := readings[next]
		next++
		return v, nil
	}}

	want := []float64{0, 50, 0.3*10 + 0.7*50, 0.3*10 + 0.7*(0.3*10+0.7*50)}
	for i, w := range want {
		got, err := s.Percent()
		if err != nil || math.Abs(got-w) > 1e-9 {
			t.Fatalf("sample %d = %v, %v; want %v", i, got, err, w)
		}
	}
}

func TestCPUSamplerPassesOnAnUnreadableCounter(t *testing.T) {
	broken := errors.New("no counter")
	s := &CPUSampler{raw: func() (float64, error) { return 0, broken }}
	if _, err := s.Percent(); !errors.Is(err, broken) {
		t.Fatalf("error = %v, want %v", err, broken)
	}
}
