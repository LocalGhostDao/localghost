// Package grib2 reads the forecast grids the weather centres publish (GRIB edition 2, WMO
// FM 92), for the box's own forecast (internal/nwp). It is the part of the format the three
// models the box pulls use and nothing more: a regular latitude-longitude grid (template 3.0),
// a field at a time or over an interval (templates 4.0 and 4.8), values packed simply (5.0),
// in groups with spatial differencing (5.2 and 5.3, GFS), or with CCSDS Rice coding (5.42,
// ECMWF since July 2023), a bitmap of missing points (6). JPEG 2000 packing (5.40) is not
// read; a message that uses it, or any template outside these, is skipped with its number in
// the error so the log says what the centre changed. Standard library only.
//
// A file holds one message or hundreds; Read walks them one at a time, decoding each message's
// header sections at once and its values on request (Values), so the caller can pass over the
// fields it does not want without unpacking them. docs/WEATHER.md says why the box reads the
// grids itself.
package grib2

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

// Grid is a regular latitude-longitude grid (template 3.0): Ni points along a row of latitude,
// Nj rows, from the first point in the scanning order. Degrees. Di and Dj are positive; the
// scanning flags say which way the rows and columns run.
type Grid struct {
	Ni, Nj              int
	Lat1, Lon1          float64 // the first point in scanning order
	Lat2, Lon2          float64
	Di, Dj              float64
	ScanWest, ScanNorth bool // rows run east to west; columns run south to north
	JConsecutive        bool // points consecutive along j (columns) rather than i (rows)
}

// Param names a field: discipline, category, number (the WMO tables), the surface and its
// value (103 = height above ground in metres, 1 = ground), and the step.
type Param struct {
	Discipline, Category, Number int
	SurfaceType                  int
	SurfaceValue                 float64
	// Step is the hours from the reference time; for a field over an interval (template 4.8)
	// StepFrom is the start of the interval and Step its end, Interval true.
	Step, StepFrom int
	Interval       bool
}

// Message is one field's header; Values unpacks it.
type Message struct {
	Centre  int
	RefTime time.Time
	Grid    Grid
	Param   Param
	// the data representation
	template           int
	npoints            int
	ref                float32
	binScale, decScale int
	nbits              int
	complex            complexParams
	ccsds              ccsdsParams
	bitmap             []byte // nil when every point is present
	data               []byte
	err                error // a representation the reader does not do, reported by Values
}

type complexParams struct {
	splitting, missingMgmt int
	missing1, missing2     uint32
	groups                 int
	widthRef, widthBits    int
	lengthRef, lengthInc   int
	lastLength, lengthBits int
	order, extraOctets     int
}

type ccsdsParams struct {
	flags, blockSize, rsi int
}

// Reader walks the messages of a stream.
type Reader struct {
	r   io.Reader
	buf []byte
}

func NewReader(r io.Reader) *Reader { return &Reader{r: r} }

// Next reads the next message's sections; io.EOF at the end. A message whose sections are
// malformed is an error; one whose grid or packing the reader does not do is returned with
// its header and an error that Values repeats, so the caller can skip it by name.
func (rd *Reader) Next() (*Message, error) {
	// find "GRIB" (a file may carry padding or an index between messages)
	var head [16]byte
	if _, err := io.ReadFull(rd.r, head[:4]); err != nil {
		return nil, err
	}
	for string(head[:4]) != "GRIB" {
		copy(head[:3], head[1:4])
		if _, err := io.ReadFull(rd.r, head[3:4]); err != nil {
			return nil, err
		}
	}
	if _, err := io.ReadFull(rd.r, head[4:16]); err != nil {
		return nil, fmt.Errorf("grib2: short indicator section: %w", err)
	}
	if head[7] != 2 {
		return nil, fmt.Errorf("grib2: edition %d, not 2", head[7])
	}
	total := binary.BigEndian.Uint64(head[8:16])
	if total < 16 || total > 1<<31 {
		return nil, fmt.Errorf("grib2: message length %d", total)
	}
	body := make([]byte, total-16)
	if _, err := io.ReadFull(rd.r, body); err != nil {
		return nil, fmt.Errorf("grib2: short message: %w", err)
	}
	m := &Message{Param: Param{Discipline: int(head[6])}}
	if err := m.parse(body); err != nil {
		return nil, err
	}
	return m, nil
}

