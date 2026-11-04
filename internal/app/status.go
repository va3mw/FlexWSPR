package app

import (
	"math"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/va3mw/FlexWSPR/internal/config"
	"github.com/va3mw/FlexWSPR/internal/wspr"
)

// Station states shown in the GUI.
const (
	StateSetup      = "setup"      // callsign/grid not set yet
	StateSearching  = "searching"  // looking for a radio
	StateConnecting = "connecting" // talking to the radio
	StateRunning    = "running"    // streaming and decoding
	StateError      = "error"      // last session failed; retrying
	StateStopped    = "stopped"
)

const (
	keepCycles = 60
	keepSpots  = 500
)

// CycleInfo is one decoded cycle.
type CycleInfo struct {
	Start  time.Time `json:"start"`
	Band   string    `json:"band"`
	DialHz int64     `json:"dial_hz"`
	Spots  int       `json:"spots"`
	Gaps   int       `json:"gaps"`
	Error  string    `json:"error,omitempty"`
}

// SpotInfo is one decoded spot, for the GUI.
type SpotInfo struct {
	Time   time.Time `json:"time"`
	Band   string    `json:"band"`
	Call   string    `json:"call"`
	Grid   string    `json:"grid"`
	SNR    int       `json:"snr"`
	DT     float64   `json:"dt"`
	FreqHz int64     `json:"freq_hz"`
	Drift  int       `json:"drift"`
	DBm    int       `json:"dbm"`
	Km     int       `json:"km"` // 0 when unknown
	// Score is the signal as if sent with 1 W over 1000 km, in dB (see
	// Score); HasScore is false when the distance is unknown.
	Score    float64 `json:"score"`
	HasScore bool    `json:"has_score"`
}

// Score rates how good a spot is, as the SNR it would have had from a 1 W
// transmitter 1000 km away:
//
//	score = SNR - (dBm - 30) + 20*log10(km / 1000)
//
// The power term removes the transmitter's advantage: 5 W (37 dBm) is
// 7 dB louder than 1 W, so 7 dB comes off. The distance term credits the
// path: received power falls roughly with the square of distance, so each
// doubling of distance is worth 6 dB. A -24 dB signal from a 200 mW station
// 7000 km away (score -0.1) beats a -10 dB signal from a 5 W station 800 km
// away (score -18.9). Distances under 100 km count as 100 km, so local
// stations are not marked down without limit.
func Score(snr, dbm, km int) float64 {
	d := math.Max(float64(km), 100)
	return float64(snr) - float64(dbm-30) + 20*math.Log10(d/1000)
}

// Status is a snapshot of the station for the GUI.
type Status struct {
	Version     string              `json:"version"`
	State       string              `json:"state"`
	Error       string              `json:"error,omitempty"`
	Since       time.Time           `json:"since"`
	Radio       string              `json:"radio"`
	DialHz      int64               `json:"dial_hz"`
	Band        string              `json:"band"`
	RxAnt       string              `json:"rxant"`
	Antennas    []string            `json:"antennas"` // as reported by the radio
	Next        []config.Planned    `json:"next"`
	Cycles      []CycleInfo         `json:"cycles"` // newest first
	Spots       []SpotInfo          `json:"spots"`  // newest first
	Upload      bool                `json:"upload"`
	UploadQueue int                 `json:"upload_queue"`
	Uploaded    int                 `json:"uploaded"` // spots accepted by wsprnet since start
	ConfigPath  string              `json:"config_path"`
	ConfigSaved bool                `json:"config_saved"` // false: running on defaults
	Now         time.Time           `json:"now"`
	BandSpots   map[string]int      `json:"band_spots"` // spots per band since start
	Best        *SpotInfo           `json:"best"`       // highest Score since start
	BestByBand  map[string]SpotInfo `json:"best_by_band"`
}

// monitor collects what the station is doing.
type monitor struct {
	mu   sync.Mutex
	st   Status
	hist history
}

func (m *monitor) set(fn func(s *Status)) {
	m.mu.Lock()
	fn(&m.st)
	m.mu.Unlock()
}

func (m *monitor) state(state, errText string) {
	m.set(func(s *Status) {
		if s.State != state {
			s.Since = time.Now().UTC()
		}
		s.State, s.Error = state, errText
	})
}

func (m *monitor) snapshot() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.st
	s.Cycles = append([]CycleInfo(nil), s.Cycles...)
	s.Spots = append([]SpotInfo(nil), s.Spots...)
	s.BandSpots = make(map[string]int, len(m.st.BandSpots))
	for k, v := range m.st.BandSpots {
		s.BandSpots[k] = v
	}
	s.BestByBand = make(map[string]SpotInfo, len(m.st.BestByBand))
	for k, v := range m.st.BestByBand {
		s.BestByBand[k] = v
	}
	if m.st.Best != nil {
		b := *m.st.Best
		s.Best = &b
	}
	s.Now = time.Now().UTC()
	return s
}

