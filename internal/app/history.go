package app

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/va3mw/FlexWSPR/internal/config"
)

// The Openings tab summarises the last day: which bands were listened to,
// what was heard on them hour by hour, and the stations heard. The history
// is reloaded from spots.txt and cycles.txt in the data folder on start.

const histSpan = 24 * time.Hour

// CyclesFile lists each decoded cycle, so the summary can tell a band that
// was listened to and quiet from one that was not listened to.
const CyclesFile = "cycles.txt"

type histCycle struct {
	t     time.Time
	band  string
	spots int
}

type history struct {
	cycles []histCycle // oldest first
	spots  []SpotInfo  // oldest first
	loaded string      // data folder loaded from
}

func (h *history) trim(now time.Time) {
	cut := now.Add(-histSpan)
	i := sort.Search(len(h.cycles), func(i int) bool { return !h.cycles[i].t.Before(cut) })
	h.cycles = h.cycles[i:]
	j := sort.Search(len(h.spots), func(i int) bool { return !h.spots[i].Time.Before(cut) })
	h.spots = h.spots[j:]
}

// HourCell is one band in one UTC hour.
type HourCell struct {
	Cycles int `json:"c"`  // cycles listened
	Spots  int `json:"s"`  // spots decoded
	Calls  int `json:"n"`  // different calls
	MaxKm  int `json:"km"` // farthest spot
}

// BandSummary is one band over the last 24 hours.
type BandSummary struct {
	Band     string     `json:"band"`
	Cycles   int        `json:"cycles"`
	Spots    int        `json:"spots"`
	Calls    int        `json:"calls"`
	LastHour int        `json:"last_hour"` // spots in the last 60 minutes
	Last     *time.Time `json:"last,omitempty"`
	Farthest *SpotInfo  `json:"farthest,omitempty"`
	Best     *SpotInfo  `json:"best,omitempty"`
	Hours    []HourCell `json:"hours"` // matches Summary.Hours
}

// StationSummary is one call heard in the last 24 hours.
type StationSummary struct {
	Call     string    `json:"call"`
	Grid     string    `json:"grid"`
	Km       int       `json:"km"`
	Spots    int       `json:"spots"`
	Bands    []string  `json:"bands"`
	BestSNR  int       `json:"best_snr"`
	DBm      int       `json:"dbm"`
	Score    float64   `json:"score"`
	HasScore bool      `json:"has_score"`
	Last     time.Time `json:"last"`
}

// Summary is the last 24 hours heard here.
type Summary struct {
	Now      time.Time        `json:"now"`
	Hours    []time.Time      `json:"hours"` // start of each UTC hour, oldest first; the last is the current hour
	Bands    []BandSummary    `json:"bands"` // bands listened to or heard, low to high
	Stations []StationSummary `json:"stations"`
	Cycles   int              `json:"cycles"`
	Spots    int              `json:"spots"`
	Calls    int              `json:"calls"`
}

