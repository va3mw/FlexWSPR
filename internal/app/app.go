// Package app runs the station: connect to the radio, open a DAX IQ stream on
// a WSPR band, record each two-minute cycle, decode it with wsprd, and move
// to the scheduled band between cycles.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/va3mw/FlexWSPR/internal/config"
	"github.com/va3mw/FlexWSPR/internal/dsp"
	"github.com/va3mw/FlexWSPR/internal/flex"
	"github.com/va3mw/FlexWSPR/internal/upload"
	"github.com/va3mw/FlexWSPR/internal/wspr"
)

// WSPR signals sit 1400-1600 Hz above the USB dial; the IQ is centred here.
const audioCentreHz = 1500

// App is one run of the station with a fixed configuration.
type App struct {
	cfg *config.Config
	log *slog.Logger
	dec *wspr.Decoder
	up  *upload.Uploader // nil when uploading is off
	mon *monitor

	decoding atomic.Bool
	spotMu   sync.Mutex
}

func newApp(cfg *config.Config, log *slog.Logger, version string, mon *monitor) (*App, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	a := &App{cfg: cfg, log: log, mon: mon,
		dec: &wspr.Decoder{Path: cfg.Wsprd.Path, Args: cfg.Wsprd.Args, DataDir: cfg.DataDir}}
	if cfg.Upload.Enabled {
		a.up = upload.New(cfg.Callsign, cfg.Grid, "FlexWSPR-"+version,
			filepath.Join(cfg.DataDir, "upload_queue.json"), log.With("src", "wsprnet"))
		a.up.URL = cfg.Upload.URL
		a.up.Load()
	}
	return a, nil
}

// Run connects and reconnects until ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	if a.up != nil {
		a.log.Info("uploading spots to wsprnet.org", "url", a.cfg.Upload.URL)
		done := make(chan struct{})
		go func() { a.up.Run(ctx); close(done) }()
		defer func() { <-done }() // free the queue file for the next run
	}
	for {
		err := a.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		a.mon.state(StateError, errText(err))
		a.log.Warn("session ended; retrying in 5 s", "err", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Second):
		}
	}
}

func (a *App) findRadio(ctx context.Context) (string, error) {
	if a.cfg.Radio.IP != "" {
		a.mon.set(func(s *Status) { s.Radio = a.cfg.Radio.IP })
		return a.cfg.Radio.IP, nil
	}
	a.mon.state(StateSearching, "")
	a.log.Info("looking for a radio on the LAN", "serial", a.cfg.Radio.Serial)
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	info, err := flex.Discover(dctx, a.cfg.Radio.Serial)
	if err != nil {
		return "", fmt.Errorf("no radio found by discovery (set the radio's IP address to skip it): %w", err)
	}
	a.log.Info("found radio", "model", info.Model, "nickname", info.Nickname, "serial", info.Serial,
		"ip", info.IP, "version", info.Version, "status", info.Status)
	a.mon.set(func(s *Status) { s.Radio = fmt.Sprintf("%s %s (%s)", info.Model, info.Nickname, info.IP) })
	return info.IP, nil
}

