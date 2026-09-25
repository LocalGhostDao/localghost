package oracled

// What the model is shown, and what llama-server says when it refuses.
//
// llama-server reads images with stb_image (through libmtmd): JPEG, PNG, GIF and BMP. framed writes
// its previews as JPEG and converts them to WebP when cwebp is on the box, and WebP is not on that
// list, so a caption of a WebP preview came back "http 400" , with the reason in a body nobody read,
// five times, then parked. Now: an image the model cannot read is converted first (dwebp, which the
// same package as cwebp installs, else ffmpeg), and every refusal carries llama-server's own words.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// imageKind names an image by its first bytes (the file name says nothing reliable).
func imageKind(raw []byte) string {
	switch {
	case len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF:
		return "jpeg"
	case len(raw) >= 8 && bytes.Equal(raw[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}):
		return "png"
	case len(raw) >= 6 && (string(raw[:6]) == "GIF87a" || string(raw[:6]) == "GIF89a"):
		return "gif"
	case len(raw) >= 2 && raw[0] == 'B' && raw[1] == 'M':
		return "bmp"
	case len(raw) >= 12 && string(raw[0:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return "webp"
	case len(raw) >= 12 && string(raw[4:8]) == "ftyp":
		switch string(raw[8:12]) {
		case "heic", "heix", "hevc", "heim", "heis", "mif1", "msf1":
			return "heic"
		case "avif", "avis":
			return "avif"
		}
	}
	return "unknown"
}

// modelReads is what stb_image decodes.
func modelReads(kind string) bool {
	return kind == "jpeg" || kind == "png" || kind == "gif" || kind == "bmp"
}

// converters turn a file the model cannot read into one it can; tried in order. Variables so tests
// can stand in for the binaries.
var converters = []func(ctx context.Context, path, kind string) ([]byte, error){dwebpPNG, ffmpegJPEG}

// dwebpPNG decodes WebP with dwebp (Debian's `webp` package, the one that brings the cwebp framed uses).
func dwebpPNG(ctx context.Context, path, kind string) ([]byte, error) {
	if kind != "webp" {
		return nil, errors.New("dwebp reads only webp")
	}
	bin, err := exec.LookPath("dwebp")
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp("", "lg-img-*.png")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	cmd := exec.CommandContext(ctx, bin, "-quiet", path, "-o", tmp.Name())
	cmd.WaitDelay = 5 * time.Second
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("dwebp: %v %s", err, strings.TrimSpace(string(out)))
	}
	return os.ReadFile(tmp.Name())
}

// ffmpegJPEG decodes anything ffmpeg can (WebP, and HEIC/AVIF on builds that have them) to a JPEG.
func ffmpegJPEG(ctx context.Context, path, kind string) ([]byte, error) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, err
	}
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "-v", "error", "-i", path, "-frames:v", "1", "-f", "image2", "-c:v", "mjpeg", "pipe:1")
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v %s", err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// imageForModel is the data URI of the image at path in a format llama-server reads, converting when
// it has to. An image nothing here can convert is an error that says so , the job parks with a reason.
func imageForModel(ctx context.Context, path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read image: %w", err)
	}
	kind := imageKind(raw)
	if modelReads(kind) {
		return dataURI(raw), nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var tried []string
	for _, conv := range converters {
		b, err := conv(ctx, path, kind)
		if err == nil && modelReads(imageKind(b)) {
			return dataURI(b), nil
		}
		if err != nil {
			tried = append(tried, err.Error())
		}
	}
	return "", fmt.Errorf("image is %s, which llama-server cannot read, and it could not be converted (%s) , install the webp package (dwebp) or ffmpeg", kind, strings.Join(tried, "; "))
}

// llamaRefusal is llama-server's reason for a non-200: the message of its JSON error body, or the
// start of whatever it sent.
func llamaRefusal(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var e struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
		if e.Error.Type != "" {
			return e.Error.Message + " (" + e.Error.Type + ")"
		}
		return e.Error.Message
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	if s == "" {
		s = "no reason given"
	}
	return s
}

// noVision says whether a refusal means the server cannot take images at all (started without its
// projector) rather than this image being the problem.
func noVision(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "image input is not supported") ||
		(strings.Contains(m, "multimodal") && strings.Contains(m, "not supported"))
}

// refusalError is the error for a non-200 from llama-server. A server that cannot see images at all
// answers "no vision: …", which searchd holds its caption lane on instead of failing every job.
func refusalError(what string, resp *http.Response) error {
	msg := llamaRefusal(resp)
	if noVision(msg) {
		return fmt.Errorf("no vision: llama-server takes no images (started without the mmproj projector?): %s", msg)
	}
	return fmt.Errorf("%s: http %d: %s", what, resp.StatusCode, msg)
}

// dataURI is raw as a data: URI with its real media type.
func dataURI(raw []byte) string {
	mime := "image/jpeg"
	switch imageKind(raw) {
	case "png":
		mime = "image/png"
	case "gif":
		mime = "image/gif"
	case "bmp":
		mime = "image/bmp"
	case "webp":
		mime = "image/webp"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
}
