// Package area asks the public WSPR database at wspr.live what stations
// near you have heard or been heard by, band by band, to show which bands
// are open from your area even when FlexWSPR is listening elsewhere.
//
// A path counts when either end (transmitter or receiver) is within the
// radius of your grid. Results are cached, so however many pages are open
// FlexWSPR asks wspr.live at most every few minutes.
package area

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/va3mw/FlexWSPR/internal/config"
)

// DefaultURL is the wspr.live ClickHouse query endpoint.
const DefaultURL = "https://db1.wspr.live/"

const (
	recentWindow = 30 * time.Minute
	recentEvery  = 5 * time.Minute
	dayEvery     = 15 * time.Minute
)

// wspr.live band numbers (MHz, rounded down) to band names.
var bandNames = map[int]string{
	-1: "2200m", 0: "630m", 1: "160m", 3: "80m", 5: "60m", 7: "40m", 10: "30m",
	14: "20m", 18: "17m", 21: "15m", 24: "12m", 28: "10m", 50: "6m",
}

// Band is one band's activity near you.
type Band struct {
	Band  string `json:"band"`
	Paths int    `json:"paths"` // spots with one end near you
	Tx    int    `json:"tx"`    // different transmitters
	Rx    int    `json:"rx"`    // different receivers
	MaxKm int    `json:"max_km"`
	K1    int    `json:"k1"`  // paths of 1000 km or more
	K5    int    `json:"k5"`  // paths of 5000 km or more
	Far   string `json:"far"` // the longest path, "TX GRID > RX GRID"
}

// Hour is one band in one UTC hour.
type Hour struct {
	Paths int `json:"p"`
	MaxKm int `json:"km"`
	K1    int `json:"k1"`
	K5    int `json:"k5"`
}

// Report is the activity near one grid.
type Report struct {
	Grid     string            `json:"grid"`
	RadiusKm int               `json:"radius_km"`
	Window   int               `json:"window_min"` // Bands covers this many minutes
	Fetched  time.Time         `json:"fetched"`
	Bands    []Band            `json:"bands"` // low to high; bands with no paths are left out
	Hours    []time.Time       `json:"hours"` // start of each UTC hour, oldest first
	Day      map[string][]Hour `json:"day"`   // band -> one entry per Hours
	DayAt    time.Time         `json:"day_fetched"`
	Error    string            `json:"error,omitempty"` // last fetch failed; data may be older
}

// Client fetches and caches reports.
type Client struct {
	URL       string
	UserAgent string
	HTTP      *http.Client

	mu   sync.Mutex
	key  string
	rep  *Report
	next time.Time // next recent fetch
	dayN time.Time // next day fetch
	busy chan struct{}
}

// New returns a client for wspr.live.
func New(userAgent string) *Client {
	return &Client{URL: DefaultURL, UserAgent: userAgent, HTTP: &http.Client{Timeout: 25 * time.Second}, busy: make(chan struct{}, 1)}
}