func (m *monitor) cycle(ci CycleInfo, spots []wspr.Spot, myGrid string) {
	m.set(func(s *Status) {
		s.Cycles = append([]CycleInfo{ci}, s.Cycles...)
		if len(s.Cycles) > keepCycles {
			s.Cycles = s.Cycles[:keepCycles]
		}
		if s.BandSpots == nil {
			s.BandSpots = map[string]int{}
		}
		add := make([]SpotInfo, 0, len(spots))
		for i := len(spots) - 1; i >= 0; i-- { // newest first, keep wsprd order within a cycle
			sp := spots[i]
			si := SpotInfo{Time: sp.Start, Band: ci.Band, Call: sp.Call, Grid: sp.Grid,
				SNR: sp.SNR, DT: sp.DT, FreqHz: sp.FreqHz, Drift: sp.Drift, DBm: sp.DBm}
			scoreSpot(&si, myGrid)
			if si.HasScore {
				if s.Best == nil || si.Score > s.Best.Score {
					b := si
					s.Best = &b
				}
				if s.BestByBand == nil {
					s.BestByBand = map[string]SpotInfo{}
				}
				if b, ok := s.BestByBand[ci.Band]; !ok || si.Score > b.Score {
					s.BestByBand[ci.Band] = si
				}
			}
			add = append(add, si)
			s.BandSpots[ci.Band]++
		}
		if ci.Error == "" {
			m.hist.cycles = append(m.hist.cycles, histCycle{t: ci.Start, band: ci.Band, spots: ci.Spots})
			for i := len(add) - 1; i >= 0; i-- {
				m.hist.spots = append(m.hist.spots, add[i])
			}
			m.hist.trim(time.Now())
		}
		s.Spots = append(add, s.Spots...)
		if len(s.Spots) > keepSpots {
			s.Spots = s.Spots[:keepSpots]
		}
	})
}

// summary is the last 24 hours heard here.
func (m *monitor) summary() Summary {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hist.summary(time.Now())
}

// loadHistory reloads the last day from the data folder when the files or
// the grid (which sets every distance) have changed. The files are read
// without holding the lock, so the GUI keeps updating meanwhile.
func (m *monitor) loadHistory(cfg *config.Config) {
	key := cfg.DataDir + "|" + cfg.SpotsFile + "|" + strings.ToUpper(cfg.Grid)
	m.mu.Lock()
	same := m.hist.loaded == key
	m.mu.Unlock()
	if same {
		return
	}
	var h history
	h.load(cfg.SpotsFile, filepath.Join(cfg.DataDir, CyclesFile), cfg.Grid, time.Now())
	h.loaded = key
	m.mu.Lock()
	m.hist = h
	m.mu.Unlock()
}

// Distance is the great-circle distance in km between two Maidenhead grids
// (4 or 6 characters), or 0 if either is not a grid.
func Distance(a, b string) int {
	la, oa, ok1 := gridLatLon(a)
	lb, ob, ok2 := gridLatLon(b)
	if !ok1 || !ok2 {
		return 0
	}
	r := math.Pi / 180
	d := math.Sin(la*r)*math.Sin(lb*r) + math.Cos(la*r)*math.Cos(lb*r)*math.Cos((ob-oa)*r)
	return int(math.Acos(math.Max(-1, math.Min(1, d)))*6371 + 0.5)
}

// gridLatLon returns the centre of a grid square.
func gridLatLon(g string) (lat, lon float64, ok bool) {
	g = strings.ToUpper(strings.TrimSpace(g))
	if len(g) != 4 && len(g) != 6 {
		return 0, 0, false
	}
	if g[0] < 'A' || g[0] > 'R' || g[1] < 'A' || g[1] > 'R' || g[2] < '0' || g[2] > '9' || g[3] < '0' || g[3] > '9' {
		return 0, 0, false
	}
	lon = float64(g[0]-'A')*20 - 180 + float64(g[2]-'0')*2
	lat = float64(g[1]-'A')*10 - 90 + float64(g[3]-'0')
	if len(g) == 6 {
		if g[4] < 'A' || g[4] > 'X' || g[5] < 'A' || g[5] > 'X' {
			return 0, 0, false
		}
		lon += float64(g[4]-'A')*(2.0/24) + 1.0/24
		lat += float64(g[5]-'A')*(1.0/24) + 0.5/24
	} else {
		lon++
		lat += 0.5
	}
	return lat, lon, true
}
