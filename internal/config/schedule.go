package config

import (
	"fmt"
	"strings"
	"time"
)

// Schedule modes.
const (
	ModeOff      = "off"      // stay on Config.Band
	ModeRotation = "rotation" // rotate through a band list, all day
	ModeSlots    = "slots"    // UTC time slots, each with its own rotation
)

// Schedule picks the band for each two-minute cycle.
//
// Rotation (the usual choice) steps through Rotation.Bands, staying
// Rotation.Cycles cycles on each, around the clock. Slots gives each range of
// UTC time its own band list; outside every slot the station stays on
// Config.Band. Rotations are counted in two-minute cycles since 1970, so they
// carry on smoothly over midnight and restarts, and a 10-band rotation of one
// cycle each starts on its first band at the top of every 20 minutes.
type Schedule struct {
	Mode     string   `yaml:"mode" json:"mode"`
	Rotation Rotation `yaml:"rotation" json:"rotation"`
	Slots    []Slot   `yaml:"slots" json:"slots"`
	// Enabled is the pre-0.4 switch for slots; read for old files only.
	Enabled bool `yaml:"enabled,omitempty" json:"-"`
}

// Rotation is a band list stepped through all day.
type Rotation struct {
	Bands  []string `yaml:"bands" json:"bands"`   // band names or MHz, in order
	Cycles int      `yaml:"cycles" json:"cycles"` // cycles on each band (default 1)
}

// Slot is one row of a time-slot schedule.
type Slot struct {
	Start  string   `yaml:"start" json:"start"`   // "HH:MM" UTC
	End    string   `yaml:"end" json:"end"`       // "HH:MM" UTC, may pass midnight; equal to Start = all day
	Bands  []string `yaml:"bands" json:"bands"`   // band names or MHz, rotated in order
	Cycles int      `yaml:"cycles" json:"cycles"` // cycles on each band before the next (default 1)
}

// Check fills in defaults and validates the schedule in use.
func (s *Schedule) Check() error {
	if s.Mode == "" {
		s.Mode = ModeOff
		if s.Enabled {
			s.Mode = ModeSlots
		}
	}
	s.Enabled = false
	if s.Rotation.Cycles < 1 {
		s.Rotation.Cycles = 1
	}
	for i := range s.Slots {
		if s.Slots[i].Cycles < 1 {
			s.Slots[i].Cycles = 1
		}
	}
	switch s.Mode {
	case ModeOff:
		return nil
	case ModeRotation:
		if len(s.Rotation.Bands) == 0 {
			return fmt.Errorf("the band rotation has no bands")
		}
		for _, b := range s.Rotation.Bands {
			if _, err := ParseBand(b); err != nil {
				return fmt.Errorf("rotation: %w", err)
			}
		}
		return nil
	case ModeSlots:
	default:
		return fmt.Errorf("schedule mode %q: use off, rotation or slots", s.Mode)
	}
	for i := range s.Slots {
		sl := &s.Slots[i]
		if _, err := hhmm(sl.Start); err != nil {
			return fmt.Errorf("schedule slot %d: start: %w", i+1, err)
		}
		if _, err := hhmm(sl.End); err != nil {
			return fmt.Errorf("schedule slot %d: end: %w", i+1, err)
		}
		if len(sl.Bands) == 0 {
			return fmt.Errorf("schedule slot %d (%s-%s) has no bands", i+1, sl.Start, sl.End)
		}
		for _, b := range sl.Bands {
			if _, err := ParseBand(b); err != nil {
				return fmt.Errorf("schedule slot %d: %w", i+1, err)
			}
		}
	}
	return nil
}

// minutes of the day for "HH:MM".
func hhmm(s string) (int, error) {
	var h, m int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d:%d", &h, &m); err != nil || h < 0 || h > 24 || m < 0 || m > 59 || (h == 24 && m != 0) {
		return 0, fmt.Errorf("%q is not a time like 06:30", s)
	}
	return h*60 + m, nil
}

// contains reports whether minute-of-day m falls in [start, end).
func (sl Slot) contains(m int) bool {
	a, _ := hhmm(sl.Start)
	b, _ := hhmm(sl.End)
	a, b = a%1440, b%1440
	switch {
	case a == b:
		return true // all day
	case a < b:
		return m >= a && m < b
	default:
		return m >= a || m < b // passes midnight
	}
}

// pick returns the band for cycle number n of a rotation.
func pick(bands []string, cycles int, n int64) (int64, bool) {
	if len(bands) == 0 {
		return 0, false
	}
	hz, err := ParseBand(bands[(n/int64(max(cycles, 1)))%int64(len(bands))])
	return hz, err == nil
}

// BandAt returns the dial frequency for the cycle starting at t (an even
// UTC minute), and the slot number it came from (0: Config.Band or the
// rotation).
func (c *Config) BandAt(t time.Time) (int64, int) {
	t = t.UTC()
	n := t.Unix() / 120 // two-minute cycles since 1970
	switch c.Schedule.Mode {
	case ModeRotation:
		if hz, ok := pick(c.Schedule.Rotation.Bands, c.Schedule.Rotation.Cycles, n); ok {
			return hz, 0
		}
	case ModeSlots:
		m := t.Hour()*60 + t.Minute()
		for i, sl := range c.Schedule.Slots {
			if !sl.contains(m) {
				continue
			}
			if hz, ok := pick(sl.Bands, sl.Cycles, n); ok {
				return hz, i + 1
			}
		}
	}
	return c.DialHz, 0
}

// Planned is one upcoming cycle and its band.
type Planned struct {
	Start  time.Time `json:"start"`
	DialHz int64     `json:"dial_hz"`
	Band   string    `json:"band"`
	Slot   int       `json:"slot"`
}

// Plan lists the next n cycles from now.
func (c *Config) Plan(now time.Time, n int) []Planned {
	t := now.UTC().Truncate(2 * time.Minute).Add(2 * time.Minute)
	out := make([]Planned, 0, n)
	for range n {
		hz, slot := c.BandAt(t)
		out = append(out, Planned{Start: t, DialHz: hz, Band: BandLabel(hz), Slot: slot})
		t = t.Add(2 * time.Minute)
	}
	return out
}
