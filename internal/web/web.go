// Package web serves the FlexWSPR GUI: a single page and a small JSON API.
// The headless program serves it over HTTP and the desktop app shows the
// same handler in its window.
package web

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/va3mw/FlexWSPR/internal/app"
	"github.com/va3mw/FlexWSPR/internal/config"
	"github.com/va3mw/FlexWSPR/internal/flex"
)

//go:embed static
var static embed.FS

// Options set what the GUI may change.
type Options struct {
	// Local is true for the desktop app's own window. Over HTTP the page
	// cannot change the program paths (wsprd, data folder, web address):
	// anyone who can reach the page could otherwise make FlexWSPR run another
	// program. Those stay editable in the config file.
	Local bool
}

// Handler returns the GUI and its API for st.
func Handler(st *app.Station, opt Options) http.Handler {
	mux := http.NewServeMux()
	files, _ := fs.Sub(static, "static")
	mux.Handle("GET /", http.FileServerFS(files))

	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, st.Status())
	})
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"config": st.Config(), "local": opt.Local})
	})
	mux.HandleFunc("POST /api/config", func(w http.ResponseWriter, r *http.Request) {
		c, ok := readConfig(w, r)
		if !ok {
			return
		}
		if !opt.Local {
			cur := st.Config()
			c.Wsprd.Path, c.Wsprd.Args = cur.Wsprd.Path, cur.Wsprd.Args
			c.DataDir, c.SpotsFile, c.Web.Listen = cur.DataDir, cur.SpotsFile, cur.Web.Listen
		}
		if err := st.Apply(c); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"config": st.Config(), "local": opt.Local})
	})
	// Plan previews the schedule in a config that has not been saved yet.
	mux.HandleFunc("POST /api/plan", func(w http.ResponseWriter, r *http.Request) {
		c, ok := readConfig(w, r)
		if !ok {
			return
		}
		if err := c.Resolve(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n <= 0 || n > 720 {
			n = 30
		}
		writeJSON(w, http.StatusOK, c.Plan(time.Now(), n))
	})
	mux.HandleFunc("GET /api/bands", func(w http.ResponseWriter, r *http.Request) {
		type band struct {
			Name   string `json:"name"`
			DialHz int64  `json:"dial_hz"`
		}
		out := make([]band, 0, len(config.BandOrder))
		for _, n := range config.BandOrder {
			out = append(out, band{n, config.Bands[n]})
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /api/radios", func(w http.ResponseWriter, r *http.Request) {
		radios, err := flex.DiscoverAll(r.Context(), 3*time.Second)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if radios == nil {
			radios = []flex.RadioInfo{}
		}
		writeJSON(w, http.StatusOK, radios)
	})
	mux.HandleFunc("GET /api/log", func(w http.ResponseWriter, r *http.Request) {
		since, _ := strconv.Atoi(r.URL.Query().Get("since"))
		lines, seq := st.Logs.Since(since)
		if lines == nil {
			lines = []string{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"lines": lines, "seq": seq})
	})
	h := noCache(mux)
	if !opt.Local {
		h = sameOrigin(h)
	}
	return h
}

// sameOrigin refuses changes sent by other web sites: a page elsewhere could
// otherwise post to http://localhost:8080 from the user's browser.
func sameOrigin(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				http.Error(w, "send JSON", http.StatusUnsupportedMediaType)
				return
			}
			if o := r.Header.Get("Origin"); o != "" {
				u, err := url.Parse(o)
				if err != nil || u.Host != r.Host {
					http.Error(w, "cross-site request refused", http.StatusForbidden)
					return
				}
			}
		}
		h.ServeHTTP(w, r)
	})
}

func readConfig(w http.ResponseWriter, r *http.Request) (*config.Config, bool) {
	c := config.Default()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad settings: " + err.Error()})
		return nil, false
	}
	return c, true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

// Server is the web page served over HTTP. It moves to a new address when
// web.listen changes in the settings. Pages served this way get the remote
// Options: they cannot change program paths.
type Server struct {
	st   *app.Station
	mu   sync.Mutex
	addr string
	srv  *http.Server
}

// Start serves the page on addr ("off" or "" serves nothing). With follow,
// it moves when web.listen changes in the settings.
func Start(addr string, st *app.Station, follow bool) *Server {
	s := &Server{st: st}
	s.Set(addr)
	if follow {
		st.OnConfig(func(c *config.Config) { s.Set(c.Web.Listen) })
	}
	return s
}

// Set moves the page to addr. On failure (say the port is taken) the
// page stays off and the error is logged.
func (s *Server) Set(addr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if addr == s.addr && (s.srv != nil || addr == "" || addr == "off") {
		return nil
	}
	if s.srv != nil {
		s.srv.Close()
		s.srv = nil
	}
	s.addr = addr
	if addr == "" || addr == "off" {
		s.st.Log().Info("web page off")
		return nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.st.Log().Error("web page", "addr", addr, "err", err)
		return err
	}
	srv := &http.Server{Handler: Handler(s.st, Options{}), ReadHeaderTimeout: 10 * time.Second}
	s.srv = srv
	s.st.Log().Info("web page ready", "url", PageURL(ln.Addr()))
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.st.Log().Error("web page", "err", err)
		}
	}()
	return nil
}

// Close stops the page.
func (s *Server) Close() { s.Set("off") }

// PageURL is the address to type in a browser.
func PageURL(a net.Addr) string {
	ta, ok := a.(*net.TCPAddr)
	if !ok {
		return "http://" + a.String() + "/"
	}
	host := ta.IP.String()
	if ta.IP.IsUnspecified() {
		host, _ = os.Hostname()
		if host == "" {
			host = "localhost"
		}
	}
	return fmt.Sprintf("http://%s/", net.JoinHostPort(host, fmt.Sprint(ta.Port)))
}
