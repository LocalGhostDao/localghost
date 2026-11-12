package imgfit

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strconv"
)

// A PICTURE OR A CLIP FROM OUTSIDE, READ BY ANOTHER PROGRAM (second audit, 12 Nov 2026). What
// Go decodes is held to MaxBytes and MaxPixels before a pixel is read. What Go cannot read
// (a WebP, a HEIC, a damaged JPEG, every clip) goes to ffmpeg or dwebp, and those were given
// the file with nothing said: no limit on the picture's size (a WebP of a few kilobytes can
// claim 16383 x 16383, a gigabyte once decoded), none on what came back down the pipe, none
// on how long it took (framed's two had no deadline: one clip that never finishes stopped
// every photo behind it), and ffmpeg free to open whatever a file told it to. These are the
// same limits for the programs.

// FFmpegMaxAlloc is the largest single block ffmpeg may allocate while it reads a file.
const FFmpegMaxAlloc = 1 << 30

// MaxPipeBytes is the most an outside decoder may send back (a decoded frame as a JPEG or a
// PNG: MaxPixels at four bytes each is 240 MB before any compression).
const MaxPipeBytes = 256 << 20

// FFmpegInput is how ffmpeg is given a file from outside, ending in "-i path": local files only
// (a container that names another address, by a playlist or a reference inside it, is not
// followed anywhere), no single allocation past FFmpegMaxAlloc, no frame past MaxPixels.
// Whatever must come before "-i" (a seek) goes before these.
func FFmpegInput(path string) []string {
	return []string{"-protocol_whitelist", "file", "-max_alloc", strconv.Itoa(FFmpegMaxAlloc),
		"-max_pixels", strconv.Itoa(MaxPixels), "-i", path}
}

// ErrPipeFull is an outside decoder that sent back more than MaxPipeBytes.
var ErrPipeFull = errors.New("the decoder sent back more than it may")

// Pipe collects what an outside decoder writes, up to Max bytes; one byte more and the write
// fails, which ends the program (its pipe is closed) instead of filling memory. It is a writer
// and nothing else on purpose: a bytes.Buffer set as a command's output is filled through its
// ReadFrom, which no limit on Write ever sees.
type Pipe struct {
	buf bytes.Buffer
	Max int
}

// NewPipe is a Pipe of MaxPipeBytes.
func NewPipe() *Pipe { return &Pipe{Max: MaxPipeBytes} }

func (p *Pipe) Write(b []byte) (int, error) {
	if p.buf.Len()+len(b) > p.Max {
		return 0, ErrPipeFull
	}
	return p.buf.Write(b)
}

// Len is how many bytes came back; Bytes is what they were; Reader reads them.
func (p *Pipe) Len() int          { return p.buf.Len() }
func (p *Pipe) Bytes() []byte     { return p.buf.Bytes() }
func (p *Pipe) Reader() io.Reader { return bytes.NewReader(p.buf.Bytes()) }

var _ io.Writer = (*Pipe)(nil)

// WebPSize reads a WebP's width and height from its first bytes (30 are enough), without
// decoding anything: the three shapes a WebP has (VP8 lossy, VP8L lossless, VP8X extended, whose
// canvas is what a decoder allocates). ok is false for anything else.
func WebPSize(head []byte) (w, h int, ok bool) {
	if len(head) < 30 || string(head[0:4]) != "RIFF" || string(head[8:12]) != "WEBP" {
		return 0, 0, false
	}
	switch string(head[12:16]) {
	case "VP8 ":
		if head[23] != 0x9d || head[24] != 0x01 || head[25] != 0x2a {
			return 0, 0, false
		}
		w = int(binary.LittleEndian.Uint16(head[26:28]) & 0x3fff)
		h = int(binary.LittleEndian.Uint16(head[28:30]) & 0x3fff)
	case "VP8L":
		if head[20] != 0x2f {
			return 0, 0, false
		}
		b := binary.LittleEndian.Uint32(head[21:25])
		w = int(b&0x3fff) + 1
		h = int((b>>14)&0x3fff) + 1
	case "VP8X":
		w = int(uint32(head[24])|uint32(head[25])<<8|uint32(head[26])<<16) + 1
		h = int(uint32(head[27])|uint32(head[28])<<8|uint32(head[29])<<16) + 1
	default:
		return 0, 0, false
	}
	return w, h, w > 0 && h > 0
}

// CheckWebPFile: a WebP on disk is one to hand to a decoder. A file that is not a WebP, or whose
// header does not read, is an error too: nothing is decoded on a guess.
func CheckWebPFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 30)
	if _, err := io.ReadFull(f, head); err != nil {
		return errors.New("not a WebP (too short)")
	}
	w, h, ok := WebPSize(head)
	if !ok {
		return errors.New("not a WebP this box can size")
	}
	return CheckPixels(w, h)
}
