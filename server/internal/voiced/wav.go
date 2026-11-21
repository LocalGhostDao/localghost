// Package voiced is ghost.voiced's logic: the person's own voice notes, archived on the volume and
// transcribed on the box by whisper.cpp when it has a speech model. Nothing here reaches a network.
package voiced

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"
)

// WavInfo is what a RIFF/WAVE file says about its samples. Only integer PCM is accepted (the phone
// writes 16-bit mono at 16 kHz); DataLen is the sample bytes that are really in the file, so a
// recording whose header was never finished (the phone died mid-note) still reads to its end.
type WavInfo struct {
	Rate     int
	Channels int
	Bits     int
	DataOff  int64
	DataLen  int64
}

// Duration of the samples.
func (w WavInfo) Duration() time.Duration {
	frame := int64(w.Channels * w.Bits / 8)
	if frame <= 0 || w.Rate <= 0 {
		return 0
	}
	return time.Duration(w.DataLen/frame) * time.Second / time.Duration(w.Rate)
}

// ReadWavInfo walks the RIFF chunks to fmt and data.
func ReadWavInfo(f *os.File) (WavInfo, error) {
	var w WavInfo
	st, err := f.Stat()
	if err != nil {
		return w, err
	}
	size := st.Size()
	var hdr [12]byte
	if _, err := f.ReadAt(hdr[:], 0); err != nil {
		return w, fmt.Errorf("not a WAV file: %w", err)
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return w, errors.New("not a WAV file (no RIFF/WAVE header)")
	}
	off := int64(12)
	haveFmt := false
	for off+8 <= size {
		var ch [8]byte
		if _, err := f.ReadAt(ch[:], off); err != nil {
			return w, err
		}
		id := string(ch[0:4])
		n := int64(binary.LittleEndian.Uint32(ch[4:8]))
		body := off + 8
		switch id {
		case "fmt ":
			if n < 16 {
				return w, errors.New("WAV fmt chunk too short")
			}
			var fm [40]byte
			m := n
			if m > 40 {
				m = 40
			}
			if _, err := f.ReadAt(fm[:m], body); err != nil {
				return w, err
			}
			format := binary.LittleEndian.Uint16(fm[0:2])
			w.Channels = int(binary.LittleEndian.Uint16(fm[2:4]))
			w.Rate = int(binary.LittleEndian.Uint32(fm[4:8]))
			w.Bits = int(binary.LittleEndian.Uint16(fm[14:16]))
			// WAVE_FORMAT_EXTENSIBLE: the sub-format GUID's first two bytes are the real tag
			if format == 0xFFFE && m >= 26 {
				format = binary.LittleEndian.Uint16(fm[24:26])
			}
			if format != 1 {
				return w, fmt.Errorf("WAV format %d is not integer PCM", format)
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return w, errors.New("WAV data before fmt")
			}
			w.DataOff = body
			w.DataLen = n
			// an unfinished header (0, or the 0xFFFFFFFF placeholder) or one that claims more than
			// the file holds: the samples run to the end of the file
			if n == 0 || n == 0xFFFFFFFF || body+n > size {
				w.DataLen = size - body
			}
			if w.Channels < 1 || w.Channels > 8 || w.Rate < 4000 || w.Rate > 192000 || (w.Bits != 16 && w.Bits != 8 && w.Bits != 24 && w.Bits != 32) {
				return w, fmt.Errorf("WAV %d Hz, %d channels, %d bit: not something to transcribe", w.Rate, w.Channels, w.Bits)
			}
			frame := int64(w.Channels * w.Bits / 8)
			w.DataLen -= w.DataLen % frame
			return w, nil
		}
		off = body + n + n%2 // chunks are word aligned
	}
	return w, errors.New("WAV has no data chunk")
}

// WavHeader is the 44-byte header of a plain PCM WAV.
func WavHeader(rate, channels, bits int, dataLen uint32) []byte {
	h := make([]byte, 44)
	copy(h[0:4], "RIFF")
	binary.LittleEndian.PutUint32(h[4:8], 36+dataLen)
	copy(h[8:16], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:20], 16)
	binary.LittleEndian.PutUint16(h[20:22], 1)
	binary.LittleEndian.PutUint16(h[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(h[24:28], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:32], uint32(rate*channels*bits/8))
	binary.LittleEndian.PutUint16(h[32:34], uint16(channels*bits/8))
	binary.LittleEndian.PutUint16(h[34:36], uint16(bits))
	copy(h[36:40], "data")
	binary.LittleEndian.PutUint32(h[40:44], dataLen)
	return h
}

