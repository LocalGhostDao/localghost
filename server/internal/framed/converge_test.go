package framed

import (
	"strings"
	"testing"
)

func TestTallyCountsStages(t *testing.T) {
	rows := []Audit{
		// fully converged photo
		{Hash: "a", Kind: "photo", PreviewPath: "p", ThumbPath: "t", PipeVer: PipelineVersion, Described: true, Titled: true, Tagged: true},
		// video behind the pipeline, everything else present
		{Hash: "b", Kind: "video", PreviewPath: "p", ThumbPath: "t", PipeVer: PipelineVersion - 1, Described: true, Titled: true, Tagged: true},
		// photo at the latest pipeline, no preview, undescribed
		{Hash: "c", Kind: "photo", PipeVer: PipelineVersion, Titled: true, Tagged: true},
		// video described but untitled and untagged
		{Hash: "d", Kind: "video", PreviewPath: "p", ThumbPath: "t", PipeVer: PipelineVersion, Described: true},
		// unknown blob: counted, never staged
		{Hash: "e", Kind: "unknown"},
	}
	r := tally(rows)
	if r.Photos != 2 || r.Videos != 2 || r.Other != 1 {
		t.Fatalf("kinds: %+v", r)
	}
	if r.AtLatest != 1 {
		t.Fatalf("AtLatest = %d, want 1", r.AtLatest)
	}
	if r.Behind != 1 || r.NoPreview != 1 || r.NoDescription != 1 || r.NoTitle != 1 || r.NoTags != 1 {
		t.Fatalf("gaps: %+v", r)
	}
}

func TestConvergeReportString(t *testing.T) {
	if s := (ConvergeReport{}).String(); s != "archive empty" {
		t.Fatalf("empty: %q", s)
	}
	ok := ConvergeReport{Photos: 3, Videos: 1, AtLatest: 4}
	if s := ok.String(); !strings.Contains(s, "all at the latest stage") || !strings.Contains(s, "4 frames") {
		t.Fatalf("healthy: %q", s)
	}
	bad := ConvergeReport{Photos: 3, Videos: 1, AtLatest: 2, NoDescription: 2, Notified: 2}
	if s := bad.String(); strings.Contains(s, "all at the latest stage") || !strings.Contains(s, "undescribed: 2") {
		t.Fatalf("unhealthy: %q", s)
	}
}

func TestRenderFor(t *testing.T) {
	if got := renderFor("photo", "/a/h.jpg", "/p/h.webp"); got != "/p/h.webp" {
		t.Fatalf("photo with preview: %q", got)
	}
	if got := renderFor("photo", "/a/h.heic", ""); got != "/a/h.heic" {
		t.Fatalf("photo without preview falls back to the original: %q", got)
	}
	if got := renderFor("video", "/a/h.mp4", ""); got != "" {
		t.Fatalf("video without a grab has nothing to show: %q", got)
	}
	if got := renderFor("video", "/a/h.mp4", "/p/h.jpg"); got != "/p/h.jpg" {
		t.Fatalf("video with a grab: %q", got)
	}
	if got := renderFor("unknown", "/a/h.bin", ""); got != "" {
		t.Fatalf("unknown: %q", got)
	}
}
