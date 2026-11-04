package upload

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/va3mw/FlexWSPR/internal/wspr"
)

type fake struct {
	mu    sync.Mutex
	got   []url.Values
	down  atomic.Bool
	reply string
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.down.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	b, _ := io.ReadAll(r.Body)
	v, _ := url.ParseQuery(string(b))
	f.mu.Lock()
	f.got = append(f.got, v)
	f.mu.Unlock()
	io.WriteString(w, f.reply)
}

func (f *fake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func newTest(t *testing.T, srv *httptest.Server) *Uploader {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	u := New("VA3MW", "FN03", "FlexWSPR-test", filepath.Join(t.TempDir(), "q.json"), log)
	u.URL = srv.URL
	return u
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("timed out")
}

var cycle = time.Date(2026, 11, 3, 22, 20, 0, 0, time.UTC)

func spots() []wspr.Spot {
	return []wspr.Spot{
		{Start: cycle, SNR: -22, DT: 0.1, FreqHz: 14097146, Drift: 0, Call: "VK3MO", Grid: "QF22", DBm: 37},
		{Start: cycle, SNR: -25, DT: 0.3, FreqHz: 14097101, Drift: -1, Call: "", Grid: "DO43XH", DBm: 30}, // unresolved
		{Start: cycle, SNR: -10, DT: 0, FreqHz: 7040100, Call: "K1ABC", Grid: "FN42", DBm: 23},            // wrong band
		{Start: cycle, SNR: -9, DT: -0.8, FreqHz: 14097112, Drift: -1, Call: "PJ4/K1ABC", DBm: 37},
	}
}

func TestSpotFields(t *testing.T) {
	f := &fake{reply: "1 spot(s) added"}
	srv := httptest.NewServer(f)
	defer srv.Close()
	u := newTest(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go u.Run(ctx)
	u.Cycle(cycle, 14095600, spots())
	waitFor(t, func() bool { return f.count() == 2 && u.Len() == 0 })

	want := map[string]string{"function": "wspr", "date": "261103", "time": "2220", "sig": "-22", "dt": "0.1",
		"drift": "0", "tqrg": "14.097146", "tcall": "VK3MO", "tgrid": "QF22", "dbm": "37",
		"rcall": "VA3MW", "rgrid": "FN03", "rqrg": "14.095600", "mode": "2", "version": "FlexWSPR-test"}
	for k, w := range want {
		if g := f.got[0].Get(k); g != w {
			t.Errorf("%s = %q, want %q", k, g, w)
		}
	}
	if f.got[1].Get("tcall") != "PJ4/K1ABC" || f.got[1].Get("tgrid") != "" {
		t.Errorf("type 2 spot: %v", f.got[1])
	}
}

func TestEmptyCycleSendsStatus(t *testing.T) {
	f := &fake{reply: "ok"}
	srv := httptest.NewServer(f)
	defer srv.Close()
	u := newTest(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go u.Run(ctx)
	u.Cycle(cycle, 14095600, nil)
	waitFor(t, func() bool { return f.count() == 1 && u.Len() == 0 })
	v := f.got[0]
	if v.Get("function") != "wsprstat" || v.Get("rqrg") != "14.095600" || v.Get("tqrg") != "14.097100" || v.Get("tpct") != "0" {
		t.Errorf("status: %v", v)
	}
}

func TestRetriesWhileDownAndSurvivesRestart(t *testing.T) {
	f := &fake{reply: "1 spot(s) added"}
	f.down.Store(true)
	srv := httptest.NewServer(f)
	defer srv.Close()
	u := newTest(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	go u.Run(ctx)
	u.Cycle(cycle, 14095600, spots())
	time.Sleep(300 * time.Millisecond)
	cancel()
	if u.Len() != 2 {
		t.Fatalf("queue %d while server down, want 2", u.Len())
	}

	// A new uploader with the same queue file picks the spots up.
	f.down.Store(false)
	u2 := newTest(t, srv)
	u2.QueueFile = u.QueueFile
	u2.Load()
	if u2.Len() != 2 {
		t.Fatalf("restored %d, want 2", u2.Len())
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go u2.Run(ctx2)
	waitFor(t, func() bool { return f.count() == 2 && u2.Len() == 0 })
}

func TestRejectedSpotIsDropped(t *testing.T) {
	f := &fake{reply: "error: bad grid"}
	srv := httptest.NewServer(f)
	defer srv.Close()
	u := newTest(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go u.Run(ctx)
	u.Cycle(cycle, 14095600, spots()[:1])
	waitFor(t, func() bool { return f.count() == 1 && u.Len() == 0 })
}
