package framed

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// A HEIC the old sniff filed as an MP4 video (its brands outside the few the major-brand check
// knew) is a photo again on re-derive: the file renamed to .heic, the row's kind, type and path
// following, and its journal line no longer saying "video archived".
func TestRederiveTurnsAMisfiledHEICBackIntoAPhoto(t *testing.T) {
	dir := os.Getenv("GHOST_PG_SOCKET_DIR")
	if dir == "" {
		t.Skip("GHOST_PG_SOCKET_DIR not set; no Postgres to test against")
	}
	port := 5432
	if p, err := strconv.Atoi(os.Getenv("GHOST_PG_PORT")); err == nil {
		port = p
	}
	user := os.Getenv("GHOST_PG_USER")
	if user == "" {
		user = "postgres"
	}
	admin := poltergres.NewReadWrite(dir, port, user, "", "postgres")
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_rekind WITH (FORCE)")
	if err := admin.ExecSimple("CREATE DATABASE lgtest_rekind"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_rekind")
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := hw.ConvergeSchema(db, lg); err != nil {
		t.Fatal(err)
	}
	store := NewStoreDB(db)

	root := t.TempDir()
	dirs := Dirs{Archive: filepath.Join(root, "archive"), Preview: filepath.Join(root, "preview"), Thumb: filepath.Join(root, "thumb"), Damaged: filepath.Join(root, "damaged")}
	for _, d := range []string{dirs.Archive, dirs.Preview, dirs.Thumb, dirs.Damaged} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := NewPipeline(dirs, store, lg)

	// the file as the old pipeline left it: a 10-bit HEIF under a video's name
	hash := "ab12ab12ab12ab12"
	day := filepath.Join(dirs.Archive, "2024", "10", "02")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(day, hash+".mp4")
	if err := os.WriteFile(old, ftyp("hevx", []string{"mif1", "heix", "hevx"}, "meta"), 0o644); err != nil {
		t.Fatal(err)
	}
	taken := int64(1727868480)
	when := time.Unix(taken, 0).UTC().Format("Mon Jan 2 2006, 15:04") // Wed Oct 2 2024, 11:28
	if err := store.InsertFrame(Frame{Hash: hash, TakenAt: taken, ArchivePath: old, Bytes: 40, Source: "phone", ReceivedAt: taken, Kind: "video", MIME: "video/mp4", TakenSrc: "hint"}); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertJournal(hash, taken, "video archived , "+when, "A video from "+when+"."); err != nil {
		t.Fatal(err)
	}

	f, _, err := p.derive(old, false)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	want := filepath.Join(day, hash+".heic")
	if f.Kind != "photo" || f.MIME != "image/heic" || f.ArchivePath != want {
		t.Fatalf("derived %s %s at %s", f.Kind, f.MIME, f.ArchivePath)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("not renamed: %v", err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatalf("the .mp4 name is still there")
	}
	rows, err := db.Query("SELECT kind, mime, archive_path, pipe_ver FROM frames WHERE hash = $1", hash)
	if err != nil || len(rows.Vals) != 1 {
		t.Fatalf("row: %v %d", err, len(rows.Vals))
	}
	v := rows.Vals[0]
	if *v[0] != "photo" || *v[1] != "image/heic" || *v[2] != want || *v[3] != strconv.Itoa(PipelineVersion) {
		t.Fatalf("row converged to %s %s %s v%s", *v[0], *v[1], *v[2], *v[3])
	}
	rows, err = db.Query("SELECT title, body FROM journal_entries WHERE source = 'ghost.framed' AND ref = $1", hash)
	if err != nil || len(rows.Vals) != 1 {
		t.Fatalf("journal: %v %d", err, len(rows.Vals))
	}
	if got := *rows.Vals[0][0]; got != "photo archived , "+when {
		t.Fatalf("journal title %q", got)
	}
	if got := *rows.Vals[0][1]; got != "A photo from "+when+"." {
		t.Fatalf("journal body %q", got)
	}

	// a second derive is a no-op: same name, same row, and the journal line is left alone even
	// though its wording would differ in a detail (a place learned later is not a kind change)
	if _, _, err := p.derive(want, false); err != nil {
		t.Fatalf("derive again: %v", err)
	}
	if err := store.InsertJournal(hash, taken, "photo at Faro", "A photo taken at Faro on "+when+"."); err != nil {
		t.Fatal(err)
	}
	rows, _ = db.Query("SELECT title FROM journal_entries WHERE source = 'ghost.framed' AND ref = $1", hash)
	if got := *rows.Vals[0][0]; got != "photo archived , "+when {
		t.Fatalf("journal title rewritten without a kind change: %q", got)
	}
	// a real clip keeps its name and kind
	clip := filepath.Join(day, "cd34cd34cd34cd34.mp4")
	if err := os.WriteFile(clip, ftyp("isom", []string{"isom", "mp42", "avc1"}, "moov"), 0o644); err != nil {
		t.Fatal(err)
	}
	if f, _, err := p.derive(clip, false); err != nil || f.Kind != "video" || f.ArchivePath != clip {
		t.Fatalf("clip: %v %s %s", err, f.Kind, f.ArchivePath)
	}
}
