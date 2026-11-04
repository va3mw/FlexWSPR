package wspr

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestC2RoundTripAndLayout(t *testing.T) {
	start := time.Date(2026, 11, 3, 14, 6, 0, 0, time.UTC)
	if got := C2Name(start); got != "261103_1406.c2" || len(got) != 14 {
		t.Fatalf("C2Name = %q", got)
	}
	iq := make([]complex64, 1000)
	for i := range iq {
		iq[i] = complex(float32(i), float32(-2*i))
	}
	path := filepath.Join(t.TempDir(), C2Name(start))
	if err := WriteC2(path, start, 14095600, iq, false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if len(b) != 14+4+8+C2Samples*8 {
		t.Fatalf("size %d", len(b))
	}
	le := binary.LittleEndian
	if string(b[:14]) != "261103_1406.c2" || le.Uint32(b[14:]) != 2 {
		t.Fatalf("bad header %q %d", b[:14], le.Uint32(b[14:]))
	}
	if mhz := math.Float64frombits(le.Uint64(b[18:])); mhz != 14.0956 {
		t.Fatalf("dial %v", mhz)
	}
	// Q is stored negated: sample 3 is (3, -6) -> stored (3, 6).
	if q := math.Float32frombits(le.Uint32(b[26+3*8+4:])); q != 6 {
		t.Fatalf("stored Q %v, want 6", q)
	}
	name, dial, back, err := ReadC2(path, false)
	if err != nil || name != "261103_1406.c2" || dial != 14095600 {
		t.Fatal(name, dial, err)
	}
	if back[3] != iq[3] || back[999] != iq[999] || back[1000] != 0 || back[C2Samples-1] != 0 {
		t.Fatalf("round trip mismatch: %v %v %v", back[3], back[999], back[1000])
	}
}

func TestParseWsprd(t *testing.T) {
	out := `0104 -21  0.3  14.097063  0  K1ABC FN42 37
0104  -9 -0.8  14.097112 -1  PJ4/K1ABC 37
0104 -27  1.2  14.097150  0  <K1ABC> FN42AB 37
0104 -27  1.2  14.097150  0  <K1ABC> FN42AB 37
0104 -25  0.3  14.097101 -1  <...> DO43XH 30
some noise line
<DecodeFinished>
`
	start := time.Date(2026, 11, 3, 1, 4, 0, 0, time.UTC)
	s := ParseWsprd(out, start)
	if len(s) != 4 {
		t.Fatalf("got %d spots: %+v", len(s), s)
	}
	if s[0].Call != "K1ABC" || s[0].Grid != "FN42" || s[0].DBm != 37 || s[0].SNR != -21 || s[0].FreqHz != 14097063 {
		t.Errorf("type 1: %+v", s[0])
	}
	if s[1].Call != "PJ4/K1ABC" || s[1].Grid != "" || s[1].DBm != 37 || s[1].Drift != -1 || s[1].DT != -0.8 {
		t.Errorf("type 2: %+v", s[1])
	}
	if s[2].Call != "K1ABC" || s[2].Grid != "FN42AB" {
		t.Errorf("type 3: %+v", s[2])
	}
	if s[3].Call != "" || s[3].Grid != "DO43XH" || !strings.Contains(s[3].Format(), "<...>") {
		t.Errorf("unresolved hash: %+v", s[3])
	}
	if got := s[0].Format(); got != "261103 0104  -21   0.3   14.097063 K1ABC        FN42    37   0" {
		t.Errorf("Format = %q", got)
	}
}

func TestCaptureAlignsToEvenMinute(t *testing.T) {
	var got []Cycle
	done := make(chan Cycle, 4)
	c := NewCapture(func(cy Cycle) { done <- cy })
	block := make([]complex64, 15) // 40 ms at 375 Hz
	// Start feeding at 14:05:30 (odd minute): nothing until 14:06:00.
	t0 := time.Date(2026, 11, 3, 14, 5, 30, 0, time.UTC)
	step := 40 * time.Millisecond
	now := t0
	for now.Before(t0.Add(30*time.Second + 116*time.Second)) {
		now = now.Add(step)
		c.Feed(block, 0, now)
	}
	select {
	case cy := <-done:
		got = append(got, cy)
	case <-time.After(time.Second):
		t.Fatal("no cycle")
	}
	cy := got[0]
	if want := time.Date(2026, 11, 3, 14, 6, 0, 0, time.UTC); !cy.Start.Equal(want) {
		t.Fatalf("cycle start %v, want %v", cy.Start, want)
	}
	if len(cy.Samples) != captureSamples {
		t.Fatalf("%d samples, want %d", len(cy.Samples), captureSamples)
	}
}

func TestCaptureLateStartPadsZeros(t *testing.T) {
	done := make(chan Cycle, 1)
	c := NewCapture(func(cy Cycle) { done <- cy })
	one := []complex64{1}
	// First data 0.5 s after the even minute: 0.5 s of zeros go in front.
	now := time.Date(2026, 11, 3, 14, 6, 0, 500_000_000, time.UTC)
	for i := 0; i < captureSamples; i++ {
		c.Feed(one, 0, now)
		now = now.Add(time.Second / rate)
	}
	cy := <-done
	lead := 0
	for lead < len(cy.Samples) && cy.Samples[lead] == 0 {
		lead++
	}
	if lead < rate/2-2 || lead > rate/2+2 {
		t.Fatalf("%d leading zeros, want about %d", lead, rate/2)
	}
}
