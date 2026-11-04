package flex

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Pan is the state of a panadapter as the radio reports it.
type Pan struct {
	ID           uint32
	ClientHandle uint32
	CenterHz     int64
	DAXIQ        int
	RxAnt        string
	AntList      []string // antennas this panadapter can use
}

// Radio is one TCP connection to the SmartSDR API (port 4992).
type Radio struct {
	log  *slog.Logger
	conn net.Conn
	done chan struct{}
	err  error

	mu      sync.Mutex
	seq     uint32
	pending map[uint32]chan reply
	handle  uint32
	pans    map[uint32]*Pan
	onPan   func(Pan)
	ants    []string
}

type reply struct {
	code uint32
	msg  string
}

// ErrClosed is returned for commands sent after the connection dropped.
var ErrClosed = errors.New("radio connection closed")

// Dial connects to the radio and waits for it to give us a client handle.
func Dial(ip string, log *slog.Logger) (*Radio, error) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, "4992"), 5*time.Second)
	if err != nil {
		return nil, err
	}
	r := &Radio{log: log, conn: conn, done: make(chan struct{}),
		pending: map[uint32]chan reply{}, pans: map[uint32]*Pan{}}
	got := make(chan struct{})
	go r.readLoop(got)
	select {
	case <-got:
	case <-r.done:
		return nil, r.err
	case <-time.After(5 * time.Second):
		r.Close()
		return nil, fmt.Errorf("no client handle from radio at %s", ip)
	}
	go r.keepalive()
	return r, nil
}

// Done is closed when the connection ends; Err then says why.
func (r *Radio) Done() <-chan struct{} { return r.done }
func (r *Radio) Err() error            { return r.err }

// Close drops the connection.
func (r *Radio) Close() { r.conn.Close() }

// Handle is our client handle.
func (r *Radio) Handle() uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.handle
}

// OnPan sets a callback for panadapter status changes.
func (r *Radio) OnPan(fn func(Pan)) {
	r.mu.Lock()
	r.onPan = fn
	r.mu.Unlock()
}

// Antennas is the antenna list the radio last reported (empty until a
// panadapter status arrives).
func (r *Radio) Antennas() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ants...)
}

// Pans returns the panadapters owned by our client handle.
func (r *Radio) OurPans() []Pan {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Pan
	for _, p := range r.pans {
		if p.ClientHandle == r.handle {
			out = append(out, *p)
		}
	}
	return out
}

func (r *Radio) readLoop(gotHandle chan struct{}) {
	defer func() {
		r.conn.Close()
		r.mu.Lock()
		for _, ch := range r.pending {
			close(ch)
		}
		r.pending = map[uint32]chan reply{}
		r.mu.Unlock()
		close(r.done)
	}()
	sc := bufio.NewScanner(r.conn)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		r.conn.SetReadDeadline(time.Now().Add(15 * time.Second))
		line := sc.Text()
		if line == "" {
			continue
		}
		switch line[0] {
		case 'V':
			r.log.Info("radio API version " + line[1:])
		case 'H':
			h, _ := strconv.ParseUint(line[1:], 16, 32)
			r.mu.Lock()
			r.handle = uint32(h)
			r.mu.Unlock()
			r.log.Info("connected", "handle", fmt.Sprintf("0x%08X", h))
			close(gotHandle)
		case 'R':
			r.handleReply(line[1:])
		case 'S':
			if _, body, ok := strings.Cut(line, "|"); ok {
				r.handleStatus(body)
			}
		case 'M':
			r.log.Debug("radio message", "msg", line[1:])
		}
	}
	r.err = sc.Err()
	if r.err == nil {
		r.err = errors.New("connection closed by radio")
	}
}

func (r *Radio) keepalive() {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-t.C:
			go r.Command("ping")
		}
	}
}