// SpeechRate is what whisper.cpp reads: 16 kHz, mono, 16-bit.
const SpeechRate = 16000

// IsSpeechFormat: the file can go to whisper as it is.
func (w WavInfo) IsSpeechFormat() bool {
	return w.Rate == SpeechRate && w.Channels == 1 && w.Bits == 16
}

// ToSpeechFormat writes src's samples as a 16 kHz mono 16-bit WAV at dst: channels averaged, the
// rate changed by linear interpolation. The phone records in that format already; this is for a
// phone whose microphone would not open at 16 kHz, and for old whisper.cpp builds that read only
// 16 kHz WAV. Voice notes are capped at 128 MB, so the samples are read whole.
func ToSpeechFormat(src *os.File, info WavInfo, dst string) error {
	raw := make([]byte, info.DataLen)
	if _, err := src.ReadAt(raw, info.DataOff); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	bps := info.Bits / 8
	frames := len(raw) / (bps * info.Channels)
	mono := make([]float64, frames)
	for i := 0; i < frames; i++ {
		var sum float64
		for c := 0; c < info.Channels; c++ {
			p := raw[(i*info.Channels+c)*bps:]
			sum += sample(p, info.Bits)
		}
		mono[i] = sum / float64(info.Channels)
	}
	outN := frames
	if info.Rate != SpeechRate && frames > 0 {
		outN = int(int64(frames) * SpeechRate / int64(info.Rate))
	}
	out := make([]byte, 44+outN*2)
	copy(out, WavHeader(SpeechRate, 1, 16, uint32(outN*2)))
	step := float64(info.Rate) / SpeechRate
	for j := 0; j < outN; j++ {
		var v float64
		if info.Rate == SpeechRate {
			v = mono[j]
		} else {
			x := float64(j) * step
			i := int(x)
			fr := x - float64(i)
			a := mono[i]
			b := a
			if i+1 < frames {
				b = mono[i+1]
			}
			v = a + (b-a)*fr
		}
		if v > 1 {
			v = 1
		} else if v < -1 {
			v = -1
		}
		binary.LittleEndian.PutUint16(out[44+j*2:], uint16(int16(v*32767)))
	}
	return os.WriteFile(dst, out, 0o600)
}

// SilenceFloor: a file whose loudest tenth of a second is below this holds no voice. About
// -48 dB of full scale: a phone's microphone in a quiet room sits near -60, a quiet voice at
// arm's length near -35. A covered microphone, or a tap by mistake, is under it.
const SilenceFloor = 0.004

// LoudestTenth is the loudest tenth of a second of a WAV: the root of the mean square of its
// samples, 0..1 of full scale, the channels together. It is what tells a recording of nothing
// from a recording of someone, before a speech model is asked to find words in it (a speech
// model handed silence finds some).
func LoudestTenth(f *os.File, info WavInfo) (float64, error) {
	bps := info.Bits / 8
	frame := bps * info.Channels
	if frame <= 0 || info.Rate <= 0 {
		return 0, errors.New("not a WAV with samples")
	}
	per := info.Rate / 10 * frame // the bytes of a tenth of a second
	if per <= 0 {
		return 0, errors.New("not a WAV with samples")
	}
	buf := make([]byte, per)
	var loudest float64
	for off := int64(0); off < info.DataLen; off += int64(per) {
		want := int64(per)
		if rest := info.DataLen - off; rest < want {
			want = rest - rest%int64(frame)
		}
		if want < int64(frame) {
			break
		}
		n, err := f.ReadAt(buf[:want], info.DataOff+off)
		n -= n % frame
		if n < frame {
			if err != nil && !errors.Is(err, io.EOF) {
				return loudest, err
			}
			break // a file shorter than its header says: what is there has been read
		}
		var sum float64
		for i := 0; i+bps <= n; i += bps {
			v := sample(buf[i:], info.Bits)
			sum += v * v
		}
		if rms := math.Sqrt(sum / float64(n/bps)); rms > loudest {
			loudest = rms
		}
	}
	return loudest, nil
}

// sample reads one little-endian integer sample as -1..1.
func sample(p []byte, bits int) float64 {
	switch bits {
	case 8:
		return (float64(p[0]) - 128) / 128 // 8-bit WAV is unsigned
	case 16:
		return float64(int16(binary.LittleEndian.Uint16(p))) / 32768
	case 24:
		v := int32(uint32(p[0])<<8|uint32(p[1])<<16|uint32(p[2])<<24) >> 8
		return float64(v) / 8388608
	case 32:
		return float64(int32(binary.LittleEndian.Uint32(p))) / 2147483648
	}
	return 0
}
