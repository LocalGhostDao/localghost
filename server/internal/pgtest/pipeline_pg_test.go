// Package pgtest runs the stock-take's SQL against a REAL Postgres: the schema registry, framed's
// InsertFrame convergence and Audit, searchd's ensure-path queries, and hw's progress feed. It
// skips unless GHOST_PG_SOCKET_DIR points at a running server (GHOST_PG_PORT, GHOST_PG_USER,
// default 5432 / postgres), so the normal suite is untouched and a developer with a local
// Postgres gets the real thing:
//
//	GHOST_PG_SOCKET_DIR=/tmp GHOST_PG_PORT=55432 GHOST_PG_USER=claude go test ./internal/pgtest/
package pgtest

import (
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/framed"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/search"
	"github.com/LocalGhostDao/localghost/server/internal/searchsql"
)

// fresh creates a database named after the test (the client keeps its connection open, so two
// tests cannot share one name) with the full schema applied.
func fresh(t *testing.T) *poltergres.ReadWrite {
	t.Helper()
	name := "lgtest_" + strings.ToLower(t.Name())
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
	if err := admin.Ping(); err != nil {
		t.Fatalf("postgres unreachable: %v", err)
	}
	if err := admin.ExecSimple("DROP DATABASE IF EXISTS " + name); err != nil {
		t.Fatal(err)
	}
	if err := admin.ExecSimple("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", name)
	if _, err := hw.ConvergeSchema(db, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if err := db.ExecSimple(searchsql.SchemaCore); err != nil {
		t.Fatalf("search schema: %v", err)
	}
	if err := db.ExecSimple(searchsql.SchemaNoVector); err != nil {
		t.Fatalf("search schema (no vector): %v", err)
	}
	return db
}

// An existing box has frames without described_at and no daemon_state: the registry must add
// both on the next unlock, which is the path every real box takes.
func TestSchemaUpgradeAddsStockTakeColumns(t *testing.T) {
	db := fresh(t)
	if err := db.ExecSimple("ALTER TABLE frames DROP COLUMN described_at; ALTER TABLE frames DROP COLUMN pipe_ver; DROP TABLE daemon_state"); err != nil {
		t.Fatal(err)
	}
	if _, err := hw.ConvergeSchema(db, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))); err != nil {
		t.Fatalf("schema upgrade: %v", err)
	}
	if err := db.Exec(`INSERT INTO frames (hash, archive_path) VALUES ('x', '/x')`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT pipe_ver, described_at FROM frames WHERE hash = 'x'`)
	if err != nil || len(rows.Vals) != 1 || *rows.Vals[0][0] != "0" || *rows.Vals[0][1] != "0" {
		t.Fatalf("columns not re-added with defaults: %v %v", rows, err)
	}
	if err := db.Exec(`INSERT INTO daemon_state (daemon, key, value, updated_at) VALUES ('t', 'k', '{}', 1)`); err != nil {
		t.Fatalf("daemon_state not re-created: %v", err)
	}
}

func TestStockTakeAgainstPostgres(t *testing.T) {
	db := fresh(t)
	fs := framed.NewStoreDB(db)
	ss := search.NewStore(db)
	ing := &search.Ingester{Store: ss, Log: slog.Default()}
	now := time.Now().UTC().Unix()

	// Three frames: a described-and-tagged photo, a bare video, and an old-pipeline photo.
	const h1, h2, h3 = "11111111111111111111111111111111", "22222222222222222222222222222222", "33333333333333333333333333333333"
	mk := func(hash, kind, prev string) framed.Frame {
		return framed.Frame{Hash: hash, TakenAt: now - 86400, ArchivePath: "/a/" + hash + ".jpg", PreviewPath: prev,
			ThumbPath: prev, Kind: kind, MIME: "image/jpeg", TakenSrc: "exif", Source: "test", ReceivedAt: now}
	}
	for _, f := range []framed.Frame{mk(h1, "photo", "/p/"+h1+".webp"), mk(h2, "video", ""), mk(h3, "photo", "/p/"+h3+".webp")} {
		if err := fs.InsertFrame(f); err != nil {
			t.Fatalf("InsertFrame: %v", err)
		}
	}
	if err := db.Exec(`UPDATE frames SET pipe_ver = 1 WHERE hash = $1`, h3); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO frame_tags (hash, tag, source, created_at) VALUES ($1, 'sea', 'model', $2)`, h1, now); err != nil {
		t.Fatal(err)
	}

	// The ensure path on the described photo: original known by its frame hash, caption applied
	// idempotently, description + described_at land, and no tag job when title and tags exist.
	sha := make([]byte, 32)
	for i := range sha {
		sha[i] = 0x11 // the hex of the first 16 bytes IS h1
	}
	id, existed, err := ss.InsertOriginal(search.Original{Source: "image", SHA256: sha, Path: "/a/" + h1 + ".jpg", CapturedAt: time.Unix(now-86400, 0), Daemon: "test"})
	if err != nil || existed {
		t.Fatalf("InsertOriginal: id=%d existed=%v err=%v", id, existed, err)
	}
	if got, err := ss.OriginalIDByFrameHash(h1); err != nil || got != id {
		t.Fatalf("OriginalIDByFrameHash = %d, %v; want %d", got, err, id)
	}
	if err := db.Exec(`UPDATE frames SET display_name = 'a title' WHERE hash = $1`, h1); err != nil {
		t.Fatal(err)
	}
	if err := ing.ApplyCaption(id, "/p/"+h1+".webp", "SCENE: a harbour at dusk\nOBJECTS: boats", time.Unix(now-86400, 0)); err != nil {
		t.Fatalf("ApplyCaption: %v", err)
	}
	if err := ing.ApplyCaption(id, "/p/"+h1+".webp", "SCENE: something else", time.Unix(now-86400, 0)); err != nil {
		t.Fatalf("ApplyCaption again: %v", err)
	}
	rows, err := db.Query(`SELECT description, described_at FROM frames WHERE hash = $1`, h1)
	if err != nil || len(rows.Vals) != 1 || rows.Vals[0][0] == nil || *rows.Vals[0][0] != "a harbour at dusk" {
		t.Fatalf("description after ApplyCaption: %v %v", rows, err)
	}
	if at, _ := strconv.ParseInt(*rows.Vals[0][1], 10, 64); at < now {
		t.Fatalf("described_at not stamped: %d", at)
	}
	if n, _ := ss.ChunkCount(0, "image", id); n == 0 {
		t.Fatal("caption chunks not written")
	}
	if queued, _, _ := ss.JobState("tag", id); queued {
		t.Fatal("a tag job was queued for a frame that already has a title and tags")
	}
	if need, err := ss.FrameNeedsTagPass(h1); err != nil || need {
		t.Fatalf("FrameNeedsTagPass(h1) = %v, %v; want false", need, err)
	}
	if need, err := ss.FrameNeedsTagPass(h2); err != nil || !need {
		t.Fatalf("FrameNeedsTagPass(h2) = %v, %v; want true", need, err)
	}
	if has, _ := ss.HasTagChunk(id); has {
		t.Fatal("HasTagChunk before any tag pass")
	}
	if _, err := ss.InsertChunksT0("image", id, time.Unix(now, 0), search.ChunkText("", "tags: sea, dusk")); err != nil {
		t.Fatal(err)
	}
	if has, _ := ss.HasTagChunk(id); !has {
		t.Fatal("HasTagChunk after the tags chunk")
	}

	// Caption state and requeue against a new render.
	if err := ss.EnqueueJob("caption", map[string]any{"origId": id, "path": "/old"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE search.jobs SET attempts = 5 WHERE kind = 'caption'`); err != nil {
		t.Fatal(err)
	}
	st, err := ss.CaptionStateOf(id)
	if err != nil || !st.JobQueued || !st.JobParked {
		t.Fatalf("CaptionStateOf = %+v, %v", st, err)
	}
	if err := ss.RequeueCaption(id, "/p/"+h1+".webp"); err != nil {
		t.Fatal(err)
	}
	if st, _ = ss.CaptionStateOf(id); !st.JobQueued || st.JobParked {
		t.Fatalf("after RequeueCaption: %+v", st)
	}
	if err := ss.SetCaption(id, "SCENE: kept"); err != nil {
		t.Fatal(err)
	}
	if st, _ = ss.CaptionStateOf(id); st.Caption != "SCENE: kept" {
		t.Fatalf("SetCaption: %+v", st)
	}

	// framed's audit sees the stages as they are.
	audit, err := fs.Audit()
	if err != nil || len(audit) != 3 {
		t.Fatalf("Audit: %d rows, %v", len(audit), err)
	}
	byHash := map[string]framed.Audit{}
	for _, a := range audit {
		byHash[a.Hash] = a
	}
	if a := byHash[h1]; !a.Described || !a.Titled || !a.Tagged || a.PipeVer != framed.PipelineVersion {
		t.Fatalf("h1 audit: %+v", a)
	}
	if a := byHash[h2]; a.Described || a.Titled || a.Tagged || a.PreviewPath != "" || a.Kind != "video" {
		t.Fatalf("h2 audit: %+v", a)
	}
	if a := byHash[h3]; a.PipeVer != 1 {
		t.Fatalf("h3 audit: %+v", a)
	}

	// Re-insert converges: the video gains a preview, the old photo is stamped with the version,
	// and nothing regresses.
	if err := fs.InsertFrame(mk(h2, "video", "/p/"+h2+".jpg")); err != nil {
		t.Fatal(err)
	}
	if err := fs.InsertFrame(mk(h3, "photo", "")); err != nil {
		t.Fatal(err)
	}
	audit, _ = fs.Audit()
	for _, a := range audit {
		byHash[a.Hash] = a
	}
	if a := byHash[h2]; a.PreviewPath != "/p/"+h2+".jpg" {
		t.Fatalf("video preview not filled: %+v", a)
	}
	if a := byHash[h3]; a.PipeVer != framed.PipelineVersion || a.PreviewPath != "/p/"+h3+".webp" {
		t.Fatalf("old photo not converged (or preview lost): %+v", a)
	}

	// framed publishes its state; hw's progress feed reads everything back.
	stateJSON, _ := json.Marshal(framed.ConvergeState{Running: true, StartedAt: now, ToDo: 2, Done: 1})
	if err := fs.SetState("converge", stateJSON); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if err := fs.SetState("converge", stateJSON); err != nil {
		t.Fatalf("SetState again: %v", err)
	}
	p, err := hw.PipelineProgressFrom(db)
	if err != nil {
		t.Fatalf("PipelineProgressFrom: %v", err)
	}
	if p.PipelineVersion != framed.PipelineVersion || p.Photos != 2 || p.Videos != 1 || p.Total != 3 {
		t.Fatalf("counts: %+v", p)
	}
	if p.Derived.Done != 3 || p.Previewed.Done != 3 || p.Described.Done != 1 || p.Titled.Done != 1 || p.Tagged.Done != 1 || p.AtLatest.Done != 1 {
		t.Fatalf("stages: derived %+v previewed %+v described %+v titled %+v tagged %+v atLatest %+v",
			p.Derived, p.Previewed, p.Described, p.Titled, p.Tagged, p.AtLatest)
	}
	if p.DescribedLastHour != 1 || p.DescribedLastDay != 1 || p.LastDescribedAt < now {
		t.Fatalf("rate: %+v", p)
	}
	if p.EtaSeconds != 2*3600 {
		t.Fatalf("eta = %d, want 2h (2 left at 1/h)", p.EtaSeconds)
	}
	if p.Caption.Pending != 1 || p.Caption.Parked != 0 {
		t.Fatalf("queue: %+v", p.Caption)
	}
	var back framed.ConvergeState
	if err := json.Unmarshal(p.Converge, &back); err != nil || !back.Running || back.ToDo != 2 || back.Done != 1 {
		t.Fatalf("converge state round trip: %s %v", p.Converge, err)
	}
	if p.ConvergeUpdatedAt < now {
		t.Fatalf("converge updated_at: %d", p.ConvergeUpdatedAt)
	}

	// Location points from the phone land like a watch's, idempotently.
	pts := []framed.TrackPoint{{TS: now - 60, Lat: 51.5, Lon: -0.12}, {TS: now - 30, Lat: 51.51, Lon: -0.12}}
	if err := fs.InsertPoints("phone", pts); err != nil {
		t.Fatalf("InsertPoints: %v", err)
	}
	if err := fs.InsertPoints("phone", pts); err != nil {
		t.Fatalf("InsertPoints again: %v", err)
	}
	if got, _ := fs.DayPoints(now-3600, now+1); len(got) != 2 {
		t.Fatalf("DayPoints = %d, want 2", len(got))
	}
}
