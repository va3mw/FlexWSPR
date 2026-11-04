package app

import (
	"bytes"
	"os/exec"
	"sync"
)

// LogRing keeps the last log lines for the GUI. It is an io.Writer.
type LogRing struct {
	mu    sync.Mutex
	lines []string
	max   int
	seq   int // number of the newest line
	part  []byte
}

func NewLogRing(max int) *LogRing { return &LogRing{max: max} }

func (r *LogRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.part = append(r.part, p...)
	for {
		i := bytes.IndexByte(r.part, '\n')
		if i < 0 {
			break
		}
		r.lines = append(r.lines, string(r.part[:i]))
		r.part = r.part[i+1:]
		r.seq++
		if len(r.lines) > r.max {
			r.lines = r.lines[len(r.lines)-r.max:]
		}
	}
	return len(p), nil
}

// Since returns the lines after line number seq and the newest number.
func (r *LogRing) Since(seq int) ([]string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.seq - seq
	if n <= 0 {
		return nil, r.seq
	}
	if n > len(r.lines) {
		n = len(r.lines)
	}
	return append([]string(nil), r.lines[len(r.lines)-n:]...), r.seq
}

func isOnPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
