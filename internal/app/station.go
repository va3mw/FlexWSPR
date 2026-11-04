package app

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/va3mw/FlexWSPR/internal/config"
	"github.com/va3mw/FlexWSPR/internal/upload"
)

// Station runs the receiver and restarts it when the configuration changes.
// Both GUIs and the headless program drive it.
type Station struct {
	Version string
	Logs    *LogRing

	path  string
	mon   monitor
	mu    sync.Mutex
	cfg   *config.Config
	saved bool
	apply chan struct{}
	onCfg []func(*config.Config)
	up    *upload.Uploader
	level *slog.LevelVar
	log   *slog.Logger
}

// NewStation returns a station for the config at path. saved is false when
// the file does not exist yet and cfg holds the defaults. Log lines go to out
// (and to the ring the GUI shows).
func NewStation(path string, cfg *config.Config, saved bool, version string, out io.Writer) *Station {
	s := &Station{Version: version, Logs: NewLogRing(400), path: path, cfg: cfg, saved: saved,
		apply: make(chan struct{}, 1), level: new(slog.LevelVar)}
	s.setLevel(cfg)
	s.log = slog.New(slog.NewTextHandler(io.MultiWriter(out, s.Logs), &slog.HandlerOptions{Level: s.level}))
	s.mon.st = Status{Version: version, State: StateStopped, ConfigPath: path, ConfigSaved: saved}
	return s
}

func (s *Station) setLevel(c *config.Config) {
	if c.Debug {
		s.level.Set(slog.LevelDebug)
	} else {
		s.level.Set(slog.LevelInfo)
	}
}

// Log is the station's logger.
func (s *Station) Log() *slog.Logger { return s.log }

// Config returns a copy of the current configuration.
func (s *Station) Config() *config.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Clone()
}

// Status returns a snapshot for the GUI.
func (s *Station) Status() Status {
	st := s.mon.snapshot()
	s.mu.Lock()
	if s.up != nil {
		st.Upload, st.UploadQueue, st.Uploaded = true, s.up.Len(), s.up.Sent()
	}
	st.Next = s.cfg.Plan(time.Now(), 8)
	st.ConfigSaved = s.saved
	s.mu.Unlock()
	return st
}

// Apply checks and saves a new configuration, then restarts the receiver
// with it.
func (s *Station) Apply(c *config.Config) error {
	c = c.Clone()
	if err := c.Resolve(); err != nil {
		return err
	}
	if err := c.Save(s.path); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg, s.saved = c, true
	hooks := append([]func(*config.Config){}, s.onCfg...)
	s.mu.Unlock()
	s.setLevel(c)
	for _, fn := range hooks {
		fn(c.Clone())
	}
	s.log.Info("configuration saved; restarting", "file", s.path)
	select {
	case s.apply <- struct{}{}:
	default:
	}
	return nil
}

// Run runs the receiver until ctx is cancelled, restarting it after Apply.
func (s *Station) Run(ctx context.Context) {
	for {
		cfg := s.Config()
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.runOnce(runCtx, cfg)
		}()
		select {
		case <-ctx.Done():
			cancel()
			<-done
			s.mon.state(StateStopped, "")
			return
		case <-s.apply:
			cancel()
			<-done // lets the radio cleanup commands go out
		}
	}
}

func (s *Station) runOnce(ctx context.Context, cfg *config.Config) {
	if !cfg.Ready() {
		s.mon.state(StateSetup, "Enter your callsign and grid in Settings to start.")
		s.log.Info("waiting for setup: callsign and grid are not set", "config", s.path)
		<-ctx.Done()
		return
	}
	s.log.Info("FlexWSPR starting", "version", s.Version, "config", s.path, "callsign", cfg.Callsign, "grid", cfg.Grid,
		"band", cfg.Band, "schedule", cfg.Schedule.Mode, "wsprd", cfg.Wsprd.Path, "data_dir", cfg.DataDir)
	if _, err := os.Stat(cfg.Wsprd.Path); err != nil && !isOnPath(cfg.Wsprd.Path) {
		s.log.Warn("wsprd not found; install WSJT-X or set its path in Settings", "path", cfg.Wsprd.Path)
	}
	a, err := newApp(cfg, s.log, s.Version, &s.mon)
	if err != nil {
		s.mon.state(StateError, err.Error())
		s.log.Error("start", "err", err)
		<-ctx.Done()
		return
	}
	s.mu.Lock()
	s.up = a.up
	s.mu.Unlock()
	hz, _ := cfg.BandAt(nextCycle(time.Now()))
	a.showBand(hz)
	a.Run(ctx)
	s.log.Info("FlexWSPR stopped")
}

// OnConfig registers fn to be called with each newly applied configuration.
func (s *Station) OnConfig(fn func(*config.Config)) {
	s.mu.Lock()
	s.onCfg = append(s.onCfg, fn)
	s.mu.Unlock()
}
