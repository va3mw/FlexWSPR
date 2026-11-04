// Package upload sends spots to wsprnet.org the way WSJT-X does
// (Network/wsprnet.cpp): one form-encoded POST per spot to
// http://wsprnet.org/post/ with function=wspr, and a function=wsprstat
// report for a cycle with no spots.
package upload

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/va3mw/FlexWSPR/internal/wspr"
)

// DefaultURL is wsprnet.org's post endpoint, as used by WSJT-X.
const DefaultURL = "http://wsprnet.org/post/"

const (
	maxQueue = 5000           // requests kept while wsprnet is unreachable
	maxAge   = 24 * time.Hour // older requests are dropped
	spacing  = 250 * time.Millisecond
)

// Request is one queued POST.
type Request struct {
	Form  string    `json:"form"`  // url-encoded body
	Spot  bool      `json:"spot"`  // a spot (reply must say "spot(s) added")
	Cycle time.Time `json:"cycle"` // for logging and expiry
	Desc  string    `json:"desc"`
}

// Uploader queues requests and sends them in order, retrying while
// wsprnet.org is unreachable. The queue is saved to disk so a restart does
// not lose spots.
type Uploader struct {
	URL       string
	Call      string
	Grid      string
	Version   string
	QueueFile string
	Client    *http.Client
	Log       *slog.Logger

	mu    sync.Mutex
	queue []Request
	sent  int // spots accepted since start
	wake  chan struct{}
}

// New returns an uploader; call Load then Run.
func New(call, grid, version, queueFile string, log *slog.Logger) *Uploader {
	return &Uploader{URL: DefaultURL, Call: call, Grid: grid, Version: version, QueueFile: queueFile,
		Client: &http.Client{Timeout: 20 * time.Second}, Log: log, wake: make(chan struct{}, 1)}
}

// Load restores requests saved by a previous run.
func (u *Uploader) Load() {
	b, err := os.ReadFile(u.QueueFile)
	if err != nil {
		return
	}
	var q []Request
	if err := json.Unmarshal(b, &q); err != nil {
		u.Log.Warn("ignoring unreadable upload queue", "file", u.QueueFile, "err", err)
		return
	}
	u.mu.Lock()
	u.queue = append(q, u.queue...)
	u.mu.Unlock()
	if len(q) > 0 {
		u.Log.Info("restored queued uploads", "count", len(q))
		u.poke()
	}
}

// Cycle queues the results of one decoded cycle: its spots, or a status
// report when there are none. dialHz is the receive dial frequency.
func (u *Uploader) Cycle(start time.Time, dialHz int64, spots []wspr.Spot) {
	rqrg := mhz(dialHz)
	var reqs []Request
	for _, s := range spots {
		if s.Call == "" {
			continue // unresolved hashed callsign
		}
		if math.Abs(float64(s.FreqHz-dialHz)) > 10000 {
			continue // not on this band (WSJT-X applies the same 10 kHz check)
		}
		v := url.Values{}
		v.Set("function", "wspr")
		v.Set("date", start.UTC().Format("060102"))
		v.Set("time", start.UTC().Format("1504"))
		v.Set("sig", fmt.Sprint(s.SNR))
		v.Set("dt", fmt.Sprintf("%.1f", s.DT))
		v.Set("drift", fmt.Sprint(s.Drift))
		v.Set("tqrg", mhz(s.FreqHz))
		v.Set("tcall", s.Call)
		v.Set("tgrid", s.Grid)
		v.Set("dbm", fmt.Sprint(s.DBm))
		u.reporter(v, rqrg)
		reqs = append(reqs, Request{Form: v.Encode(), Spot: true, Cycle: start, Desc: s.Call})
	}
	if len(reqs) == 0 {
		v := url.Values{}
		v.Set("function", "wsprstat")
		v.Set("rcall", u.Call)
		v.Set("rgrid", u.Grid)
		v.Set("rqrg", rqrg)
		v.Set("tpct", "0")
		v.Set("tqrg", mhz(dialHz+1500))
		v.Set("dbm", "0")
		v.Set("version", u.Version)
		v.Set("mode", "2")
		reqs = append(reqs, Request{Form: v.Encode(), Cycle: start, Desc: "status"})
	}
	u.mu.Lock()
	u.queue = append(u.queue, reqs...)
	if n := len(u.queue) - maxQueue; n > 0 {
		u.Log.Warn("upload queue full; dropping oldest", "dropped", n)
		u.queue = u.queue[n:]
	}
	u.saveLocked()
	u.mu.Unlock()
	u.poke()
}

func (u *Uploader) reporter(v url.Values, rqrg string) {
	v.Set("version", u.Version)
	v.Set("rcall", u.Call)
	v.Set("rgrid", u.Grid)
	v.Set("rqrg", rqrg)
	v.Set("mode", "2")
}

func (u *Uploader) poke() {
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

// Run sends queued requests until ctx is cancelled.
func (u *Uploader) Run(ctx context.Context) {
	backoff := 30 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-u.wake:
		case <-time.After(backoff):
		}
		for {
			u.mu.Lock()
			u.expireLocked()
			if len(u.queue) == 0 {
				u.mu.Unlock()
				break
			}
			r := u.queue[0]
			u.mu.Unlock()

			retry, err := u.send(ctx, r)
			if err != nil && retry {
				u.Log.Warn("wsprnet upload failed; will retry", "err", err, "queued", u.Len())
				if backoff < 10*time.Minute {
					backoff *= 2
				}
				break
			}
			backoff = 30 * time.Second
			if err != nil {
				u.Log.Warn("wsprnet rejected upload; dropping it", "what", r.Desc, "err", err)
			} else {
				u.Log.Debug("uploaded", "what", r.Desc, "cycle", r.Cycle.Format("15:04"))
			}
			u.mu.Lock()
			if err == nil && r.Spot {
				u.sent++
			}
			if len(u.queue) > 0 && u.queue[0].Form == r.Form {
				u.queue = u.queue[1:]
			}
			u.saveLocked()
			u.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(spacing):
			}
		}
	}
}

// send posts one request. retry reports whether a failure is worth retrying
// (network or server trouble) rather than a rejection.
func (u *Uploader) send(ctx context.Context, r Request) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.URL, strings.NewReader(r.Form))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "FlexWSPR/"+u.Version)
	resp, err := u.Client.Do(req)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return true, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	if r.Spot && !strings.Contains(string(body), "spot(s) added") {
		return false, fmt.Errorf("unexpected reply: %s", snippet(body))
	}
	return false, nil
}

// Sent is the number of spots wsprnet has accepted since start.
func (u *Uploader) Sent() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.sent
}

// Len is the number of queued requests.
func (u *Uploader) Len() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.queue)
}

func (u *Uploader) expireLocked() {
	cut := time.Now().Add(-maxAge)
	n := 0
	for n < len(u.queue) && u.queue[n].Cycle.Before(cut) {
		n++
	}
	if n > 0 {
		u.Log.Warn("dropping uploads older than 24 h", "count", n)
		u.queue = u.queue[n:]
		u.saveLocked()
	}
}

// saveLocked writes the queue to disk (atomically), or removes the file when
// the queue is empty.
func (u *Uploader) saveLocked() {
	if u.QueueFile == "" {
		return
	}
	if len(u.queue) == 0 {
		os.Remove(u.QueueFile)
		return
	}
	b, _ := json.Marshal(u.queue)
	tmp := u.QueueFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err == nil {
		os.Rename(tmp, u.QueueFile)
	}
}

func mhz(hz int64) string { return fmt.Sprintf("%.6f", float64(hz)/1e6) }

func snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