func (h *history) summary(now time.Time) Summary {
	now = now.UTC()
	h.trim(now)
	first := now.Truncate(time.Hour).Add(-23 * time.Hour)
	sum := Summary{Now: now, Bands: []BandSummary{}, Stations: []StationSummary{}}
	for i := 0; i < 24; i++ {
		sum.Hours = append(sum.Hours, first.Add(time.Duration(i)*time.Hour))
	}
	hourOf := func(t time.Time) int {
		i := int(t.Sub(first) / time.Hour)
		if i < 0 || i > 23 {
			return -1
		}
		return i
	}
	byBand := map[string]*BandSummary{}
	get := func(b string) *BandSummary {
		bs := byBand[b]
		if bs == nil {
			bs = &BandSummary{Band: b, Hours: make([]HourCell, 24)}
			byBand[b] = bs
		}
		return bs
	}
	for _, c := range h.cycles {
		i := hourOf(c.t)
		if i < 0 {
			continue
		}
		bs := get(c.band)
		bs.Cycles++
		bs.Hours[i].Cycles++
		sum.Cycles++
	}
	hourCalls := map[string]map[string]bool{} // band/hour -> calls
	bandCalls := map[string]map[string]bool{}
	allCalls := map[string]bool{}
	stations := map[string]*StationSummary{}
	for i := range h.spots {
		s := h.spots[i]
		hi := hourOf(s.Time)
		if hi < 0 {
			continue
		}
		bs := get(s.Band)
		bs.Spots++
		sum.Spots++
		cell := &bs.Hours[hi]
		cell.Spots++
		if s.Call != "" && s.Km > cell.MaxKm {
			cell.MaxKm = s.Km
		}
		if now.Sub(s.Time) < time.Hour+2*time.Minute {
			bs.LastHour++
		}
		t := s.Time
		bs.Last = &t
		if s.Km > 0 && s.Call != "" && (bs.Farthest == nil || s.Km > bs.Farthest.Km) {
			f := s
			bs.Farthest = &f
		}
		if s.HasScore && (bs.Best == nil || s.Score > bs.Best.Score) {
			b := s
			bs.Best = &b
		}
		if s.Call == "" {
			continue
		}
		hk := fmt.Sprintf("%s/%d", s.Band, hi)
		if hourCalls[hk] == nil {
			hourCalls[hk] = map[string]bool{}
		}
		if !hourCalls[hk][s.Call] {
			hourCalls[hk][s.Call] = true
			cell.Calls++
		}
		if bandCalls[s.Band] == nil {
			bandCalls[s.Band] = map[string]bool{}
		}
		if !bandCalls[s.Band][s.Call] {
			bandCalls[s.Band][s.Call] = true
			bs.Calls++
		}
		allCalls[s.Call] = true
		st := stations[s.Call]
		if st == nil {
			st = &StationSummary{Call: s.Call, BestSNR: -99}
			stations[s.Call] = st
		}
		st.Spots++
		st.Last = s.Time
		if s.Grid != "" {
			st.Grid, st.Km, st.DBm = s.Grid, s.Km, s.DBm
		}
		if s.SNR > st.BestSNR {
			st.BestSNR = s.SNR
		}
		if s.HasScore && (!st.HasScore || s.Score > st.Score) {
			st.Score, st.HasScore = s.Score, true
		}
		if !contains(st.Bands, s.Band) {
			st.Bands = append(st.Bands, s.Band)
		}
	}
	sum.Calls = len(allCalls)
	for _, b := range config.BandOrder {
		if bs := byBand[b]; bs != nil {
			sum.Bands = append(sum.Bands, *bs)
			delete(byBand, b)
		}
	}
	var odd []string // frequencies off the standard bands
	for b := range byBand {
		odd = append(odd, b)
	}
	sort.Strings(odd)
	for _, b := range odd {
		sum.Bands = append(sum.Bands, *byBand[b])
	}
	for _, st := range stations {
		sort.Slice(st.Bands, func(i, j int) bool { return bandIndex(st.Bands[i]) < bandIndex(st.Bands[j]) })
		sum.Stations = append(sum.Stations, *st)
	}
	sort.Slice(sum.Stations, func(i, j int) bool {
		a, b := sum.Stations[i], sum.Stations[j]
		if a.HasScore != b.HasScore {
			return a.HasScore
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.Call < b.Call
	})
	return sum
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func bandIndex(b string) int {
	for i, n := range config.BandOrder {
		if n == b {
			return i
		}
	}
	return len(config.BandOrder)
}

// scoreSpot fills in the distance and score of a spot heard at myGrid.
func scoreSpot(si *SpotInfo, myGrid string) {
	si.Km = Distance(myGrid, si.Grid)
	if si.Km > 0 && si.Call != "" {
		si.Score, si.HasScore = math.Round(Score(si.SNR, si.DBm, si.Km)*10)/10, true
	}
}

// load reads the last day of spots and cycles from the data folder. Only
// the end of a long spots file is read.
func (h *history) load(spotsPath, cyclesPath, myGrid string, now time.Time) {
	cut := now.Add(-histSpan)
	h.spots, h.cycles = nil, nil
	readTail(spotsPath, func(line string) {
		if si, ok := parseSpotLine(line); ok && !si.Time.Before(cut) {
			scoreSpot(&si, myGrid)
			h.spots = append(h.spots, si)
		}
	})
	readTail(cyclesPath, func(line string) {
		if c, ok := parseCycleLine(line); ok && !c.t.Before(cut) {
			h.cycles = append(h.cycles, c)
		}
	})
	sort.SliceStable(h.spots, func(i, j int) bool { return h.spots[i].Time.Before(h.spots[j].Time) })
	sort.SliceStable(h.cycles, func(i, j int) bool { return h.cycles[i].t.Before(h.cycles[j].t) })
}

// readTail calls fn for each line in the last 16 MB of the file.
func readTail(path string, fn func(string)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	const max = 16 << 20
	skip := false
	if fi, err := f.Stat(); err == nil && fi.Size() > max {
		f.Seek(fi.Size()-max, io.SeekStart)
		skip = true // part line
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if skip {
			skip = false
			continue
		}
		fn(sc.Text())
	}
}

// parseSpotLine reads a spots.txt line as written by wspr.Spot.Format:
// "261104 1252  -26   0.9   14.097053 WW0WWV       DN70    30   0".
func parseSpotLine(line string) (SpotInfo, bool) {
	f := strings.Fields(line)
	if len(f) != 9 {
		return SpotInfo{}, false
	}
	t, err := time.Parse("060102 1504", f[0]+" "+f[1])
	if err != nil {
		return SpotInfo{}, false
	}
	snr, e1 := strconv.Atoi(f[2])
	dt, e2 := strconv.ParseFloat(f[3], 64)
	mhz, e3 := strconv.ParseFloat(f[4], 64)
	dbm, e4 := strconv.Atoi(f[7])
	drift, e5 := strconv.Atoi(f[8])
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
		return SpotInfo{}, false
	}
	hz := int64(math.Round(mhz * 1e6))
	si := SpotInfo{Time: t, Band: config.BandOf(hz), Call: f[5], Grid: f[6], SNR: snr, DT: dt, FreqHz: hz, DBm: dbm, Drift: drift}
	if si.Band == "" {
		si.Band = fmt.Sprintf("%.4f MHz", mhz)
	}
	if si.Call == "<...>" {
		si.Call = ""
	}
	if si.Grid == "-" {
		si.Grid = ""
	}
	return si, true
}

// cycleLine is a cycles.txt line: "261104 1252 20m 14.095600 5".
func cycleLine(c CycleInfo) string {
	return fmt.Sprintf("%s %s %.6f %d", c.Start.UTC().Format("060102 1504"), strings.ReplaceAll(c.Band, " ", ""), float64(c.DialHz)/1e6, c.Spots)
}

func parseCycleLine(line string) (histCycle, bool) {
	f := strings.Fields(line)
	if len(f) != 5 {
		return histCycle{}, false
	}
	t, err := time.Parse("060102 1504", f[0]+" "+f[1])
	if err != nil {
		return histCycle{}, false
	}
	mhz, e1 := strconv.ParseFloat(f[3], 64)
	n, e2 := strconv.Atoi(f[4])
	if e1 != nil || e2 != nil {
		return histCycle{}, false
	}
	b := config.BandLabel(int64(math.Round(mhz * 1e6)))
	return histCycle{t: t, band: b, spots: n}, true
}
