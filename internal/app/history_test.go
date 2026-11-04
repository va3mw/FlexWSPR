package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/va3mw/FlexWSPR/internal/config"
)

func TestHistory(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(2 * time.Minute)
	t0 := now.Add(-10 * time.Minute)
	old := now.Add(-25 * time.Hour)
	spots := old.Format("060102 1504") + "  -20   0.1    7.040000 OLD1         FN03    23   0\n" +
		t0.Format("060102 1504") + "  -26   0.9   14.097053 WW0WWV       DN70    30   0\n" +
		t0.Format("060102 1504") + "  -22   0.3   14.097106 G4HSB        IO94    23   0\n" +
		t0.Format("060102 1504") + "  -14   0.0   14.097035 <...>        JN61TP  23   0\n" +
		"garbage line\n"
	cycles := cycleLine(CycleInfo{Start: t0, Band: "20m", DialHz: 14095600, Spots: 3}) + "\n" +
		cycleLine(CycleInfo{Start: t0.Add(2 * time.Minute), Band: "40m", DialHz: 7038600}) + "\n"
	os.WriteFile(filepath.Join(dir, "spots.txt"), []byte(spots), 0o644)
	os.WriteFile(filepath.Join(dir, CyclesFile), []byte(cycles), 0o644)

	var h history
	h.load(filepath.Join(dir, "spots.txt"), filepath.Join(dir, CyclesFile), "FN03", now)
	if len(h.spots) != 3 || len(h.cycles) != 2 {
		t.Fatalf("loaded %d spots, %d cycles", len(h.spots), len(h.cycles))
	}
	s := h.summary(now)
	if len(s.Hours) != 24 || len(s.Bands) != 2 || s.Bands[0].Band != "40m" || s.Bands[1].Band != "20m" {
		t.Fatalf("bands %+v", s.Bands)
	}
	b := s.Bands[1]
	if b.Cycles != 1 || b.Spots != 3 || b.Calls != 2 || b.LastHour != 3 || b.Farthest == nil || b.Farthest.Call != "G4HSB" {
		t.Fatalf("20m %+v", b)
	}
	if s.Bands[0].Cycles != 1 || s.Bands[0].Spots != 0 {
		t.Fatalf("40m %+v", s.Bands[0])
	}
	cell := b.Hours[int(t0.Sub(s.Hours[0])/time.Hour)]
	if cell.Spots != 3 || cell.Cycles != 1 || cell.MaxKm != b.Farthest.Km {
		t.Fatalf("cell %+v", cell)
	}
	if len(s.Stations) != 2 || s.Calls != 2 {
		t.Fatalf("stations %+v", s.Stations)
	}
}

func TestHistoryReloadsOnGridChange(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Now().UTC().Truncate(2 * time.Minute).Add(-10 * time.Minute)
	os.WriteFile(filepath.Join(dir, "spots.txt"),
		[]byte(t0.Format("060102 1504")+"  -22   0.3   14.097106 G4HSB        IO94    23   0\n"), 0o644)
	cfg := &config.Config{Grid: "FN03", DataDir: dir, SpotsFile: filepath.Join(dir, "spots.txt")}
	var m monitor
	m.loadHistory(cfg)
	km1 := m.summary().Stations[0].Km
	cfg.Grid = "EM10"
	m.loadHistory(cfg)
	if km2 := m.summary().Stations[0].Km; km2 == km1 || km2 == 0 {
		t.Fatalf("distance not redone for the new grid: %d then %d", km1, km2)
	}
}
