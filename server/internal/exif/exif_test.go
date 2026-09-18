package exif

import (
	"encoding/binary"
	"math"
	"testing"
)

// buildTestJPEG hand-assembles a minimal JPEG whose APP1 carries a little-endian TIFF with IFD0
// (Exif + GPS pointers), an Exif IFD holding DateTimeOriginal, and a GPS IFD holding a London
// coordinate (51.5 N, 0.1277 W , 51 deg 30 min, 0 deg 7 min 39.6 sec). Building the bytes by hand is
// the point: the parser is spec-driven, so the test is spec-driven too.
func buildTestJPEG() []byte {
	le := binary.LittleEndian
	tiff := make([]byte, 178)
	copy(tiff[0:], "II")
	le.PutUint16(tiff[2:], 42)
	le.PutUint32(tiff[4:], 8) // IFD0 offset

	putEntry := func(off int, tag, typ uint16, count, val uint32) {
		le.PutUint16(tiff[off:], tag)
		le.PutUint16(tiff[off+2:], typ)
		le.PutUint32(tiff[off+4:], count)
		le.PutUint32(tiff[off+8:], val)
	}

	// IFD0 @8: 2 entries , ExifIFD @38, GPSIFD @76
	le.PutUint16(tiff[8:], 2)
	putEntry(10, 0x8769, 4, 1, 38)
	putEntry(22, 0x8825, 4, 1, 76)
	le.PutUint32(tiff[34:], 0) // next IFD

	// Exif IFD @38: 1 entry , DateTimeOriginal (ASCII, 20 bytes @56)
	le.PutUint16(tiff[38:], 1)
	putEntry(40, 0x9003, 2, 20, 56)
	le.PutUint32(tiff[52:], 0)
	copy(tiff[56:], "2024:06:01 12:30:00\x00")

	// GPS IFD @76: 4 entries , LatRef "N" inline, Lat 3 rationals @130, LonRef "W" inline, Lon @154
	le.PutUint16(tiff[76:], 4)
	putEntry(78, 0x0001, 2, 2, 0)
	copy(tiff[86:], "N\x00")
	putEntry(90, 0x0002, 5, 3, 130)
	putEntry(102, 0x0003, 2, 2, 0)
	copy(tiff[110:], "W\x00")
	putEntry(114, 0x0004, 5, 3, 154)
	le.PutUint32(tiff[126:], 0)

	putRat := func(off int, num, den uint32) {
		le.PutUint32(tiff[off:], num)
		le.PutUint32(tiff[off+4:], den)
	}
	// 51 deg 30 min 0 sec
	putRat(130, 51, 1)
	putRat(138, 30, 1)
	putRat(146, 0, 1)
	// 0 deg 7 min 39.6 sec (396/10)
	putRat(154, 0, 1)
	putRat(162, 7, 1)
	putRat(170, 396, 10)

	app1Payload := append([]byte("Exif\x00\x00"), tiff...)
	segLen := 2 + len(app1Payload)
	out := []byte{0xFF, 0xD8, 0xFF, 0xE1, byte(segLen >> 8), byte(segLen & 0xFF)}
	out = append(out, app1Payload...)
	return append(out, 0xFF, 0xD9)
}

func TestParseGPSAndTime(t *testing.T) {
	m := Parse(buildTestJPEG())
	if !m.HasGPS {
		t.Fatal("expected GPS")
	}
	wantLat := 51.5
	wantLon := -(0.0 + 7.0/60 + 39.6/3600) // W is negative
	if math.Abs(m.Lat-wantLat) > 1e-6 {
		t.Fatalf("lat = %v, want %v", m.Lat, wantLat)
	}
	if math.Abs(m.Lon-wantLon) > 1e-6 {
		t.Fatalf("lon = %v, want %v", m.Lon, wantLon)
	}
	if m.TakenAt.Format("2006:01:02 15:04:05") != "2024:06:01 12:30:00" {
		t.Fatalf("takenAt = %v", m.TakenAt)
	}
}

// TestParseTolerance: garbage, truncation, and EXIF-free JPEGs all yield zero Meta, never a panic ,
// absence of metadata is a normal photo, not an error.
func TestParseTolerance(t *testing.T) {
	cases := [][]byte{
		nil,
		{0xFF},
		{0xFF, 0xD8},                         // bare SOI
		{0xFF, 0xD8, 0xFF, 0xD9},             // empty JPEG
		{0x89, 'P', 'N', 'G', 0, 0, 0, 0},    // not a JPEG at all
		buildTestJPEG()[:40],                 // truncated mid-TIFF
	}
	for i, c := range cases {
		m := Parse(c)
		if m.HasGPS || !m.TakenAt.IsZero() {
			t.Fatalf("case %d: expected zero Meta, got %+v", i, m)
		}
	}
}

