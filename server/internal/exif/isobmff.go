package exif

// ISO base media (MP4/MOV/3GP) metadata: the same two facts framed wants from a JPEG, read from the
// container a phone camera writes. Videos never had a metadata path , they were archived, thumbed,
// and left off the map , yet every clip the phone records carries its fix in moov/udta/©xyz (Android
// MediaRecorder, Samsung camera) or moov/meta/ilst under com.apple.quicktime.location.ISO6709
// (iPhone, and Android clips that passed through Apple tooling), and its creation time in mvhd.
//
// Walks boxes on a ReadSeeker, so a 2GB clip whose moov sits at the END (the default for a camera
// that does not know the file length until it stops) costs a handful of header reads and one moov
// read, never a load of the file. Written from the ISO 14496-12 box grammar directly, no dependency.

import (
	"encoding/binary"
	"errors"
	"io"
	"strconv"
	"time"
)

// moovCap bounds the one box we do load whole. A real moov is tens of KB to a few MB (sample tables
// for a long clip); anything bigger is not a moov we want in RAM.
const moovCap = 64 << 20

// ParseISOBMFF extracts Meta from an MP4/MOV/3GP stream. Absence of metadata yields zero Meta, never
// an error , same contract as Parse for JPEG.
func ParseISOBMFF(r io.ReadSeeker) Meta {
	m, err := parseISOBMFF(r)
	if err != nil {
		return Meta{}
	}
	return m
}

var errNoMoov = errors.New("no moov")

func parseISOBMFF(r io.ReadSeeker) (Meta, error) {
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return Meta{}, err
	}
	var pos int64
	hdr := make([]byte, 16)
	for pos+8 <= size {
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			return Meta{}, err
		}
		if _, err := io.ReadFull(r, hdr[:8]); err != nil {
			return Meta{}, err
		}
		boxSize := int64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:8])
		hdrLen := int64(8)
		switch boxSize {
		case 0: // to end of file
			boxSize = size - pos
		case 1: // 64-bit largesize follows
			if _, err := io.ReadFull(r, hdr[8:16]); err != nil {
				return Meta{}, err
			}
			boxSize = int64(binary.BigEndian.Uint64(hdr[8:16]))
			hdrLen = 16
		}
		if boxSize < hdrLen || pos+boxSize > size {
			return Meta{}, errNoMoov // corrupt size: stop, do not guess
		}
		if typ == "moov" {
			body := boxSize - hdrLen
			if body > moovCap {
				return Meta{}, errNoMoov
			}
			buf := make([]byte, body)
			if _, err := io.ReadFull(r, buf); err != nil {
				return Meta{}, err
			}
			return parseMoov(buf), nil
		}
		pos += boxSize
	}
	return Meta{}, errNoMoov
}

// parseMoov reads mvhd (creation time), udta/©xyz (Android/Samsung fix) and meta/ilst
// (Apple-keyed fix) from a moov body.
func parseMoov(b []byte) Meta {
	var m Meta
	walkBoxes(b, func(typ string, body []byte) {
		switch typ {
		case "mvhd":
			if ts := mvhdCreation(body); !ts.IsZero() {
				m.TakenAt = ts
			}
		case "udta":
			walkBoxes(body, func(t string, bb []byte) {
				if t == "\xa9xyz" && len(bb) >= 4 {
					n := int(binary.BigEndian.Uint16(bb[:2]))
					if 4+n <= len(bb) {
						if lat, lon, ok := parseISO6709(string(bb[4 : 4+n])); ok && !m.HasGPS {
							m.Lat, m.Lon, m.HasGPS = lat, lon, true
						}
					}
				}
			})
		case "meta":
			// meta is a FullBox in ISO (4-byte version/flags before children) but a plain box in
			// QuickTime. Sniff: a plain box starts with a child header whose type is ASCII.
			inner := body
			if len(inner) >= 8 && !asciiType(inner[4:8]) && len(inner) >= 12 && asciiType(inner[8:12]) {
				inner = inner[4:]
			}
			if lat, lon, ok := appleLocation(inner); ok && !m.HasGPS {
				m.Lat, m.Lon, m.HasGPS = lat, lon, true
			}
		}
	})
	return m
}

func asciiType(t []byte) bool {
	for _, c := range t {
		if c < 0x20 || c > 0x7e {
			return c == 0xa9 // the © prefix QuickTime uses
		}
	}
	return true
}

