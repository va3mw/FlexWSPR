package flex

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"time"
)

// RadioInfo is one radio seen in the UDP discovery broadcast.
type RadioInfo struct {
	Serial   string
	Model    string
	Nickname string
	IP       string
	Version  string
	Status   string
}

// Discover listens on UDP 4992 for a radio's discovery broadcast and returns
// the first one whose serial matches (any radio when serial is blank).
func Discover(ctx context.Context, serial string) (RadioInfo, error) {
	lc := net.ListenConfig{Control: reuseAddr} // SmartSDR may share the port
	pc, err := lc.ListenPacket(ctx, "udp4", ":4992")
	if err != nil {
		return RadioInfo{}, fmt.Errorf("listen for discovery on UDP 4992: %w", err)
	}
	defer pc.Close()
	go func() { <-ctx.Done(); pc.Close() }()
	buf := make([]byte, 2048)
	for {
		pc.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := pc.ReadFrom(buf)
		if ctx.Err() != nil {
			return RadioInfo{}, ctx.Err()
		}
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return RadioInfo{}, err
		}
		info := parseDiscovery(buf[:n])
		if info == nil {
			continue
		}
		if serial == "" || strings.EqualFold(info.Serial, serial) {
			return *info, nil
		}
	}
}

func parseDiscovery(b []byte) *RadioInfo {
	if len(b) < 8 {
		return nil
	}
	hdr := binary.BigEndian.Uint32(b[0:4])
	off := 4
	if t := hdr >> 28; t == 1 || t == 3 || t == 4 {
		off += 4
	}
	if hdr&(1<<27) != 0 {
		off += 8
	}
	if (hdr>>22)&3 != 0 {
		off += 4
	}
	if (hdr>>20)&3 != 0 {
		off += 8
	}
	end := int(hdr&0xFFFF) * 4
	if end > len(b) || end <= off {
		end = len(b)
	}
	if off >= end {
		return nil
	}
	kv := ParseKV(strings.TrimRight(string(b[off:end]), "\x00 "))
	if kv["serial"] == "" || kv["ip"] == "" {
		return nil
	}
	return &RadioInfo{
		Serial:   kv["serial"],
		Model:    kv["model"],
		Nickname: unescape(kv["nickname"]),
		IP:       kv["ip"],
		Version:  kv["version"],
		Status:   kv["status"],
	}
}

// ParseKV splits "a=1 b=2 c" into a map. Bare words map to "".
func ParseKV(s string) map[string]string {
	m := map[string]string{}
	for _, f := range strings.Fields(s) {
		k, v, _ := strings.Cut(f, "=")
		m[k] = v
	}
	return m
}

// unescape reverses the radio's encoding of spaces (0x7F) in names.
func unescape(s string) string { return strings.ReplaceAll(s, "\x7f", " ") }

// DiscoverAll listens for d and returns every radio heard, once each.
func DiscoverAll(ctx context.Context, d time.Duration) ([]RadioInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	lc := net.ListenConfig{Control: reuseAddr}
	pc, err := lc.ListenPacket(ctx, "udp4", ":4992")
	if err != nil {
		return nil, fmt.Errorf("listen for discovery on UDP 4992: %w", err)
	}
	defer pc.Close()
	go func() { <-ctx.Done(); pc.Close() }()
	seen := map[string]bool{}
	var out []RadioInfo
	buf := make([]byte, 2048)
	for {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return out, nil
			}
			return out, err
		}
		if info := parseDiscovery(buf[:n]); info != nil && !seen[info.Serial] {
			seen[info.Serial] = true
			out = append(out, *info)
		}
	}
}
