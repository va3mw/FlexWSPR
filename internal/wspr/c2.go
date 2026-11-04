// Package wspr captures two-minute WSPR cycles, writes them as .c2 files and
// decodes them with wsprd.
package wspr

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"time"
)

// C2Samples is the number of complex samples in a .c2 file: 120 s at 375 Hz.
const C2Samples = 45000

// C2Name is the file name wsprd expects for a cycle, "YYMMDD_HHMM.c2"
// (exactly 14 characters); wsprd takes the date and time from it.
func C2Name(start time.Time) string { return start.UTC().Format("060102_1504") + ".c2" }

// WriteC2 writes a .c2 file in wsprd's layout (little-endian, as wsprd reads
// it on x86 and ARM):
//
//	char   name[14]   file name, YYMMDD_HHMM.c2
//	int32  type       2 (WSPR-2)
//	double dial       dial frequency in MHz
//	float  iq[45000][2]
//
// wsprd negates Q when it reads the file, so Q is stored negated here. flip
// stores it un-negated instead (for a spectrum that comes out mirrored).
// Missing samples are written as zeros; extra ones are dropped.
func WriteC2(path string, start time.Time, dialHz int64, iq []complex64, flip bool) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 64*1024)
	var name [14]byte
	copy(name[:], C2Name(start))
	w.Write(name[:])
	le := binary.LittleEndian
	var b [8]byte
	le.PutUint32(b[:4], 2)
	w.Write(b[:4])
	le.PutUint64(b[:], math.Float64bits(float64(dialHz)/1e6))
	w.Write(b[:])
	qs := float32(-1)
	if flip {
		qs = 1
	}
	for i := 0; i < C2Samples; i++ {
		var s complex64
		if i < len(iq) {
			s = iq[i]
		}
		le.PutUint32(b[:4], math.Float32bits(real(s)))
		le.PutUint32(b[4:], math.Float32bits(qs*imag(s)))
		w.Write(b[:])
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ReadC2 reads a .c2 file written by WriteC2 (undoing the Q negation unless
// flip). Used by tests and the -decode tool.
func ReadC2(path string, flip bool) (name string, dialHz int64, iq []complex64, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0, nil, err
	}
	if len(b) != 14+4+8+C2Samples*8 {
		return "", 0, nil, fmt.Errorf("%s: %d bytes, not a WSPR-2 .c2 file", path, len(b))
	}
	le := binary.LittleEndian
	name = string(b[:14])
	dialHz = int64(math.Float64frombits(le.Uint64(b[18:]))*1e6 + 0.5)
	qs := float32(-1)
	if flip {
		qs = 1
	}
	iq = make([]complex64, C2Samples)
	p := b[26:]
	for i := range iq {
		iq[i] = complex(math.Float32frombits(le.Uint32(p[i*8:])), qs*math.Float32frombits(le.Uint32(p[i*8+4:])))
	}
	return name, dialHz, iq, nil
}