// parse reads sections 1 to 8 from the body after the indicator.
func (m *Message) parse(b []byte) error {
	off := 0
	for {
		if off+4 > len(b) {
			return errors.New("grib2: no end section")
		}
		if string(b[off:off+4]) == "7777" {
			return nil
		}
		if off+5 > len(b) {
			return errors.New("grib2: truncated section")
		}
		n := int(binary.BigEndian.Uint32(b[off:]))
		if n < 5 || off+n > len(b) {
			return fmt.Errorf("grib2: section length %d past the message", n)
		}
		sec := b[off+4]
		s := b[off+5 : off+n]
		var err error
		switch sec {
		case 1:
			err = m.identification(s)
		case 2:
			// local use: ignored
		case 3:
			err = m.gridDefinition(s)
		case 4:
			err = m.productDefinition(s)
		case 5:
			err = m.dataRepresentation(s)
		case 6:
			err = m.bitmapSection(s)
		case 7:
			m.data = s
		default:
			return fmt.Errorf("grib2: section %d", sec)
		}
		if err != nil {
			return err
		}
		off += n
	}
}

func (m *Message) identification(s []byte) error {
	if len(s) < 16 {
		return errors.New("grib2: short identification section")
	}
	m.Centre = int(binary.BigEndian.Uint16(s[0:2]))
	year := int(binary.BigEndian.Uint16(s[7:9]))
	m.RefTime = time.Date(year, time.Month(s[9]), int(s[10]), int(s[11]), int(s[12]), int(s[13]), 0, time.UTC)
	return nil
}

func (m *Message) gridDefinition(s []byte) error {
	if len(s) < 9 {
		return errors.New("grib2: short grid section")
	}
	m.npoints = int(binary.BigEndian.Uint32(s[1:5]))
	tmpl := int(binary.BigEndian.Uint16(s[7:9]))
	if tmpl != 0 {
		m.err = fmt.Errorf("grib2: grid template 3.%d (only 3.0, a regular latitude-longitude grid, is read)", tmpl)
		return nil
	}
	t := s[9:]
	if len(t) < 58 {
		return errors.New("grib2: short grid template 3.0")
	}
	// octets of template 3.0, from its first: the earth's shape and radii (0..15), Ni (16),
	// Nj (20), the basic angle and its subdivisions (24, 28), the first point (32, 36), the
	// resolution flags (40), the last point (41, 45), the increments (49, 53), the scanning
	// mode (57)
	g := &m.Grid
	g.Ni = int(binary.BigEndian.Uint32(t[16:20]))
	g.Nj = int(binary.BigEndian.Uint32(t[20:24]))
	basic := binary.BigEndian.Uint32(t[24:28])
	sub := binary.BigEndian.Uint32(t[28:32])
	unit := 1e-6
	if basic != 0 && basic != 0xffffffff && sub != 0 && sub != 0xffffffff {
		unit = float64(basic) / float64(sub)
	}
	g.Lat1 = float64(signMag32(t[32:36])) * unit
	g.Lon1 = float64(signMag32(t[36:40])) * unit
	g.Lat2 = float64(signMag32(t[41:45])) * unit
	g.Lon2 = float64(signMag32(t[45:49])) * unit
	di := binary.BigEndian.Uint32(t[49:53])
	dj := binary.BigEndian.Uint32(t[53:57])
	if di != 0xffffffff {
		g.Di = float64(di) * unit
	}
	if dj != 0xffffffff {
		g.Dj = float64(dj) * unit
	}
	scan := t[57]
	g.ScanWest = scan&0x80 != 0
	g.ScanNorth = scan&0x40 != 0
	g.JConsecutive = scan&0x20 != 0
	if g.Di == 0 && g.Ni > 1 {
		g.Di = math.Abs(g.Lon2-g.Lon1) / float64(g.Ni-1)
	}
	if g.Dj == 0 && g.Nj > 1 {
		g.Dj = math.Abs(g.Lat2-g.Lat1) / float64(g.Nj-1)
	}
	if g.Ni <= 0 || g.Nj <= 0 || g.Ni*g.Nj != m.npoints {
		return fmt.Errorf("grib2: grid %d×%d for %d points", g.Ni, g.Nj, m.npoints)
	}
	return nil
}

func (m *Message) productDefinition(s []byte) error {
	if len(s) < 4 {
		return errors.New("grib2: short product section")
	}
	tmpl := int(binary.BigEndian.Uint16(s[2:4]))
	t := s[4:]
	if tmpl != 0 && tmpl != 8 {
		m.err = fmt.Errorf("grib2: product template 4.%d (4.0 and 4.8 are read)", tmpl)
		return nil
	}
	if len(t) < 25 {
		return errors.New("grib2: short product template")
	}
	p := &m.Param
	p.Category = int(t[0])
	p.Number = int(t[1])
	unit := t[8]
	ft := int(signMag32(t[9:13]))
	p.Step = hours(unit, ft)
	p.SurfaceType = int(t[13])
	p.SurfaceValue = scaled(t[14], t[15:19])
	if tmpl == 8 {
		if len(t) < 25+7+1+4+12 {
			return errors.New("grib2: short product template 4.8")
		}
		p.Interval = true
		p.StepFrom = p.Step
		// the end of the interval, then the ranges: the first range's length is the interval
		n := int(t[32])
		if n >= 1 && len(t) >= 37+12 {
			r := t[37:]
			p.Step = p.StepFrom + hours(r[2], int(binary.BigEndian.Uint32(r[3:7])))
		}
	}
	return nil
}