// session is one connection: set up, stream, decode, tear down.
func (a *App) session(ctx context.Context) error {
	ip, err := a.findRadio(ctx)
	if err != nil {
		return err
	}
	a.mon.state(StateConnecting, "")
	r, err := flex.Dial(ip, a.log.With("src", "radio"))
	if err != nil {
		return err
	}
	defer r.Close()

	udp, port, err := openUDP()
	if err != nil {
		return err
	}
	defer udp.Close()

	cmd := func(c string) (string, error) { return r.Command(c) }
	must := func(c string) (string, error) {
		rep, err := cmd(c)
		if err != nil {
			a.log.Error("command failed", "cmd", c, "err", err)
		}
		return rep, err
	}

	// We are the only client the station needs, so connect as a GUI client:
	// only GUI clients can own panadapters and slices.
	cmd("client program FlexWSPR")
	cmd("client station " + flex.Escape("FlexWSPR "+a.cfg.Callsign))
	if _, err := must("client gui"); err != nil {
		return fmt.Errorf("the radio would not accept us as a GUI client (is another client using all its GUI connections?): %w", err)
	}
	if _, err := must(fmt.Sprintf("client udpport %d", port)); err != nil {
		return err
	}
	cmd("sub pan all")
	cmd("sub slice all")

	// The radio may restore a panadapter for a GUI client; start clean.
	time.Sleep(1500 * time.Millisecond)
	for _, p := range r.OurPans() {
		a.log.Info("removing leftover panadapter", "pan", fmt.Sprintf("0x%08X", p.ID))
		cmd(fmt.Sprintf("display pan remove 0x%08X", p.ID))
	}

	// The pan's centre is the IQ stream's 0 Hz. Track what the radio reports
	// and shift the rest in software.
	var (
		panCentre, wantCentre, curDial atomic.Int64
		panIDa, sidA                   atomic.Uint32
	)
	r.OnPan(func(p flex.Pan) {
		if p.ID != panIDa.Load() {
			return
		}
		if p.CenterHz != 0 && panCentre.Swap(p.CenterHz) != p.CenterHz {
			a.log.Debug("panadapter centre", "MHz", float64(p.CenterHz)/1e6)
		}
		a.mon.set(func(s *Status) {
			s.RxAnt = p.RxAnt
			if len(p.AntList) > 0 {
				s.Antennas = p.AntList
			}
		})
	})

	// A band is a panadapter, a slice and a DAX IQ stream. Changing band
	// builds a fresh set: retuning the existing panadapter left the DAX IQ
	// stream carrying noise on the new band (FLEX-6600, v4.3.16).
	type rig struct{ pan, stream string }
	var cur *rig
	closeRig := func() {
		if cur == nil {
			return
		}
		if cur.stream != "" {
			cmd("stream remove " + cur.stream)
		}
		cmd("display pan remove " + cur.pan) // takes its slice with it
		a.log.Debug("removed panadapter", "pan", cur.pan)
		cur = nil
	}
	defer func() {
		if cur != nil {
			pan := cur.pan
			closeRig()
			a.log.Info("removed panadapter", "pan", pan)
		}
	}()
	openRig := func(dial int64) error {
		centre := dial + audioCentreHz
		rep, err := must("display panafall create x=100 y=100")
		if err != nil {
			return err
		}
		panID, err := flex.FirstHex(rep)
		if err != nil {
			// Some firmware answers with an empty reply; the status names it.
			time.Sleep(time.Second)
			if ps := r.OurPans(); len(ps) > 0 {
				panID, err = ps[0].ID, nil
			}
		}
		if err != nil {
			return fmt.Errorf("panadapter: %w", err)
		}
		cur = &rig{pan: fmt.Sprintf("0x%08X", panID)}
		pan := cur.pan
		panIDa.Store(panID)
		if _, err := must(fmt.Sprintf("display pan set %s center=%.6f", pan, float64(centre)/1e6)); err != nil {
			return err
		}
		panCentre.Store(centre) // a later status still corrects it
		wantCentre.Store(centre)
		curDial.Store(dial)
		ant := a.cfg.AntennaFor(dial)
		if ant != "" {
			must(fmt.Sprintf("display pan set %s rxant=%s", pan, ant))
		}
		if _, err := must(fmt.Sprintf("display pan set %s daxiq_channel=%d", pan, a.cfg.DAXIQ.Channel)); err != nil {
			return err
		}
		if a.cfg.CreateSlice {
			c := fmt.Sprintf("slice create pan=%s freq=%.6f mode=DIGU", pan, float64(dial)/1e6)
			if ant != "" {
				c += " rxant=" + ant
			}
			if idx, err := must(c); err == nil {
				cmd(fmt.Sprintf("slice set %s digu_offset=0 rit_on=0", strings.TrimSpace(idx)))
			}
		}
		rep, err = must(fmt.Sprintf("stream create type=dax_iq daxiq_channel=%d", a.cfg.DAXIQ.Channel))
		if err != nil {
			return err
		}
		sid, err := flex.FirstHex(rep)
		if err != nil {
			return fmt.Errorf("DAX IQ stream: %w", err)
		}
		cur.stream = fmt.Sprintf("0x%08X", sid)
		sidA.Store(sid)
		must(fmt.Sprintf("stream set %s daxiq_rate=%d", cur.stream, a.cfg.DAXIQ.Rate))
		a.log.Info("receiving", "band", config.BandLabel(dial), "dial_MHz", float64(dial)/1e6, "rxant", ant,
			"pan", pan, "stream", cur.stream)
		a.showBand(dial)
		return nil
	}

	// Start on the band scheduled for the next cycle.
	dial, _ := a.cfg.BandAt(nextCycle(time.Now()))
	if err := openRig(dial); err != nil {
		return err
	}
	a.mon.state(StateRunning, "")

	// Change band in the gap after a capture ends (114 s), while the last
	// cycle decodes, and before the next even minute.
	rigErr := make(chan error, 1)
	sctx, stop := context.WithCancel(ctx)
	tunerDone := make(chan struct{})
	defer func() { stop(); <-tunerDone }() // before the final cleanup above
	go func() {
		defer close(tunerDone)
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		var planned time.Time
		for {
			select {
			case <-sctx.Done():
				return
			case now := <-t.C:
				next := nextCycle(now)
				if left := next.Sub(now); left > 5800*time.Millisecond || left < 2*time.Second || next.Equal(planned) {
					continue
				}
				planned = next
				hz, _ := a.cfg.BandAt(next)
				if hz == curDial.Load() {
					continue
				}
				t0 := time.Now()
				closeRig()
				if err := openRig(hz); err != nil {
					rigErr <- fmt.Errorf("band change to %s: %w", config.BandLabel(hz), err)
					return
				}
				a.log.Info("band change", "band", config.BandLabel(hz), "took", time.Since(t0).Round(10*time.Millisecond))
			}
		}
	}()

	capture := wspr.NewCapture(func(cy wspr.Cycle) { a.decodeCycle(ctx, cy) })
	capture.Dial = curDial.Load
	iqErr := make(chan error, 1)
	go func() { iqErr <- a.receiveIQ(udp, net.ParseIP(ip), &sidA, &wantCentre, &panCentre, capture) }()

	select {
	case <-ctx.Done():
		return nil
	case <-r.Done():
		return r.Err()
	case err := <-iqErr:
		return err
	case err := <-rigErr:
		return err
	}
}

