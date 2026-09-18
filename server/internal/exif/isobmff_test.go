package exif

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

func box(typ string, body ...[]byte) []byte {
	var b bytes.Buffer
	n := 8
	for _, p := range body {
		n += len(p)
	}
	_ = binary.Write(&b, binary.BigEndian, uint32(n))
	b.WriteString(typ)
	for _, p := range body {
		b.Write(p)
	}
	return b.Bytes()
}

func be16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func be32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }

// mvhd version 0: version/flags, creation, modification, timescale, duration, ... (we only read
// creation). 2024-06-01T12:30:00Z = 1717245000 unix = 3800089800 since 1904.
func mvhdV0(creation1904 uint32) []byte {
	body := append([]byte{0, 0, 0, 0}, be32(creation1904)...)
	body = append(body, be32(0)...)    // modification
	body = append(body, be32(1000)...) // timescale
	body = append(body, be32(0)...)    // duration
	body = append(body, make([]byte, 80)...)
	return box("mvhd", body)
}

// androidClip lays out a phone recording: ftyp, a big mdat, then moov at the END with udta/©xyz.
func androidClip() []byte {
	xyz := "+51.5074-000.1278/"
	udta := box("udta", box("\xa9xyz", be16(uint16(len(xyz))), be16(0x15c7), []byte(xyz)))
	moov := box("moov", mvhdV0(3800089800), udta)
	var f bytes.Buffer
	f.Write(box("ftyp", []byte("isom"), be32(0x200), []byte("isommp42")))
	f.Write(box("mdat", make([]byte, 100_000)))
	f.Write(moov)
	return f.Bytes()
}

func TestParseISOBMFFAndroidXYZ(t *testing.T) {
	m := ParseISOBMFF(bytes.NewReader(androidClip()))
	if !m.HasGPS {
		t.Fatal("expected a fix from moov/udta/©xyz")
	}
	if math.Abs(m.Lat-51.5074) > 1e-6 || math.Abs(m.Lon+0.1278) > 1e-6 {
		t.Fatalf("lat/lon = %v/%v", m.Lat, m.Lon)
	}
	if got := m.TakenAt.Format("2006-01-02T15:04:05Z"); got != "2024-06-01T12:30:00Z" {
		t.Fatalf("mvhd creation = %s", got)
	}
}

func TestParseISOBMFFAppleKeys(t *testing.T) {
	key := "com.apple.quicktime.location.ISO6709"
	keys := box("keys", []byte{0, 0, 0, 0}, be32(1), box("mdta", []byte(key)))
	// ilst item typed by 1-based key index, holding a data box: type indicator 1 (UTF-8), locale 0.
	data := box("data", be32(1), be32(0), []byte("+48.8566+002.3522+035.000/"))
	item := box(string(be32(1)), data)
	ilst := box("ilst", item)
	// ISO-style meta FullBox: version/flags then children.
	meta := box("meta", []byte{0, 0, 0, 0}, box("hdlr", make([]byte, 24)), keys, ilst)
	moov := box("moov", mvhdV0(0), meta)
	f := append(box("ftyp", []byte("qt  "), be32(0), []byte("qt  ")), moov...)
	m := ParseISOBMFF(bytes.NewReader(f))
	if !m.HasGPS {
		t.Fatal("expected a fix from meta/ilst ISO6709")
	}
	if math.Abs(m.Lat-48.8566) > 1e-6 || math.Abs(m.Lon-2.3522) > 1e-6 {
		t.Fatalf("lat/lon = %v/%v", m.Lat, m.Lon)
	}
	if !m.TakenAt.IsZero() {
		t.Fatalf("mvhd creation 0 must stay unknown, got %v", m.TakenAt)
	}
}

func TestParseISOBMFFTolerance(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte("not a box at all"),
		box("ftyp", []byte("isom")),
		androidClip()[:5000], // cut before moov
		append(box("ftyp", []byte("isom")), 0, 0, 0xff, 0xff, 'm', 'o', 'o', 'v'), // size past EOF
	}
	for i, c := range cases {
		if m := ParseISOBMFF(bytes.NewReader(c)); m.HasGPS || !m.TakenAt.IsZero() {
			t.Fatalf("case %d: expected zero Meta, got %+v", i, m)
		}
	}
}

func TestParseISO6709(t *testing.T) {
	for _, c := range []struct {
		in       string
		lat, lon float64
		ok       bool
	}{
		{"+51.5074-000.1278/", 51.5074, -0.1278, true},
		{"-33.8688+151.2093+021.000/", -33.8688, 151.2093, true},
		{"+00.0000+000.0000/", 0, 0, false}, // no lock
		{"+5130.44-00007.66/", 0, 0, false}, // DDMM form: rejected, not misread
		{"garbage", 0, 0, false},
		{"+91.0+0.0/", 0, 0, false},
	} {
		lat, lon, ok := parseISO6709(c.in)
		if ok != c.ok || (ok && (math.Abs(lat-c.lat) > 1e-9 || math.Abs(lon-c.lon) > 1e-9)) {
			t.Fatalf("%q: got %v/%v/%v want %v/%v/%v", c.in, lat, lon, ok, c.lat, c.lon, c.ok)
		}
	}
}