// hours converts a forecast time in the unit given (table 4.4) to hours; minutes and days
// are the ones the centres use besides hours.
func hours(unit byte, v int) int {
	switch unit {
	case 0:
		return v / 60
	case 1:
		return v
	case 2:
		return v * 24
	case 10:
		return v * 3
	case 11:
		return v * 6
	case 12:
		return v * 12
	case 13:
		return v / 3600
	}
	return v
}

func scaled(factor byte, v []byte) float64 {
	raw := signMag32(v)
	if raw == -0x7fffffff || (uint32(raw) == 0xffffffff) {
		return 0
	}
	f := int(int8(factor))
	return float64(raw) / math.Pow(10, float64(f))
}

func (m *Message) dataRepresentation(s []byte) error {
	if len(s) < 6 {
		return errors.New("grib2: short data representation section")
	}
	m.template = int(binary.BigEndian.Uint16(s[4:6]))
	t := s[6:]
	switch m.template {
	case 0, 2, 3, 42:
	default:
		m.err = fmt.Errorf("grib2: data template 5.%d (5.0, 5.2, 5.3 and 5.42 are read)", m.template)
		return nil
	}
	if len(t) < 10 {
		return errors.New("grib2: short data template")
	}
	m.ref = math.Float32frombits(binary.BigEndian.Uint32(t[0:4]))
	m.binScale = signMag16(t[4:6])
	m.decScale = signMag16(t[6:8])
	m.nbits = int(t[8])
	switch m.template {
	case 2, 3:
		if len(t) < 36 {
			return errors.New("grib2: short data template 5.2")
		}
		c := &m.complex
		c.splitting = int(t[10])
		c.missingMgmt = int(t[11])
		c.missing1 = binary.BigEndian.Uint32(t[12:16])
		c.missing2 = binary.BigEndian.Uint32(t[16:20])
		c.groups = int(binary.BigEndian.Uint32(t[20:24]))
		c.widthRef = int(t[24])
		c.widthBits = int(t[25])
		c.lengthRef = int(binary.BigEndian.Uint32(t[26:30]))
		c.lengthInc = int(t[30])
		c.lastLength = int(binary.BigEndian.Uint32(t[31:35]))
		c.lengthBits = int(t[35])
		if m.template == 3 {
			if len(t) < 38 {
				return errors.New("grib2: short data template 5.3")
			}
			c.order = int(t[36])
			c.extraOctets = int(t[37])
		}
	case 42:
		if len(t) < 14 {
			return errors.New("grib2: short data template 5.42")
		}
		m.ccsds = ccsdsParams{flags: int(t[10]), blockSize: int(t[11]), rsi: int(binary.BigEndian.Uint16(t[12:14]))}
	}
	return nil
}

func (m *Message) bitmapSection(s []byte) error {
	if len(s) < 1 {
		return errors.New("grib2: short bitmap section")
	}
	switch s[0] {
	case 0:
		m.bitmap = s[1:]
	case 255:
		m.bitmap = nil
	default:
		m.err = fmt.Errorf("grib2: bitmap indicator %d (a bitmap defined elsewhere is not read)", s[0])
	}
	return nil
}

// Missing marks a point the bitmap leaves out.
var Missing = float32(math.NaN())

// Values unpacks the field: Grid.Ni × Grid.Nj values in the message's scanning order, Missing
// where the bitmap says so.
func (m *Message) Values() ([]float32, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.data == nil {
		return nil, errors.New("grib2: no data section")
	}
	n := m.npoints
	coded := n
	if m.bitmap != nil {
		coded = 0
		for i := 0; i < n; i++ {
			if m.bitmap[i>>3]&(0x80>>(i&7)) != 0 {
				coded++
			}
		}
	}
	var packed []uint32
	var err error
	switch m.template {
	case 0:
		packed, err = unpackSimple(m.data, coded, m.nbits)
	case 2, 3:
		return m.unpackComplex(n, coded)
	case 42:
		packed, err = unpackCCSDS(m.data, coded, m.nbits, m.ccsds)
	}
	if err != nil {
		return nil, err
	}
	out := make([]float32, n)
	scale := math.Pow(2, float64(m.binScale)) / math.Pow(10, float64(m.decScale))
	ref := float64(m.ref) / math.Pow(10, float64(m.decScale))
	k := 0
	for i := 0; i < n; i++ {
		if m.bitmap != nil && m.bitmap[i>>3]&(0x80>>(i&7)) == 0 {
			out[i] = Missing
			continue
		}
		v := ref
		if m.nbits > 0 {
			v = ref + float64(packed[k])*scale
		}
		out[i] = float32(v)
		k++
	}
	return out, nil
}

