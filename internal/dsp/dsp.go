// Package dsp turns the radio's DAX IQ stream into the 375 sample/s complex
// baseband that wsprd decodes.
package dsp

import (
	"fmt"
	"math"
)

// WSPRRate is the sample rate wsprd expects in a .c2 file.
const WSPRRate = 375

// NCO shifts a complex signal down by a frequency (positive freq moves a
// signal at +freq to 0 Hz).
type NCO struct {
	phase, step float64
}

// SetFreq sets the shift in Hz at sample rate fs.
func (n *NCO) SetFreq(freq float64, fs int) { n.step = -2 * math.Pi * freq / float64(fs) }

// Mix shifts x in place.
func (n *NCO) Mix(x []complex64) {
	if n.step == 0 {
		return
	}
	for i := range x {
		s, c := math.Sincos(n.phase)
		x[i] *= complex(float32(c), float32(s))
		n.phase += n.step
		if n.phase > math.Pi {
			n.phase -= 2 * math.Pi
		} else if n.phase < -math.Pi {
			n.phase += 2 * math.Pi
		}
	}
}

// firDecim is one low-pass filter and decimate-by-M stage.
type firDecim struct {
	taps  []float32
	m     int
	hist  []complex64 // last len(taps)-1 inputs, then the new block
	phase int         // inputs consumed since the last output
}

func (f *firDecim) process(in []complex64, out []complex64) []complex64 {
	n := len(f.taps)
	f.hist = append(f.hist, in...)
	i := 0
	for ; i+n <= len(f.hist); i++ {
		if f.phase == 0 {
			var acc complex64
			w := f.hist[i : i+n]
			for k, t := range f.taps {
				acc += w[k] * complex(t, 0)
			}
			out = append(out, acc)
		}
		f.phase++
		if f.phase == f.m {
			f.phase = 0
		}
	}
	f.hist = append(f.hist[:0], f.hist[i:]...)
	return out
}

// Decimator low-pass filters and decimates from the DAX IQ rate to 375 Hz.
// The passband is about +-170 Hz, enough for wsprd's +-150 Hz search.
type Decimator struct {
	stages []*firDecim
	bufs   [][]complex64
	delay  float64 // group delay in seconds
}

// NewDecimator builds a decimator from fs (24000, 48000, 96000 or 192000).
func NewDecimator(fs int) (*Decimator, error) {
	if fs%WSPRRate != 0 {
		return nil, fmt.Errorf("rate %d is not a multiple of %d", fs, WSPRRate)
	}
	d := fs / WSPRRate
	var ms []int
	for d > 4 {
		if d%4 == 0 && d/4 >= 4 {
			ms = append(ms, 4)
			d /= 4
		} else if d%2 == 0 {
			ms = append(ms, 2)
			d /= 2
		} else {
			return nil, fmt.Errorf("cannot decimate %d to %d", fs, WSPRRate)
		}
	}
	if d != 4 {
		return nil, fmt.Errorf("cannot decimate %d to %d", fs, WSPRRate)
	}
	ms = append(ms, 4)

	dec := &Decimator{}
	rate := float64(fs)
	for i, m := range ms {
		outRate := rate / float64(m)
		var cutoff, ntaps float64
		if i == len(ms)-1 {
			// Final stage: flat to 170 Hz, stop by 187.5 Hz (Nyquist of 375).
			cutoff = 179
			ntaps = 6.6 * rate / 17 // Blackman: transition ~5.5/N of fs
		} else {
			// Earlier stages only protect the final 0..200 Hz from aliasing.
			stop := outRate - 200
			cutoff = (200 + stop) / 2
			ntaps = 6.6 * rate / (stop - 200)
		}
		n := int(ntaps) | 1
		if n < 15 {
			n = 15
		}
		// Start with a history of zeros so every input yields its output.
		dec.stages = append(dec.stages, &firDecim{taps: lowpass(n, cutoff/rate), m: m, hist: make([]complex64, n-1)})
		dec.delay += float64(n-1) / 2 / rate
		dec.bufs = append(dec.bufs, nil)
		rate = outRate
	}
	return dec, nil
}

// Delay is the filter's group delay in seconds: an output sample shows the
// input from this long before it was produced.
func (d *Decimator) Delay() float64 { return d.delay }

// Process filters a block and returns the 375 Hz output. The returned slice
// is reused on the next call.
func (d *Decimator) Process(in []complex64) []complex64 {
	x := in
	for i, s := range d.stages {
		d.bufs[i] = s.process(x, d.bufs[i][:0])
		x = d.bufs[i]
	}
	return x
}

// lowpass designs a Blackman-windowed sinc with unity DC gain. fc is the
// cutoff as a fraction of the sample rate.
func lowpass(n int, fc float64) []float32 {
	h := make([]float64, n)
	mid := float64(n-1) / 2
	var sum float64
	for i := range h {
		x := float64(i) - mid
		s := 2 * fc
		if x != 0 {
			s = math.Sin(2*math.Pi*fc*x) / (math.Pi * x)
		}
		w := 0.42 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1)) + 0.08*math.Cos(4*math.Pi*float64(i)/float64(n-1))
		h[i] = s * w
		sum += h[i]
	}
	out := make([]float32, n)
	for i := range h {
		out[i] = float32(h[i] / sum)
	}
	return out
}