// nextCycle is the first even UTC minute after t.
func nextCycle(t time.Time) time.Time { return t.UTC().Truncate(2 * time.Minute).Add(2 * time.Minute) }

// showBand shows the band the radio is on.
func (a *App) showBand(hz int64) {
	a.mon.set(func(s *Status) { s.DialHz, s.Band = hz, config.BandLabel(hz) })
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// receiveIQ reads DAX IQ packets for our stream, shifts them so the wanted
// centre is at 0 Hz, decimates them to 375 Hz and feeds the capture.
func (a *App) receiveIQ(c *net.UDPConn, radio net.IP, sid *atomic.Uint32, wantCentre, panCentre *atomic.Int64, capture *wspr.Capture) error {
	var (
		dec     *dsp.Decimator
		nco     dsp.NCO
		fs      int
		ncoAt   int64 = -1
		ncoWant int64 = -1
		last          = -1
		perPkt  int
		iq      []complex64
		buf     = make([]byte, 16384)
		lostTot int
		got     bool
	)
	for {
		c.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, src, err := c.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return fmt.Errorf("no DAX IQ data for 10 s")
			}
			return err
		}
		if radio != nil && !src.IP.Equal(radio) {
			continue
		}
		p, err := flex.ParseVITA(buf[:n])
		if err != nil || p.StreamID != sid.Load() {
			continue
		}
		rate := flex.IQRate(p.PacketClass)
		if rate == 0 {
			continue
		}
		if rate != fs {
			if dec, err = dsp.NewDecimator(rate); err != nil {
				return err
			}
			fs, ncoAt, last = rate, -1, -1
			if got {
				a.log.Debug("DAX IQ rate changed", "rate", rate)
			}
		}
		if !got {
			got = true
			a.log.Info("DAX IQ data arriving", "rate", rate, "samples_per_packet", len(p.Payload)/8)
		}
		if pc, w := panCentre.Load(), wantCentre.Load(); pc != ncoAt || w != ncoWant {
			nco.SetFreq(float64(w-pc), fs)
			ncoAt, ncoWant = pc, w
		}

		// Stand zeros in for lost packets so the cycle keeps its timing.
		lost := 0
		if last >= 0 {
			lost = (int(p.Count) - last - 1 + 16) % 16
		}
		last = int(p.Count)
		iq = iq[:0]
		if lost > 0 && perPkt > 0 {
			lostTot += lost
			iq = append(iq, make([]complex64, lost*perPkt)...)
			a.log.Debug("lost DAX IQ packets", "count", lost, "total", lostTot)
		}
		gap := len(iq)
		samples := flex.IQSamples(nil, p.Payload, a.cfg.DAXIQ.SwapIQ)
		perPkt = len(samples)
		iq = append(iq, samples...)
		nco.Mix(iq)
		out := dec.Process(iq)
		if len(out) > 0 {
			// Date the samples by when they entered the filter.
			at := time.Now().Add(-time.Duration(dec.Delay() * float64(time.Second)))
			capture.Feed(out, gap*len(out)/len(iq), at)
		}
	}
}