// walkBoxes calls fn for each child box of b (a box body).
func walkBoxes(b []byte, fn func(typ string, body []byte)) {
	pos := 0
	for pos+8 <= len(b) {
		boxSize := int(binary.BigEndian.Uint32(b[pos : pos+4]))
		typ := string(b[pos+4 : pos+8])
		hdr := 8
		if boxSize == 1 {
			if pos+16 > len(b) {
				return
			}
			ls := binary.BigEndian.Uint64(b[pos+8 : pos+16])
			if ls > uint64(len(b)) {
				return
			}
			boxSize = int(ls)
			hdr = 16
		} else if boxSize == 0 {
			boxSize = len(b) - pos
		}
		if boxSize < hdr || pos+boxSize > len(b) {
			return
		}
		fn(typ, b[pos+hdr:pos+boxSize])
		pos += boxSize
	}
}

// mvhdCreation reads creation_time: version 0 = 32-bit, version 1 = 64-bit, both in seconds since
// 1904-01-01 UTC. Zero (a recorder that never set it) stays zero.
func mvhdCreation(b []byte) time.Time {
	if len(b) < 4 {
		return time.Time{}
	}
	var secs uint64
	switch b[0] {
	case 0:
		if len(b) < 8 {
			return time.Time{}
		}
		secs = uint64(binary.BigEndian.Uint32(b[4:8]))
	case 1:
		if len(b) < 12 {
			return time.Time{}
		}
		secs = binary.BigEndian.Uint64(b[4:12])
	default:
		return time.Time{}
	}
	const epoch1904 = 2082844800 // seconds between 1904-01-01 and 1970-01-01
	if secs <= epoch1904 {
		return time.Time{}
	}
	t := time.Unix(int64(secs-epoch1904), 0).UTC()
	if t.Year() > time.Now().Year()+1 {
		return time.Time{} // a clock from the future is not a capture time
	}
	return t
}

// appleLocation finds com.apple.quicktime.location.ISO6709 in a meta box: keys lists the names by
// index, ilst holds the values as boxes typed by that 1-based index.
func appleLocation(meta []byte) (float64, float64, bool) {
	wantIdx := -1
	var ilst []byte
	walkBoxes(meta, func(typ string, body []byte) {
		switch typ {
		case "keys":
			if len(body) < 8 {
				return
			}
			n := int(binary.BigEndian.Uint32(body[4:8]))
			pos := 8
			for i := 1; i <= n && pos+8 <= len(body); i++ {
				sz := int(binary.BigEndian.Uint32(body[pos : pos+4]))
				if sz < 8 || pos+sz > len(body) {
					return
				}
				if string(body[pos+8:pos+sz]) == "com.apple.quicktime.location.ISO6709" {
					wantIdx = i
				}
				pos += sz
			}
		case "ilst":
			ilst = body
		}
	})
	if wantIdx < 0 || ilst == nil {
		return 0, 0, false
	}
	var lat, lon float64
	found := false
	walkBoxes(ilst, func(typ string, body []byte) {
		if len(typ) == 4 && int(binary.BigEndian.Uint32([]byte(typ))) == wantIdx {
			walkBoxes(body, func(t string, bb []byte) {
				if t == "data" && len(bb) > 8 {
					if la, lo, ok := parseISO6709(string(bb[8:])); ok {
						lat, lon, found = la, lo, true
					}
				}
			})
		}
	})
	return lat, lon, found
}

// parseISO6709 decodes "+DD.DDDD+DDD.DDDD/" , the decimal-degrees form Android's MediaRecorder and
// Apple both write , taking the first two signed numbers; an altitude or CRS suffix is ignored.
// The DDMM.M form the standard also allows is NOT decoded: it fails the range check below and
// reads as no fix, which is the safe failure. Rejects 0/0 and out-of-range, as the JPEG path does.
func parseISO6709(s string) (float64, float64, bool) {
	var nums []float64
	i := 0
	for i < len(s) && len(nums) < 2 {
		if s[i] != '+' && s[i] != '-' {
			i++
			continue
		}
		j := i + 1
		for j < len(s) && ((s[j] >= '0' && s[j] <= '9') || s[j] == '.') {
			j++
		}
		v, err := strconv.ParseFloat(s[i:j], 64)
		if err != nil || j == i+1 {
			return 0, 0, false
		}
		nums = append(nums, v)
		i = j
	}
	if len(nums) < 2 {
		return 0, 0, false
	}
	lat, lon := nums[0], nums[1]
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 || (lat == 0 && lon == 0) {
		return 0, 0, false
	}
	return lat, lon, true
}
