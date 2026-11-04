package dsp

import (
	"math"
	"math/cmplx"
	"testing"
)

// tone returns n samples of exp(j2πft) at fs.
func tone(f float64, fs, n int) []complex64 {
	x := make([]complex64, n)
	for i := range x {
		x[i] = complex64(cmplx.Exp(complex(0, 2*math.Pi*f*float64(i)/float64(fs))))
	}
	return x
}

// powerAt measures the output power at frequency f (Hz, at 375 Hz) after
// dropping the filter's start-up transient.
func powerAt(y []complex64, f float64) (atF, total float64) {
	y = y[len(y)/4:]
	var acc complex128
	for i, v := range y {
		acc += complex128(v) * cmplx.Exp(complex(0, -2*math.Pi*f*float64(i)/WSPRRate))
		total += real(complex128(v) * cmplx.Conj(complex128(v)))
	}
	atF = cmplx.Abs(acc) / float64(len(y))
	return atF * atF, total / float64(len(y))
}

func run(t *testing.T, fs int, f float64) []complex64 {
	t.Helper()
	d, err := NewDecimator(fs)
	if err != nil {
		t.Fatal(err)
	}
	x := tone(f, fs, fs*4) // 4 s
	var y []complex64
	for i := 0; i < len(x); i += 128 { // packet-sized blocks
		y = append(y, d.Process(x[i:min(i+128, len(x))])...)
	}
	if want := 4 * WSPRRate; len(y) < want-2 || len(y) > want+2 {
		t.Fatalf("fs %d: got %d output samples, want about %d", fs, len(y), want)
	}
	return y
}

func TestDecimatorPassband(t *testing.T) {
	for _, fs := range []int{24000, 48000, 96000, 192000} {
		for _, f := range []float64{0, 100, -100, 150, -150, 165} {
			p, _ := powerAt(run(t, fs, f), f)
			if db := 10 * math.Log10(p); db < -0.5 || db > 0.5 {
				t.Errorf("fs %d tone %+.0f Hz: gain %.2f dB, want about 0", fs, f, db)
			}
		}
	}
}

func TestDecimatorStopband(t *testing.T) {
	// Tones outside +-187.5 Hz must not alias into the WSPR window.
	for _, fs := range []int{24000, 48000} {
		for _, f := range []float64{200, -200, 300, 500, 1000, -1400, 3000, 5900} {
			_, total := powerAt(run(t, fs, f), 0)
			if db := 10 * math.Log10(total+1e-30); db > -50 {
				t.Errorf("fs %d tone %+.0f Hz: leaks through at %.1f dB", fs, f, db)
			}
		}
	}
}

func TestNCOShiftsDown(t *testing.T) {
	const fs = 24000
	x := tone(1234, fs, fs)
	var n NCO
	n.SetFreq(1234-50, fs) // 1234 Hz should come out at +50 Hz
	n.Mix(x)
	d, _ := NewDecimator(fs)
	y := d.Process(x)
	p, _ := powerAt(y, 50)
	if db := 10 * math.Log10(p); db < -0.5 {
		t.Errorf("tone not at +50 Hz after mixing: %.2f dB", db)
	}
}