// decodeCycle writes a cycle to .c2, runs wsprd and records the spots.
func (a *App) decodeCycle(ctx context.Context, cy wspr.Cycle) {
	if !a.decoding.CompareAndSwap(false, true) {
		a.log.Warn("previous decode still running; skipping cycle", "cycle", cy.Start.Format("15:04"))
		return
	}
	defer a.decoding.Store(false)
	dial := cy.DialHz
	if dial == 0 {
		dial = a.cfg.DialHz
	}
	info := CycleInfo{Start: cy.Start, Band: config.BandLabel(dial), DialHz: dial, Gaps: cy.Gaps}
	path := filepath.Join(a.cfg.DataDir, wspr.C2Name(cy.Start))
	if err := wspr.WriteC2(path, cy.Start, dial, cy.Samples, a.cfg.Wsprd.FlipC2); err != nil {
		a.log.Error("write .c2", "err", err)
		info.Error = err.Error()
		a.mon.cycle(info, nil, a.cfg.Grid)
		return
	}
	if !a.cfg.KeepC2 {
		defer os.Remove(path)
	}
	t0 := time.Now()
	spots, err := a.dec.Decode(ctx, path, cy.Start)
	if err != nil {
		if ctx.Err() != nil {
			return // stopping
		}
		a.log.Error("wsprd", "err", err)
		info.Error = err.Error()
		a.mon.cycle(info, nil, a.cfg.Grid)
		return
	}
	info.Spots = len(spots)
	a.mon.cycle(info, spots, a.cfg.Grid)
	a.log.Info("cycle decoded", "cycle", cy.Start.Format("15:04"), "band", info.Band, "spots", len(spots),
		"took", time.Since(t0).Round(100*time.Millisecond), "gap_samples", cy.Gaps)
	if a.up != nil {
		a.up.Cycle(cy.Start, dial, spots)
	}
	if len(spots) == 0 {
		return
	}
	a.spotMu.Lock()
	defer a.spotMu.Unlock()
	f, err := os.OpenFile(a.cfg.SpotsFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		a.log.Error("spot log", "err", err)
	}
	for _, s := range spots {
		a.log.Info("spot", "call", s.Call, "grid", s.Grid, "snr", s.SNR, "dt", s.DT,
			"MHz", fmt.Sprintf("%.6f", float64(s.FreqHz)/1e6), "dBm", s.DBm, "drift", s.Drift)
		if f != nil {
			fmt.Fprintln(f, s.Format())
		}
	}
	if f != nil {
		f.Close()
	}
}

// openUDP binds the local VITA socket, starting at 4991 like FlexLib.
func openUDP() (*net.UDPConn, int, error) {
	var lastErr error
	for port := 4991; port <= 4991+40; port++ {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
		if err == nil {
			c.SetReadBuffer(1 << 21)
			return c, port, nil
		}
		lastErr = err
	}
	return nil, 0, fmt.Errorf("no free UDP port for the DAX IQ stream: %w", lastErr)
}
