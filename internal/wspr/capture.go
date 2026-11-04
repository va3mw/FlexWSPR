package wspr

import (
	"time"
)

const (
	rate = 375
	// CaptureSeconds is how much of each cycle is recorded. A transmission
	// starts 1 s into the even minute and lasts 110.6 s; 114 s matches
	// WSJT-X and leaves the rest of the cycle for decoding.
	CaptureSeconds = 114
	captureSamples = CaptureSeconds * rate
)

// Cycle is one recorded WSPR cycle.
type Cycle struct {
	Start   time.Time   // the even minute, UTC
	Samples []complex64 // 375 Hz baseband, starting at Start
	Gaps    int         // samples filled in for lost packets
	DialHz  int64       // dial frequency the cycle was recorded on
}

// Capture cuts the 375 Hz stream into cycles that start on even UTC minutes,
// using the system clock (keep it synced with chrony/NTP).
type Capture struct {
	// Dial, when set, gives the dial frequency for a cycle as it starts.
	Dial    func() int64
	onCycle func(Cycle)
	active  bool
	cur     Cycle
	lastAt  time.Time
}

// NewCapture calls onCycle (in its own goroutine) for each complete cycle.
func NewCapture(onCycle func(Cycle)) *Capture { return &Capture{onCycle: onCycle} }

// Feed adds samples that ended arriving at now. gap is how many of them are
// zeros standing in for lost packets.
func (c *Capture) Feed(s []complex64, gap int, now time.Time) {
	now = now.UTC()
	// A stall of more than 2 s ruins the cycle's timing: drop it.
	if c.active && !c.lastAt.IsZero() && now.Sub(c.lastAt) > 2*time.Second {
		c.active = false
	}
	c.lastAt = now
	if !c.active {
		start := now.Truncate(2 * time.Minute)
		late := now.Sub(start) - time.Duration(len(s))*time.Second/rate
		if late < 0 || late > time.Second {
			return // wait for the next even minute
		}
		c.active = true
		c.cur = Cycle{Start: start, Samples: make([]complex64, int(late.Seconds()*rate), captureSamples+rate)}
		if c.Dial != nil {
			c.cur.DialHz = c.Dial()
		}
	}
	c.cur.Samples = append(c.cur.Samples, s...)
	c.cur.Gaps += gap
	if len(c.cur.Samples) >= captureSamples {
		cy := c.cur
		cy.Samples = cy.Samples[:captureSamples]
		c.active = false
		go c.onCycle(cy)
	}
}
