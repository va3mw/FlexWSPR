package wspr

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Spot is one decode.
type Spot struct {
	Start   time.Time // cycle start (UTC)
	SNR     int       // dB in 2500 Hz
	DT      float64   // s
	FreqHz  int64     // transmitter frequency
	Drift   int       // Hz/min
	Message string    // as wsprd printed it
	Call    string
	Grid    string // may be blank (type 2 messages)
	DBm     int
}

// Decoder runs wsprd on .c2 files.
type Decoder struct {
	Path    string   // wsprd binary
	Args    []string // extra arguments
	DataDir string   // wsprd's -a directory (keeps hashtable.txt between runs)
}

// Decode runs wsprd on one .c2 file and returns its spots.
func (d *Decoder) Decode(ctx context.Context, c2 string, start time.Time) ([]Spot, error) {
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	args := append(append([]string{}, d.Args...), "-a", d.DataDir, c2)
	cmd := exec.CommandContext(ctx, d.Path, args...)
	hideWindow(cmd) // no console window from the desktop app
	cmd.Dir = d.DataDir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", d.Path, strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return ParseWsprd(out.String(), start), nil
}

// ParseWsprd reads wsprd's stdout. Each decode is printed as
//
//	HHMM  SNR   DT       FREQ DRIFT  MESSAGE...
//	0104  -21  0.3  14.097063  0  K1ABC FN42 37
//
// and the run ends with "<DecodeFinished>".
func ParseWsprd(out string, start time.Time) []Spot {
	var spots []Spot
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 7 || len(f[0]) != 4 {
			continue
		}
		if _, err := strconv.Atoi(f[0]); err != nil {
			continue
		}
		snr, err1 := strconv.ParseFloat(f[1], 64)
		dt, err2 := strconv.ParseFloat(f[2], 64)
		mhz, err3 := strconv.ParseFloat(f[3], 64)
		drift, err4 := strconv.Atoi(f[4])
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			continue
		}
		s := Spot{Start: start, SNR: int(snr), DT: dt, FreqHz: int64(mhz*1e6 + 0.5), Drift: drift,
			Message: strings.Join(f[5:], " ")}
		s.Call, s.Grid, s.DBm = parseMessage(f[5:])
		// wsprd can report one signal twice (from two decoding passes).
		key := fmt.Sprintf("%s|%d", s.Message, s.FreqHz/10)
		if seen[key] {
			continue
		}
		seen[key] = true
		spots = append(spots, s)
	}
	return spots
}

// parseMessage splits a WSPR message: "CALL GRID DBM" (type 1),
// "PFX/CALL DBM" (type 2) or "<CALL> GRID6 DBM" (type 3).
func parseMessage(m []string) (call, grid string, dbm int) {
	if len(m) == 0 {
		return
	}
	dbm, _ = strconv.Atoi(m[len(m)-1])
	call = strings.Trim(m[0], "<>")
	if call == "..." {
		call = "" // type 3 message whose callsign hash wsprd could not resolve
	}
	if len(m) >= 3 {
		grid = m[1]
	}
	return
}

// Format renders a spot as a line for the spot log, close to WSJT-X's
// ALL_WSPR.TXT: date time snr dt MHz call grid dBm drift.
func (s Spot) Format() string {
	grid, call := s.Grid, s.Call
	if grid == "" {
		grid = "-"
	}
	if call == "" {
		call = "<...>"
	}
	return fmt.Sprintf("%s %4d %5.1f %11.6f %-12s %-6s %3d %3d",
		s.Start.UTC().Format("060102 1504"), s.SNR, s.DT, float64(s.FreqHz)/1e6, call, grid, s.DBm, s.Drift)
}
