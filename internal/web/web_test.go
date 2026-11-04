package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/va3mw/FlexWSPR/internal/app"
	"github.com/va3mw/FlexWSPR/internal/config"
)

func station(t *testing.T) (*app.Station, string) {
	path := filepath.Join(t.TempDir(), "flexwspr.yaml")
	c := config.Default()
	c.DataDir = t.TempDir()
	c.Wsprd.Path = "wsprd"
	if err := c.Resolve(); err != nil {
		t.Fatal(err)
	}
	return app.NewStation(path, c, false, "test", io.Discard), path
}

func post(h http.Handler, path, body, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Host = "pi.local:8080"
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestSaveKeepsProgramPathsOverHTTP(t *testing.T) {
	st, path := station(t)
	h := Handler(st, Options{})
	body := `{"callsign":"va3mw","grid":"FN03","band":"40m","daxiq":{"channel":1,"rate":24000},
		"wsprd":{"path":"/bin/sh","args":["-c","echo hi"]},"data_dir":"/tmp/x","web":{"listen":":9999"}}`
	w := post(h, "/api/config", body, "http://pi.local:8080")
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Callsign != "VA3MW" || c.Band != "40m" {
		t.Errorf("settings not saved: %+v", c)
	}
	if c.Wsprd.Path != "wsprd" || len(c.Wsprd.Args) != 0 || c.DataDir == "/tmp/x" || c.Web.Listen == ":9999" {
		t.Errorf("program paths changed over HTTP: %+v %+v %q %q", c.Wsprd.Path, c.Wsprd.Args, c.DataDir, c.Web.Listen)
	}

	// The desktop window may change them.
	st2, path2 := station(t)
	if w := post(Handler(st2, Options{Local: true}), "/api/config", body, ""); w.Code != 200 {
		t.Fatalf("local save: %d %s", w.Code, w.Body)
	}
	c2, _ := config.Load(path2)
	if c2.Wsprd.Path != "/bin/sh" {
		t.Errorf("local save lost wsprd path: %q", c2.Wsprd.Path)
	}
}

func TestCrossSiteRefused(t *testing.T) {
	st, _ := station(t)
	h := Handler(st, Options{})
	if w := post(h, "/api/config", `{"callsign":"X"}`, "https://evil.example"); w.Code != http.StatusForbidden {
		t.Errorf("other origin: got %d", w.Code)
	}
	r := httptest.NewRequest("POST", "/api/config", strings.NewReader(`{"callsign":"X"}`))
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form post: got %d", w.Code)
	}
}

func TestBadSettingsAndPlan(t *testing.T) {
	st, _ := station(t)
	h := Handler(st, Options{})
	if w := post(h, "/api/config", `{"callsign":"VA3MW","grid":"ZZ99","daxiq":{"channel":1,"rate":24000}}`, ""); w.Code != 400 {
		t.Errorf("bad grid saved: %d", w.Code)
	}
	w := post(h, "/api/plan?n=3", `{"band":"20m","daxiq":{"channel":1,"rate":24000},
		"schedule":{"mode":"slots","slots":[{"start":"00:00","end":"00:00","bands":["40m"],"cycles":1}]}}`, "")
	var plan []config.Planned
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &plan) != nil || len(plan) != 3 || plan[0].Band != "40m" {
		t.Errorf("plan: %d %s", w.Code, w.Body)
	}
	r := httptest.NewRequest("GET", "/", nil)
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, r)
	if rw.Code != 200 || !strings.Contains(rw.Body.String(), "FlexWSPR") {
		t.Errorf("page: %d", rw.Code)
	}
}

func TestDistance(t *testing.T) {
	// FN03 (Toronto area) to DM79 (Denver area): about 2100 km.
	if d := app.Distance("FN03", "DM79"); d < 1950 || d > 2250 {
		t.Errorf("FN03-DM79 = %d km", d)
	}
	if app.Distance("FN03", "") != 0 {
		t.Error("blank grid")
	}
}

func TestServerFollowsSettings(t *testing.T) {
	st, _ := station(t)
	srv := Start("127.0.0.1:0", st, true)
	defer srv.Close()
	get := func(url string) int {
		resp, err := http.Get(url + "api/status")
		if err != nil {
			return 0
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	lastURL := func() string {
		lines, _ := st.Logs.Since(0)
		url := ""
		for _, l := range lines {
			if i := strings.Index(l, "url=http://"); i >= 0 {
				url = strings.Fields(l[i+4:])[0]
			}
		}
		return url
	}
	first := lastURL()
	if first == "" || get(first) != 200 {
		t.Fatalf("not serving at %q", first)
	}

	// A cross-site post is refused, as on the headless program.
	req, _ := http.NewRequest("POST", first+"api/config", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site post: %v %v", resp, err)
	}

	// Saving a new port in the desktop window moves the page.
	c := st.Config()
	c.Callsign, c.Grid = "VA3MW", "FN03"
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	c.Web.Listen = fmt.Sprintf("127.0.0.1:%d", port)
	if err := st.Apply(c); err != nil {
		t.Fatal(err)
	}
	second := lastURL()
	if second == first || get(second) != 200 {
		t.Fatalf("did not move: %q -> %q", first, second)
	}
	if get(first) == 200 {
		t.Error("old address still serving")
	}
	c.Web.Listen = "off"
	st.Apply(c)
	if get(second) == 200 {
		t.Error("still serving after off")
	}
}

func TestCrossSiteReadRefused(t *testing.T) {
	st, _ := station(t)
	h := Handler(st, Options{})
	for _, site := range []string{"cross-site", "same-origin", ""} {
		r := httptest.NewRequest("GET", "/api/status", nil)
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if want := site == "cross-site"; (w.Code == http.StatusForbidden) != want {
			t.Errorf("Sec-Fetch-Site %q: got %d", site, w.Code)
		}
	}
}
