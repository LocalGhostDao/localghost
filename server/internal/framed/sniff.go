package framed

import "bytes"

// MediaKind is what a spooled file actually is, decided by its CONTENT, not its name. Uploads arrive
// as <nanos>-<rand> with no extension (secd deliberately never inspects bytes), so framed sniffs the
// magic bytes here. This is authoritative: a phone that mislabels, or a future client that sends no
// hint at all, is still classified correctly.
type MediaKind int

const (
	KindUnknown MediaKind = iota
	KindPhoto
	KindVideo
)

// SniffResult is the detected type plus the canonical extension to archive it under.
type SniffResult struct {
	Kind MediaKind
	Ext  string // includes the dot, e.g. ".jpg", ".mp4"; ".bin" when truly unknown
	MIME string // best-effort, for the record; "" when unknown
}

// Sniff identifies a media file from its leading bytes. It recognises the formats a phone camera
// actually produces , JPEG, PNG, HEIF/HEIC, WebP, GIF for stills; MP4, QuickTime MOV, and the common
// ISO-BMFF brands plus WebM/Matroska for video , and falls back to KindUnknown/.bin for anything else
// so nothing is ever silently mis-archived.
func Sniff(b []byte) SniffResult {
	if len(b) < 12 {
		return SniffResult{KindUnknown, ".bin", ""}
	}

	// --- stills ---
	switch {
	case b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF: // JPEG SOI
		return SniffResult{KindPhoto, ".jpg", "image/jpeg"}
	case bytes.HasPrefix(b, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return SniffResult{KindPhoto, ".png", "image/png"}
	case bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a")):
		return SniffResult{KindPhoto, ".gif", "image/gif"}
	case bytes.Equal(b[0:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return SniffResult{KindPhoto, ".webp", "image/webp"}
	}

	// --- ISO base media file format (MP4/MOV/HEIF share the ftyp box) ---
	// Bytes 4..8 are "ftyp"; the 4-byte major brand at 8..12 disambiguates still (HEIC) vs video (MP4/MOV).
	if bytes.Equal(b[4:8], []byte("ftyp")) {
		brand := string(b[8:12])
		switch brand {
		case "heic", "heix", "hevc", "heim", "heis", "mif1", "msf1":
			return SniffResult{KindPhoto, ".heic", "image/heic"}
		case "avif", "avis":
			return SniffResult{KindPhoto, ".avif", "image/avif"}
		case "qt  ":
			return SniffResult{KindVideo, ".mov", "video/quicktime"}
		default:
			// isom, mp41, mp42, iso2, iso5, M4V, dash, etc. , treat as MP4 video.
			return SniffResult{KindVideo, ".mp4", "video/mp4"}
		}
	}

	// --- Matroska / WebM (EBML header) ---
	if bytes.HasPrefix(b, []byte{0x1A, 0x45, 0xDF, 0xA3}) {
		// Could be .mkv or .webm; .webm is the phone-relevant one. Default to .webm.
		return SniffResult{KindVideo, ".webm", "video/webm"}
	}

	// --- 3GPP (older phone video) also uses ftyp, caught above; AVI as a fallback ---
	if bytes.Equal(b[0:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("AVI ")) {
		return SniffResult{KindVideo, ".avi", "video/x-msvideo"}
	}

	return SniffResult{KindUnknown, ".bin", ""}
}
