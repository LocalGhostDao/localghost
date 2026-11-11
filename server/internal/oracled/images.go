package oracled

// What the model is shown, and what llama-server says when it refuses.
//
// llama-server reads images with stb_image (through libmtmd): JPEG, PNG, GIF and BMP. framed writes
// its previews as JPEG and converts them to WebP when cwebp is on the box, and WebP is not on that
// list, so a caption of a WebP preview came back "http 400" , with the reason in a body nobody read,
// five times, then parked. Now: an image the model cannot read is converted first (dwebp, which the
// same package as cwebp installs, else ffmpeg), and every refusal carries llama-server's own words.
//
// SIZE (30 Sep 2026). The mirror's llama.cpp v0.5.0 ABORTS on a full-size phone photo (a 3.3 MB
// JPEG killed it mid-caption on 29 Sep; a tiny one was fine), taking chat down with it. So nothing
// reaches it whole any more: every image is decoded here and sent as a baseline JPEG, upright (its
// EXIF orientation applied, which stb_image ignores), its long side at most ImageMaxSide. An image
// that cannot be decoded here or by a converter is not sent at all; the job parks with the reason.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // decoders for image.Decode
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/exif"
	"github.com/LocalGhostDao/localghost/server/internal/imgfit"
)

// ImageMaxSide is the longest side, in pixels, of any image the model is shown (conf imageMaxSide).
// 1024 keeps a photo's detail for a caption and its image tokens well inside one batch.
var ImageMaxSide = 1024

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
	// "-o -": the PNG comes back on a pipe. The picture is decoded from where it lies on the
	// encrypted volume and never written anywhere else, not even a temporary file.
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "-quiet", path, "-o", "-")
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("dwebp: %v %s", err, strings.TrimSpace(errb.String()))
	}
	if out.Len() == 0 {
		return nil, errors.New("dwebp produced nothing")
	}
	return out.Bytes(), nil
}

// ffmpegJPEG decodes anything ffmpeg can (WebP, and HEIC/AVIF on builds that have them) to a JPEG.
func ffmpegJPEG(ctx context.Context, path, kind string) ([]byte, error) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, err
	}
	var out, errb bytes.Buffer
	// scaled on the way out, so a HEIC or a JPEG Go refuses is small before it is decoded again here
	side := strconv.Itoa(ImageMaxSide)
	scale := "scale=w='min(iw," + side + ")':h='min(ih," + side + ")':force_original_aspect_ratio=decrease"
	cmd := exec.CommandContext(ctx, bin, "-v", "error", "-i", path, "-frames:v", "1", "-vf", scale, "-f", "image2", "-c:v", "mjpeg", "pipe:1")
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v: %s", err, firstLine(errb.String()))
	}
	return out.Bytes(), nil
}

// firstLine is the first thing ffmpeg said, without its "[mjpeg @ 0x55…]" prefix: the reason, not
// the forty lines of its decoder giving up (they filled the logs, a screen per failed caption).
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		for strings.HasPrefix(line, "[") {
			i := strings.Index(line, "] ")
			if i < 0 {
				break
			}
			line = strings.TrimSpace(line[i+2:])
		}
		if line != "" {
			if len(line) > 120 {
				line = line[:120]
			}
			return line
		}
	}
	return "no reason given"
}

// imageForModel is the data URI of the image at path as the model is shown it: decoded, turned
// upright, fitted to ImageMaxSide and sent as a JPEG. What the standard library cannot decode (WebP,
// HEIC, the odd JPEG it refuses) goes through a converter first. An image nothing here can decode is
// an error that says so , the job parks with a reason, and the engine never sees the file whole.
func imageForModel(ctx context.Context, path string) (string, error) {
	// past the box's limits it is not read, converted or sent (imgfit/limits.go)
	if err := imgfit.CheckFile(path); err != nil {
		var tl imgfit.ErrTooLarge
		if errors.As(err, &tl) {
			return "", err
		}
		return "", fmt.Errorf("read image: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read image: %w", err)
	}
	kind := imageKind(raw)
	var tried []string
	if modelReads(kind) {
		b, err := fitDecoded(raw, ImageMaxSide)
		if err == nil {
			return dataURI(b), nil
		}
		tried = append(tried, "decode: "+err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	missing := false // a converter not installed: installing it might read the image
	for _, conv := range converters {
		b, err := conv(ctx, path, kind)
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				missing = true
			}
			tried = append(tried, err.Error())
			continue
		}
		f, err := fitDecoded(b, ImageMaxSide)
		if err != nil {
			tried = append(tried, "decode converted: "+err.Error())
			continue
		}
		return dataURI(f), nil
	}
	if !missing && modelReads(kind) {
		// Go's decoder and ffmpeg both read the file and both refused it: no install will help.
		// searchd finishes the job without a caption on these words ("the file looks damaged").
		return "", fmt.Errorf("image is %s and could not be decoded by anything on the box, the file looks damaged (%s); it is not sent whole", kind, strings.Join(tried, "; "))
	}
	return "", fmt.Errorf("image is %s and could not be decoded to fit the model (%s) , install the webp package (dwebp) or ffmpeg; it is not sent whole, a full-size photo crashes llama-server", kind, strings.Join(tried, "; "))
}

// chatImageMax is the largest attachment a chat turn may carry, before decoding.
const chatImageMax = 32 << 20

// chatImage is a chat attachment (base64 from the phone) as the model is shown it: a data URI
// of the decoded, fitted, re-encoded picture. Only what the standard library decodes is taken;
// anything else is an error the chat shows.
func chatImage(b64 string) (string, error) {
	if base64.StdEncoding.DecodedLen(len(b64)) > chatImageMax {
		return "", fmt.Errorf("the attached image is over %d MB", chatImageMax>>20)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return "", fmt.Errorf("the attached image is not readable (base64)")
	}
	if kind := imageKind(raw); !modelReads(kind) {
		return "", fmt.Errorf("the attached image is %s, which the chat cannot show the model; send a JPEG or PNG", kind)
	}
	fitted, err := fitDecoded(raw, ImageMaxSide)
	if err != nil {
		return "", fmt.Errorf("the attached image could not be decoded: %v", err)
	}
	return dataURI(fitted), nil
}

// fitDecoded decodes raw (JPEG, PNG or GIF), applies a JPEG's EXIF orientation, scales it so its
// long side is at most maxSide, and encodes a baseline JPEG.
func fitDecoded(raw []byte, maxSide int) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if err := imgfit.CheckPixels(cfg.Width, cfg.Height); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	orient := 0
	if imageKind(raw) == "jpeg" {
		orient = exif.Parse(raw).Orientation
	}
	out := imgfit.Orient(imgfit.Downscale(img, maxSide), orient)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 90}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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