// signMag32 reads a four-octet GRIB integer: a sign bit and a magnitude.
func signMag32(b []byte) int32 {
	v := binary.BigEndian.Uint32(b)
	if v&0x80000000 != 0 {
		return -int32(v & 0x7fffffff)
	}
	return int32(v)
}

func signMag16(b []byte) int {
	v := binary.BigEndian.Uint16(b)
	if v&0x8000 != 0 {
		return -int(v & 0x7fff)
	}
	return int(v)
}

// --- the grid's geometry ---

// Index is the position of column i (along the rows, from the first point) and row j in the
// values slice.
func (g Grid) Index(i, j int) int {
	if g.JConsecutive {
		return i*g.Nj + j
	}
	return j*g.Ni + i
}

// LatLon is the latitude and longitude of column i, row j.
func (g Grid) LatLon(i, j int) (float64, float64) {
	lat := g.Lat1 - float64(j)*g.Dj
	if g.ScanNorth {
		lat = g.Lat1 + float64(j)*g.Dj
	}
	lon := g.Lon1 + float64(i)*g.Di
	if g.ScanWest {
		lon = g.Lon1 - float64(i)*g.Di
	}
	return lat, lon
}

// Sample is the value at a point by bilinear interpolation of the four grid points around
// it, in the grid's units; ok false off the grid or beside a missing point. Longitudes wrap
// (a global grid from 0 to 359.75 answers for -10 as for 350; a grid that stops short of the
// seam does not answer past its edge).
func (g Grid) Sample(vals []float32, lat, lon float64) (float32, bool) {
	fi, fj, ok := g.Locate(lat, lon)
	if !ok {
		return 0, false
	}
	i0 := int(math.Floor(fi))
	j0 := int(math.Floor(fj))
	ti := fi - float64(i0)
	tj := fj - float64(j0)
	i1, j1 := i0+1, j0+1
	global := math.Abs(float64(g.Ni)*g.Di-360) < g.Di/2
	if i1 >= g.Ni {
		if global {
			i1 = 0
		} else if ti < 1e-9 {
			i1 = i0
		} else {
			return 0, false
		}
	}
	if j1 >= g.Nj {
		if tj < 1e-9 {
			j1 = j0
		} else {
			return 0, false
		}
	}
	v00 := vals[g.Index(i0, j0)]
	v10 := vals[g.Index(i1, j0)]
	v01 := vals[g.Index(i0, j1)]
	v11 := vals[g.Index(i1, j1)]
	if isNaN(v00) || isNaN(v10) || isNaN(v01) || isNaN(v11) {
		return 0, false
	}
	top := float64(v00)*(1-ti) + float64(v10)*ti
	bot := float64(v01)*(1-ti) + float64(v11)*ti
	return float32(top*(1-tj) + bot*tj), true
}

// Locate is the fractional column and row of a point; ok false off the grid.
func (g Grid) Locate(lat, lon float64) (float64, float64, bool) {
	if g.Di <= 0 || g.Dj <= 0 {
		return 0, 0, false
	}
	var fj float64
	if g.ScanNorth {
		fj = (lat - g.Lat1) / g.Dj
	} else {
		fj = (g.Lat1 - lat) / g.Dj
	}
	if fj < -1e-9 || fj > float64(g.Nj-1)+1e-9 {
		return 0, 0, false
	}
	// longitude into the grid's own range, wrapping by 360
	d := lon - g.Lon1
	if g.ScanWest {
		d = g.Lon1 - lon
	}
	d = math.Mod(d, 360)
	if d < 0 {
		d += 360
	}
	fi := d / g.Di
	span := float64(g.Ni) * g.Di
	global := math.Abs(span-360) < g.Di/2
	if fi > float64(g.Ni-1)+1e-9 {
		if !global {
			// the point may sit west of the first column by a little, as a wrapped -0.0001
			if 360-d < 1e-6 {
				fi = 0
			} else {
				return 0, 0, false
			}
		}
	}
	if fj < 0 {
		fj = 0
	}
	if fi < 0 {
		fi = 0
	}
	return fi, fj, true
}

func isNaN(v float32) bool { return v != v }
