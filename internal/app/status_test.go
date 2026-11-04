package app

import (
	"math"
	"testing"
	"time"

	"github.com/va3mw/FlexWSPR/internal/wspr"
)

func TestScore(t *testing.T) {
	cases := []struct {
		snr, dbm, km int
		want         float64
	}{
		{-20, 30, 1000, -20},  // 1 W at 1000 km: the SNR itself
		{-20, 37, 1000, -27},  // 5 W: 7 dB less credit
		{-20, 30, 2000, -14},  // twice as far: +6 dB
		{-24, 23, 7000, -0.1}, // 200 mW across 7000 km
		{-10, 37, 800, -18.9}, // loud local 5 W
		{-5, 30, 10, -25},     // under 100 km counts as 100 km
	}
	for _, c := range cases {
		if got := Score(c.snr, c.dbm, c.km); math.Abs(got-c.want) > 0.06 {
			t.Errorf("Score(%d, %d, %d) = %.2f, want %.1f", c.snr, c.dbm, c.km, got, c.want)
		}
	}
}

func TestBestSpot(t *testing.T) {
	var m monitor
	at := time.Date(2026, 11, 4, 1, 18, 0, 0, time.UTC)
	m.cycle(CycleInfo{Start: at, Band: "30m"}, []wspr.Spot{
		{Start: at, Call: "W1AW", Grid: "FN31", SNR: -5, DBm: 37},    // close and loud
		{Start: at, Call: "TI4JWC", Grid: "EK70", SNR: -17, DBm: 30}, // 3700 km at 1 W
		{Start: at, Call: "", Grid: "EM10CR", SNR: -8, DBm: 37},      // hashed call: not scored
	}, "FN03")
	m.cycle(CycleInfo{Start: at.Add(2 * time.Minute), Band: "40m"}, []wspr.Spot{
		{Start: at, Call: "N3AIU", Grid: "DM45", SNR: -11, DBm: 37},
	}, "FN03")
	s := m.snapshot()
	if s.Best == nil || s.Best.Call != "TI4JWC" {
		t.Fatalf("best: %+v", s.Best)
	}
	if b := s.BestByBand["40m"]; b.Call != "N3AIU" || !b.HasScore {
		t.Errorf("40m best: %+v", b)
	}
	for _, sp := range s.Spots {
		if sp.Call == "" && sp.HasScore {
			t.Error("hashed call scored")
		}
	}
}
