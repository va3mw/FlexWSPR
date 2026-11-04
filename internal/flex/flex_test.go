package flex

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestParseVITAIQ(t *testing.T) {
	// IF data with stream id, class id, timestamps and 2 IQ samples.
	payload := make([]byte, 16)
	le := binary.LittleEndian
	for i, v := range []float32{0.5, -0.25, 1, 2} {
		le.PutUint32(payload[i*4:], math.Float32bits(v))
	}
	words := 1 + 1 + 2 + 1 + 2 + len(payload)/4
	b := make([]byte, words*4)
	be := binary.BigEndian
	h := uint32(1)<<28 | 1<<27 | 1<<22 | 1<<20 | uint32(7)<<16 | uint32(words)
	be.PutUint32(b, h)
	be.PutUint32(b[4:], 0x20000001)
	be.PutUint32(b[8:], flexOUI)
	be.PutUint32(b[12:], classIQ24k)
	copy(b[28:], payload)

	p, err := ParseVITA(b)
	if err != nil {
		t.Fatal(err)
	}
	if p.StreamID != 0x20000001 || p.OUI != flexOUI || IQRate(p.PacketClass) != 24000 || p.Count != 7 {
		t.Fatalf("%+v", p)
	}
	iq := IQSamples(nil, p.Payload, false)
	if len(iq) != 2 || iq[0] != complex(0.5, -0.25) || iq[1] != complex(1, 2) {
		t.Fatalf("%v", iq)
	}
	if sw := IQSamples(nil, p.Payload, true); sw[0] != complex(-0.25, 0.5) {
		t.Fatalf("swap %v", sw)
	}
}

func TestFirstHex(t *testing.T) {
	for in, want := range map[string]uint32{
		"0x40000000,0x42000000": 0x40000000,
		"0x20000000":            0x20000000,
		"pan=0x40000001 wf=0x4": 0x40000001,
	} {
		if got, err := FirstHex(in); err != nil || got != want {
			t.Errorf("FirstHex(%q) = %x, %v", in, got, err)
		}
	}
	if _, err := FirstHex(""); err == nil {
		t.Error("empty reply should fail")
	}
}