// TestParseTruncatedAPP1 , the pipeline reads a fixed HEAD of the file, and a phone's Exif APP1 can
// run to 64KiB with its embedded thumbnail. The segment overrunning the buffer must not cost the
// date and the fix that sit at the front of it. Simulated: declare a segment length far beyond the
// bytes supplied and cut the file mid-thumbnail.
func TestParseTruncatedAPP1(t *testing.T) {
	full := buildTestJPEG()
	// Pretend the APP1 is 60000 bytes long (thumbnail we never received) and hand over only the
	// bytes that exist , the IFDs are all present, the declared tail is not.
	cut := append([]byte{}, full[:len(full)-2]...) // drop EOI
	cut[4], cut[5] = byte(60000>>8), byte(60000&0xFF)
	m := Parse(cut)
	if !m.HasGPS {
		t.Fatal("truncated APP1 must still yield GPS from the IFDs that arrived")
	}
	if m.TakenAt.IsZero() {
		t.Fatal("truncated APP1 must still yield DateTimeOriginal")
	}
	if math.Abs(m.Lat-51.5) > 1e-6 {
		t.Fatalf("lat = %v", m.Lat)
	}
}

// TestParseFillBytes , 0xFF padding between markers is legal JPEG; it must not read as "not a JPEG".
func TestParseFillBytes(t *testing.T) {
	full := buildTestJPEG()
	padded := append([]byte{0xFF, 0xD8, 0xFF, 0xFF, 0xFF}, full[2:]...)
	if m := Parse(padded); !m.HasGPS {
		t.Fatal("fill bytes before APP1 must be skipped")
	}
}

// TestParseNullIsland , a 0/0 fix is a camera with no lock, not a photo taken in the Gulf of Guinea.
func TestParseNullIsland(t *testing.T) {
	b := buildTestJPEG()
	le := binary.LittleEndian
	// TIFF starts at offset 12 (SOI 2 + marker 2 + len 2 + "Exif\0\0" 6); rationals @130 and @154.
	base := 12
	for _, off := range []int{130, 138, 146, 154, 162, 170} {
		le.PutUint32(b[base+off:], 0)
		le.PutUint32(b[base+off+4:], 1)
	}
	if m := Parse(b); m.HasGPS {
		t.Fatalf("0/0 must not count as a fix, got %+v", m)
	}
}

// TestParseOffsetTimeOriginal , with the EXIF 2.31 zone tag the wall clock becomes an instant.
func TestParseOffsetTimeOriginal(t *testing.T) {
	le := binary.LittleEndian
	// Rebuild the Exif IFD with TWO entries: DateTimeOriginal and OffsetTimeOriginal ("+01:00").
	tiff := make([]byte, 120)
	copy(tiff[0:], "II")
	le.PutUint16(tiff[2:], 42)
	le.PutUint32(tiff[4:], 8)
	putEntry := func(off int, tag, typ uint16, count, val uint32) {
		le.PutUint16(tiff[off:], tag)
		le.PutUint16(tiff[off+2:], typ)
		le.PutUint32(tiff[off+4:], count)
		le.PutUint32(tiff[off+8:], val)
	}
	le.PutUint16(tiff[8:], 1)
	putEntry(10, 0x8769, 4, 1, 26)
	le.PutUint32(tiff[22:], 0)
	le.PutUint16(tiff[26:], 2)
	putEntry(28, 0x9003, 2, 20, 56)
	putEntry(40, 0x9011, 2, 7, 76)
	le.PutUint32(tiff[52:], 0)
	copy(tiff[56:], "2024:06:01 12:30:00\x00")
	copy(tiff[76:], "+01:00\x00")
	payload := append([]byte("Exif\x00\x00"), tiff...)
	segLen := 2 + len(payload)
	b := append([]byte{0xFF, 0xD8, 0xFF, 0xE1, byte(segLen >> 8), byte(segLen & 0xFF)}, payload...)
	m := Parse(append(b, 0xFF, 0xD9))
	if got := m.TakenAt.UTC().Format("2006-01-02T15:04:05Z"); got != "2024-06-01T11:30:00Z" {
		t.Fatalf("12:30 at +01:00 must be 11:30Z, got %s", got)
	}
}
