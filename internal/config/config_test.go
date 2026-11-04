package config

import (
	"path/filepath"
	"testing"
	"time"
)

func at(hhmm string) time.Time {
	t, _ := time.Parse("2006-01-02 15:04", "2026-11-03 "+hhmm)
	return t
}

func TestScheduleSlots(t *testing.T) {
	c := Default()
	c.Band = "30m"
	c.Schedule = Schedule{Enabled: true, Slots: []Slot{
		{Start: "22:00", End: "10:00", Bands: []string{"40m", "80m"}, Cycles: 2}, // passes midnight
		{Start: "12:00", End: "18:00", Bands: []string{"20m"}},
	}}
	if err := c.Resolve(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		t    string
		want string
		slot int
	}{
		{"23:00", "80m", 1}, // minute 1380 = cycle 690; 690/2 = 345, odd: 80m
		{"00:00", "40m", 1},
		{"00:02", "40m", 1},
		{"00:04", "80m", 1},
		{"00:06", "80m", 1},
		{"00:08", "40m", 1},
		{"11:00", "30m", 0}, // no slot: default band
		{"12:00", "20m", 2},
		{"17:58", "20m", 2},
		{"18:00", "30m", 0},
	}
	for _, k := range cases {
		hz, slot := c.BandAt(at(k.t))
		if BandLabel(hz) != k.want || slot != k.slot {
			t.Errorf("%s: got %s slot %d, want %s slot %d", k.t, BandLabel(hz), slot, k.want, k.slot)
		}
	}
	if p := c.Plan(at("00:01"), 3); p[0].Band != "40m" || p[1].Band != "80m" || p[2].Band != "80m" {
		t.Errorf("plan: %+v", p)
	}
}

func TestScheduleAllDayAndOff(t *testing.T) {
	c := Default()
	c.Schedule = Schedule{Enabled: true, Slots: []Slot{{Start: "00:00", End: "00:00", Bands: []string{"40m", "30m", "20m"}}}}
	if err := c.Resolve(); err != nil {
		t.Fatal(err)
	}
	got := ""
	for _, p := range c.Plan(at("13:59"), 4) {
		got += p.Band + " "
	}
	// 14:00 is cycle 420 -> 420%3=0 -> 40m
	if got != "40m 30m 20m 40m " {
		t.Errorf("rotation: %s", got)
	}
	c.Schedule.Mode = ModeOff
	if hz, _ := c.BandAt(at("14:00")); hz != Bands["20m"] {
		t.Errorf("schedule off should use band: %d", hz)
	}
}

func TestScheduleCheck(t *testing.T) {
	c := Default()
	c.Schedule = Schedule{Enabled: true, Slots: []Slot{{Start: "25:00", End: "01:00", Bands: []string{"20m"}}}}
	if c.Resolve() == nil {
		t.Error("bad time accepted")
	}
	c.Schedule.Slots = []Slot{{Start: "01:00", End: "02:00"}}
	if c.Resolve() == nil {
		t.Error("slot without bands accepted")
	}
	c.Schedule.Slots = []Slot{{Start: "01:00", End: "02:00", Bands: []string{"99m"}}}
	if c.Resolve() == nil {
		t.Error("bad band accepted")
	}
}

func TestGridAndSave(t *testing.T) {
	c := Default()
	c.Callsign, c.Grid = " va3mw ", "fn03AB"
	c.Schedule = Schedule{Enabled: true, Slots: []Slot{{Start: "00:00", End: "12:00", Bands: []string{"40m"}}}}
	if err := c.Resolve(); err != nil {
		t.Fatal(err)
	}
	if c.Callsign != "VA3MW" || c.Grid != "FN03ab" {
		t.Errorf("tidy: %q %q", c.Callsign, c.Grid)
	}
	p := filepath.Join(t.TempDir(), "sub", "flexwspr.yaml")
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	d, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if d.Callsign != "VA3MW" || len(d.Schedule.Slots) != 1 || d.Schedule.Slots[0].Cycles != 1 || d.DialHz != Bands["20m"] {
		t.Errorf("round trip: %+v", d)
	}
	c.Grid = "ZZ99"
	if c.Resolve() == nil {
		t.Error("bad grid accepted")
	}
}

func TestAntennaPerBand(t *testing.T) {
	c := Default()
	c.RxAnt = "ant1"
	c.Antennas = map[string]string{"40M": "rx_a", "20m": " ", "160m": "RX_B"}
	if err := c.Resolve(); err != nil {
		t.Fatal(err)
	}
	if c.AntennaFor(Bands["40m"]) != "RX_A" || c.AntennaFor(Bands["160m"]) != "RX_B" {
		t.Errorf("per band: %+v", c.Antennas)
	}
	if c.AntennaFor(Bands["20m"]) != "ANT1" || c.AntennaFor(14080000) != "ANT1" {
		t.Error("default antenna not used")
	}
	if _, ok := c.Antennas["20m"]; ok {
		t.Error("blank entry kept")
	}
	c.Antennas = map[string]string{"99m": "ANT1"}
	if c.Resolve() == nil {
		t.Error("bad band accepted")
	}
}

func TestRotation(t *testing.T) {
	c := Default()
	c.Schedule = Schedule{Mode: ModeRotation, Rotation: Rotation{
		Bands: []string{"80m", "60m", "40m", "30m", "20m", "17m", "15m", "12m", "10m", "6m"}, Cycles: 3}}
	if err := c.Resolve(); err != nil {
		t.Fatal(err)
	}
	// 10 bands x 3 cycles = 60 min: each band 6 minutes, starting each hour.
	got := ""
	for _, p := range c.Plan(at("13:59"), 12) {
		got += p.Band + " "
	}
	if got != "80m 80m 80m 60m 60m 60m 40m 40m 40m 30m 30m 30m " {
		t.Errorf("rotation: %s", got)
	}
	// Smooth over midnight: 23:54 is the last 6m block, 00:00 starts 80m.
	if hz, _ := c.BandAt(at("23:58")); BandLabel(hz) != "6m" {
		t.Errorf("23:58: %s", BandLabel(hz))
	}
	if hz, _ := c.BandAt(at("00:00").Add(24 * time.Hour)); BandLabel(hz) != "80m" {
		t.Errorf("00:00: %s", BandLabel(hz))
	}
	c.Schedule.Rotation.Bands = nil
	if c.Resolve() == nil {
		t.Error("empty rotation accepted")
	}
}

func TestOldEnabledFlag(t *testing.T) {
	c := Default()
	c.Schedule = Schedule{Enabled: true, Slots: []Slot{{Start: "00:00", End: "00:00", Bands: []string{"40m"}}}}
	if err := c.Resolve(); err != nil || c.Schedule.Mode != ModeSlots {
		t.Errorf("old file: mode %q err %v", c.Schedule.Mode, err)
	}
	d := Default()
	d.Resolve()
	if d.Schedule.Mode != ModeOff {
		t.Errorf("default mode %q", d.Schedule.Mode)
	}
}

func TestExampleFileLoads(t *testing.T) {
	c, err := Load("../../flexwspr.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Schedule.Mode != ModeOff || len(c.Schedule.Rotation.Bands) != 10 || c.Schedule.Rotation.Cycles != 3 {
		t.Errorf("example schedule: %+v", c.Schedule)
	}
}