func (r *Radio) handleReply(s string) {
	parts := strings.SplitN(s, "|", 3)
	if len(parts) < 2 {
		return
	}
	seq, _ := strconv.ParseUint(parts[0], 10, 32)
	code, _ := strconv.ParseUint(parts[1], 16, 32)
	msg := ""
	if len(parts) == 3 {
		msg = parts[2]
	}
	r.mu.Lock()
	ch := r.pending[uint32(seq)]
	delete(r.pending, uint32(seq))
	r.mu.Unlock()
	if ch != nil {
		ch <- reply{uint32(code), msg}
	}
}

// handleStatus tracks panadapters: "display pan 0x40000000 center=14.097 ...".
func (r *Radio) handleStatus(body string) {
	f := strings.Fields(body)
	if len(f) < 3 || f[0] != "display" || f[1] != "pan" {
		return
	}
	id, err := parseHex(f[2])
	if err != nil {
		return
	}
	kv := ParseKV(strings.Join(f[3:], " "))
	r.mu.Lock()
	if _, gone := kv["removed"]; gone {
		delete(r.pans, id)
		r.mu.Unlock()
		return
	}
	p := r.pans[id]
	if p == nil {
		p = &Pan{ID: id}
		r.pans[id] = p
	}
	for k, v := range kv {
		switch k {
		case "client_handle":
			h, _ := parseHex(v)
			p.ClientHandle = h
		case "center":
			if mhz, err := strconv.ParseFloat(v, 64); err == nil {
				p.CenterHz = int64(mhz*1e6 + 0.5)
			}
		case "daxiq_channel":
			p.DAXIQ, _ = strconv.Atoi(v)
		case "rxant":
			p.RxAnt = v
		case "ant_list":
			p.AntList = nil
			for _, a := range strings.Split(v, ",") {
				if a != "" {
					p.AntList = append(p.AntList, a)
				}
			}
			r.ants = p.AntList
		}
	}
	cp, fn := *p, r.onPan
	r.mu.Unlock()
	if fn != nil {
		fn(cp)
	}
}

// Command sends cmd and waits up to 5 s for the reply. A non-zero reply
// code is returned as an error together with the reply text.
func (r *Radio) Command(cmd string) (string, error) {
	ch := make(chan reply, 1)
	r.mu.Lock()
	select {
	case <-r.done:
		r.mu.Unlock()
		return "", ErrClosed
	default:
	}
	r.seq++
	seq := r.seq
	r.pending[seq] = ch
	r.mu.Unlock()
	if cmd != "ping" {
		r.log.Debug("->", "cmd", cmd)
	}
	r.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if _, err := fmt.Fprintf(r.conn, "C%d|%s\n", seq, cmd); err != nil {
		r.conn.Close()
		return "", err
	}
	select {
	case rep, ok := <-ch:
		if !ok {
			return "", ErrClosed
		}
		if rep.code != 0 {
			return rep.msg, fmt.Errorf("radio refused %q (0x%08X %s)", cmd, rep.code, rep.msg)
		}
		return rep.msg, nil
	case <-time.After(5 * time.Second):
		r.mu.Lock()
		delete(r.pending, seq)
		r.mu.Unlock()
		return "", fmt.Errorf("no reply from the radio to %q", cmd)
	}
}

// Escape encodes spaces the way the radio expects in names (0x7F).
func Escape(s string) string { return strings.ReplaceAll(s, " ", "\x7f") }

func parseHex(s string) (uint32, error) {
	v, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X"), 16, 32)
	return uint32(v), err
}

// FirstHex returns the first hex number (with or without 0x) in a reply such
// as "0x40000000,0x42000000", or an error.
func FirstHex(s string) (uint32, error) {
	for _, tok := range strings.FieldsFunc(s, func(c rune) bool { return c == ',' || c == ' ' || c == '=' }) {
		if v, err := parseHex(tok); err == nil && v != 0 {
			return v, nil
		}
	}
	return 0, fmt.Errorf("no id in reply %q", s)
}
