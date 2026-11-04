package flex

import (
	"encoding/binary"
	"errors"
	"math"
)

const (
	flexOUI = 0x001C2D

	// DAX IQ ("IF wide") packet classes: little-endian float32 I/Q pairs.
	classIQ24k  = 0x02E3
	classIQ48k  = 0x02E4
	classIQ96k  = 0x02E5
	classIQ192k = 0x02E6
)

// Packet is a decoded VITA-49 packet.
type Packet struct {
	Type        uint8
	Count       uint8 // 4-bit packet count, to spot lost packets
	StreamID    uint32
	OUI         uint32
	PacketClass uint16
	Payload     []byte
}

// ParseVITA decodes the VITA-49 preamble and returns the payload slice.
func ParseVITA(b []byte) (Packet, error) {
	var p Packet
	if len(b) < 4 {
		return p, errors.New("short packet")
	}
	be := binary.BigEndian
	h := be.Uint32(b)
	p.Type = uint8(h >> 28)
	p.Count = uint8(h>>16) & 0x0F
	size := int(h&0xFFFF) * 4
	if size > len(b) || size < 4 {
		size = len(b)
	}
	i := 4
	need := func(n int) bool { return i+n <= size }
	if p.Type == 1 || p.Type == 3 {
		if !need(4) {
			return p, errors.New("truncated stream id")
		}
		p.StreamID = be.Uint32(b[i:])
		i += 4
	}
	if h&(1<<27) != 0 {
		if !need(8) {
			return p, errors.New("truncated class id")
		}
		p.OUI = be.Uint32(b[i:]) & 0xFFFFFF
		p.PacketClass = uint16(be.Uint32(b[i+4:]))
		i += 8
	}
	if (h>>22)&3 != 0 {
		i += 4
	}
	if (h>>20)&3 != 0 {
		i += 8
	}
	end := size
	if h&(1<<26) != 0 { // trailer
		end -= 4
	}
	if i > end {
		return p, errors.New("truncated packet")
	}
	p.Payload = b[i:end]
	return p, nil
}

// IQRate returns the sample rate of a DAX IQ packet class, or 0.
func IQRate(class uint16) int {
	switch class {
	case classIQ24k:
		return 24000
	case classIQ48k:
		return 48000
	case classIQ96k:
		return 96000
	case classIQ192k:
		return 192000
	}
	return 0
}

// IQSamples decodes a DAX IQ payload (little-endian float32 I, Q pairs) into
// dst, which is grown as needed and returned. swap exchanges I and Q.
func IQSamples(dst []complex64, payload []byte, swap bool) []complex64 {
	n := len(payload) / 8
	if cap(dst) < n {
		dst = make([]complex64, n)
	}
	dst = dst[:n]
	le := binary.LittleEndian
	for k := range dst {
		i := math.Float32frombits(le.Uint32(payload[k*8:]))
		q := math.Float32frombits(le.Uint32(payload[k*8+4:]))
		if swap {
			i, q = q, i
		}
		dst[k] = complex(i, q)
	}
	return dst
}