// Get returns the report for the area around lat/lon, fetching it when the
// cached one is old. Only one fetch runs at a time; others wait for it. The
// fetch is shared by every page, so it runs on its own timeout rather than
// ctx: a page closed mid-fetch does not cancel it for the others. ctx only
// limits how long this caller waits for its turn.
func (c *Client) Get(ctx context.Context, grid string, lat, lon float64, radiusKm int) (*Report, error) {
	select {
	case c.busy <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.busy }()

	key := fmt.Sprintf("%s/%d", grid, radiusKm)
	now := time.Now().UTC()
	c.mu.Lock()
	if c.key != key {
		c.key, c.rep, c.next, c.dayN = key, nil, time.Time{}, time.Time{}
	}
	rep := c.rep
	needRecent, needDay := rep == nil || now.After(c.next), rep == nil || now.After(c.dayN)
	c.mu.Unlock()
	if !needRecent && !needDay {
		return result(rep)
	}

	n := &Report{Grid: grid, RadiusKm: radiusKm, Window: int(recentWindow / time.Minute)}
	if rep != nil {
		*n = *rep
	}
	fctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var (
		wg                sync.WaitGroup
		errRecent, errDay error
		bands             []Band
		hours             []time.Time
		day               map[string][]Hour
	)
	if needRecent {
		wg.Add(1)
		go func() { defer wg.Done(); bands, errRecent = c.recent(fctx, lat, lon, radiusKm) }()
	}
	if needDay {
		wg.Add(1)
		go func() { defer wg.Done(); hours, day, errDay = c.day(fctx, lat, lon, radiusKm, now) }()
	}
	wg.Wait()
	if needRecent && errRecent == nil {
		n.Bands, n.Fetched = bands, now
	}
	if needDay && errDay == nil {
		n.Hours, n.Day, n.DayAt = hours, day, now
	}
	n.Error = ""
	if err := errors.Join(errRecent, errDay); err != nil {
		n.Error = err.Error()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rep = n
	// After a failure try again in a minute rather than at once.
	if needRecent {
		c.next = now.Add(recentEvery)
		if errRecent != nil {
			c.next = now.Add(time.Minute)
		}
	}
	if needDay {
		c.dayN = now.Add(dayEvery)
		if errDay != nil {
			c.dayN = now.Add(time.Minute)
		}
	}
	return result(n)
}

// result returns a cached report, or its error when it holds no data at all.
func result(r *Report) (*Report, error) {
	if r.Bands == nil && r.Day == nil && r.Error != "" {
		return r, errors.New(r.Error)
	}
	return r, nil
}

// near is the SQL condition for a path with either end within r km.
func near(lat, lon float64, r int) string {
	return fmt.Sprintf("(greatCircleDistance(rx_lon, rx_lat, %.4f, %.4f) <= %d OR greatCircleDistance(tx_lon, tx_lat, %.4f, %.4f) <= %d)",
		lon, lat, r*1000, lon, lat, r*1000)
}

func (c *Client) recent(ctx context.Context, lat, lon float64, r int) ([]Band, error) {
	q := fmt.Sprintf(`SELECT band, count(), uniq(tx_sign), uniq(rx_sign), max(distance), countIf(distance >= 1000), countIf(distance >= 5000),
 argMax(concat(tx_sign, ' ', tx_loc, ' > ', rx_sign, ' ', rx_loc), distance)
 FROM wspr.rx WHERE time >= now() - INTERVAL %d MINUTE AND %s GROUP BY band`, int(recentWindow/time.Minute), near(lat, lon, r))
	rows, err := c.query(ctx, q)
	if err != nil {
		return nil, err
	}
	out := []Band{}
	for _, row := range rows {
		var b Band
		var num int
		if len(row) != 8 || !num2(row[0], &num) || !num2(row[1], &b.Paths) || !num2(row[2], &b.Tx) || !num2(row[3], &b.Rx) ||
			!num2(row[4], &b.MaxKm) || !num2(row[5], &b.K1) || !num2(row[6], &b.K5) || json.Unmarshal(row[7], &b.Far) != nil {
			return nil, fmt.Errorf("wspr.live: unexpected row")
		}
		if b.Band = bandNames[num]; b.Band != "" {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return order(out[i].Band) < order(out[j].Band) })
	return out, nil
}

func (c *Client) day(ctx context.Context, lat, lon float64, r int, now time.Time) ([]time.Time, map[string][]Hour, error) {
	first := now.Truncate(time.Hour).Add(-23 * time.Hour)
	q := fmt.Sprintf(`SELECT band, toUnixTimestamp(toStartOfHour(time)) AS h, count(), max(distance), countIf(distance >= 1000), countIf(distance >= 5000)
 FROM wspr.rx WHERE time >= toDateTime(%d) AND %s GROUP BY band, h`, first.Unix(), near(lat, lon, r))
	rows, err := c.query(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	hours := make([]time.Time, 24)
	for i := range hours {
		hours[i] = first.Add(time.Duration(i) * time.Hour)
	}
	day := map[string][]Hour{}
	for _, row := range rows {
		var num, h int
		var cell Hour
		if len(row) != 6 || !num2(row[0], &num) || !num2(row[1], &h) || !num2(row[2], &cell.Paths) || !num2(row[3], &cell.MaxKm) ||
			!num2(row[4], &cell.K1) || !num2(row[5], &cell.K5) {
			return nil, nil, fmt.Errorf("wspr.live: unexpected row")
		}
		name := bandNames[num]
		i := int((int64(h) - first.Unix()) / 3600)
		if name == "" || i < 0 || i > 23 {
			continue
		}
		if day[name] == nil {
			day[name] = make([]Hour, 24)
		}
		day[name][i] = cell
	}
	return hours, day, nil
}

func (c *Client) query(ctx context.Context, q string) ([][]json.RawMessage, error) {
	u := c.URL + "?query=" + url.QueryEscape(q+" FORMAT JSONCompact")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wspr.live: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("wspr.live: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := string(body)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("wspr.live: %s: %s", resp.Status, msg)
	}
	var out struct {
		Data [][]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("wspr.live: %w", err)
	}
	return out.Data, nil
}

// num2 reads a JSON number, or a number in a string (ClickHouse writes
// 64-bit integers as strings).
func num2(m json.RawMessage, v *int) bool {
	if json.Unmarshal(m, v) == nil {
		return true
	}
	var s string
	if json.Unmarshal(m, &s) != nil {
		return false
	}
	_, err := fmt.Sscan(s, v)
	return err == nil
}

func order(b string) int {
	for i, n := range config.BandOrder {
		if n == b {
			return i
		}
	}
	return len(config.BandOrder)
}
