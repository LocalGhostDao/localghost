package main

// The day pipeline against a real Postgres: daySummaryPass gathers a day's sheet from the tables
// the other daemons write (frames, frame_tags, location_points, health_metrics, journal_entries,
// chats), writes the day_summaries row and the kind='day' memory, leaves an unchanged day alone on
// the next pass, and "on this day" reads the row back. The model is away (no oracled socket), so
// the template is what gets written , the path every box takes before its GPU is up.
//
// Skips unless GHOST_PG_SOCKET_DIR points at a running server:
//
//	GHOST_PG_SOCKET_DIR=/tmp GHOST_PG_PORT=55432 GHOST_PG_USER=claude go test -run DaysPG ./cmd/ghost.synthd/

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/geo"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

func pgFresh(t *testing.T, name string) *poltergres.ReadWrite {
	t.Helper()
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
	if err := admin.ExecSimple("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if err := admin.ExecSimple("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", name)
	if _, err := hw.ConvergeSchema(db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestDaysPGBuildOnceTellOnThisDay(t *testing.T) {
	db := pgFresh(t, "lgtest_synthd_days")
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := time.Now().UTC()
	// a day a year ago, so "on this day" tells it (this year's own day is not a memory)
	day := now.AddDate(-1, 0, -3)
	dayStr := day.Format("2006-01-02")
	t0 := time.Date(day.Year(), day.Month(), day.Day(), 9, 0, 0, 0, time.UTC).Unix()

	exec := func(q string, args ...any) {
		t.Helper()
		if err := db.Exec(q, args...); err != nil {
			t.Fatalf("%v\n  %s", err, q)
		}
	}
	for i := 0; i < 6; i++ {
		h := strings.Repeat(strconv.Itoa(i+1), 32)
		exec(`INSERT INTO frames (hash, taken_at, archive_path, kind, place, description, received_at) VALUES ($1,$2,$3,'photo',$4,$5,$6)`,
			h, t0+int64(i)*1800, "/a/"+h, geo.Place{Continent: "Europe", Country: "Greece", Admin1: "Ionian Islands", Admin2: "Kerkyra", Locality: "Lakka"}.String(), map[bool]string{true: "A harbour at dawn.", false: ""}[i < 4], t0)
		exec(`INSERT INTO frame_tags (hash, tag, source, created_at, category) VALUES ($1,'sea','model',$2,'nature')`, h, t0)
	}
	for i := 0; i < 30; i++ {
		exec(`INSERT INTO location_points (ts, lat, lon, source) VALUES ($1, 39.23, 20.13, 'phone')`, t0+int64(i)*600)
	}
	exec(`INSERT INTO health_metrics (day, metric, value) VALUES ($1,'steps',12840)`, dayStr)
	exec(`INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ('ghost.noted','c1',$1,'Daily check-in `+dayStr+`','Feeling: calm'||chr(10)||'Energy: good',$1)`, t0+3600)
	exec(`INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ('ghost.noted','n1',$1,'Swam to the lighthouse','',$1)`, t0+7200)
	exec(`INSERT INTO chats (title, created_at, updated_at) VALUES ('ferry times to Corfu',$1,$1)`, (t0+5400)*1000)
	// a voice note from the check-in, as ghost.voiced journals it: the words go to the sheet, not the title
	exec(`INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ('ghost.voiced','voice:0123456789abcdef0123456789abcdef',$1,'Voice note: The water was cold at eight','Said at the daily check-in of `+dayStr+` (1 min 12 s):'||chr(10)||'The water was cold at eight but I swam anyway.',$1)`, t0+36000)
	// the backfill starts at this day (the watermark), so one pass reaches it
	exec(`INSERT INTO settings (key, value) VALUES ('synthd_days_watermark',$1)`, dayStr)

	oc := oracle.NewClient(t.TempDir(), time.Second) // no oracled: the template path
	mount := t.TempDir()
	lastDayPass = time.Time{}
	built, written, err := daySummaryPass(db, oc, mount, lg)
	if err != nil {
		t.Fatalf("daySummaryPass: %v", err)
	}
	if built < 1 || written != 0 {
		t.Fatalf("built %d, written by the model %d", built, written)
	}
	rows, err := db.Query(`SELECT title, summary, written_by, facts::text FROM day_summaries WHERE day = $1`, dayStr)
	if err != nil || len(rows.Vals) != 1 {
		t.Fatalf("no row for %s: %v", dayStr, err)
	}
	v := rows.Vals[0]
	var f dayFacts
	if err := json.Unmarshal([]byte(*v[3]), &f); err != nil {
		t.Fatal(err)
	}
	if f.Photos != 6 || f.Described != 4 || f.Points != 30 || f.Steps != 12840 || f.Feeling != "calm" ||
		len(f.Notes) != 1 || len(f.Chats) != 1 || len(f.Tags) != 1 || f.Country != "Greece" || len(f.Covers) != 6 ||
		len(f.Places) != 1 || f.Places[0] != "Lakka" || len(f.Spoken) != 1 || f.Spoken[0] != "The water was cold at eight but I swam anyway." {
		t.Fatalf("the sheet: %+v", f)
	}
	if *v[2] != "template" || !strings.Contains(*v[1], "You said: \u201cThe water was cold") || !strings.Contains(*v[0], day.Weekday().String()) {
		t.Fatalf("row: title %q summary %q by %q", *v[0], *v[1], *v[2])
	}
	// the memories feed got the day (it has signal: six photos, a feeling, a note)
	if rows, err := db.Query(`SELECT count(*) FROM memories WHERE kind = 'day' AND source_ref = $1`, "day:"+dayStr); err != nil || *rows.Vals[0][0] != "1" {
		ex, _ := db.Query(`SELECT source_ref FROM memories WHERE kind = 'day'`)
		t.Fatalf("day memory: %v %v", err, ex)
	}
	// nothing changed: the next pass leaves the row alone
	lastDayPass = time.Time{}
	exec(`INSERT INTO settings (key, value) VALUES ('synthd_days_watermark',$1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, dayStr)
	if built, _, err := daySummaryPass(db, oc, mount, lg); err != nil || built != 0 {
		t.Fatalf("second pass rebuilt %d rows (%v)", built, err)
	}
	// a late sync changes the sheet: built again
	lastDayPass = time.Time{}
	exec(`UPDATE health_metrics SET value = 13000 WHERE day = $1 AND metric = 'steps'`, dayStr)
	exec(`INSERT INTO settings (key, value) VALUES ('synthd_days_watermark',$1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, dayStr)
	if built, _, err := daySummaryPass(db, oc, mount, lg); err != nil || built != 1 {
		t.Fatalf("after the late sync: built %d (%v)", built, err)
	}

	// on this day reads the row: the narrative, the title, the covers and places
	memDB = db
	defer func() { memDB = nil }()
	out, err := onThisDay(t.TempDir()+"/run", day.Format("01-02"), lg)
	if err != nil {
		t.Fatalf("onThisDay: %v", err)
	}
	if !strings.Contains(out, `"narrative"`) || !strings.Contains(out, *v[0]) || !strings.Contains(out, "Lakka") {
		t.Fatalf("on this day did not tell the prebuilt day:\n%s", out)
	}
}

// TODAY is told the moment the check-in is in: the day's summary carries the feeling and the
// person's own why, the notes leave out the box's own lines (a photo's, health's, a chat's), and
// the memories feed gets the day, all before the evening hour the pass otherwise waits for.
func TestTodayTellsAfterTheCheckin(t *testing.T) {
	db := pgFresh(t, "lgtest_synthd_today")
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := time.Now().In(hw.LocalZone(db)) // the pass's own clock (no local_tz row here: the box's)
	today := now.Format("2006-01-02")
	t0 := time.Date(now.Year(), now.Month(), now.Day(), 0, 30, 0, 0, time.UTC).Unix()
	exec := func(q string, args ...any) {
		t.Helper()
		if err := db.Exec(q, args...); err != nil {
			t.Fatalf("%v\n  %s", err, q)
		}
	}
	for i := 0; i < 6; i++ {
		h := strings.Repeat(strconv.Itoa(i+1), 32)
		exec(`INSERT INTO frames (hash, taken_at, archive_path, kind, place, description, received_at) VALUES ($1,$2,$3,'photo',$4,'A beach.',$5)`,
			h, t0+int64(i)*600, "/a/"+h, geo.Place{Country: "Greece", Locality: "Lakka"}.String(), t0)
		exec(`INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ('ghost.framed',$1,$2,'photo at Lakka','',$2)`, "p"+h, t0+int64(i)*600)
	}
	exec(`INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ('ghost.tallyd','h',$1,'health , `+today+`','8000 steps.',$1)`, t0+100)
	exec(`INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ('ghost.noted','c',$1,'conversation: ferries','',$1)`, t0+200)
	exec(`INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ('ghost.noted','n',$1,'A note of my own','',$1)`, t0+300)

	oc := oracle.NewClient(t.TempDir(), time.Second) // no oracled: the template path
	mount := t.TempDir()
	// no check-in yet: today is not a candidate (unless it is already evening in UTC)
	if now.Hour() < dayEvening {
		lastDayPass = time.Time{}
		_, _, _ = daySummaryPass(db, oc, mount, lg)
		if rows, _ := db.Query(`SELECT count(*) FROM day_summaries WHERE day = $1`, today); *rows.Vals[0][0] != "0" {
			t.Fatal("today built before the check-in and before the evening")
		}
	}
	exec(`INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ('ghost.noted','ci',$1,'Daily check-in `+today+`','Feeling: tired, happy'||chr(10)||'Preselected: tired'||chr(10)||'Why: long swim, then too much food',$1)`, t0+400)
	built, _, err := daySummaryPass(db, oc, mount, lg, today)
	if err != nil || built != 1 {
		t.Fatalf("built %d err %v", built, err)
	}
	rows, err := db.Query(`SELECT summary, facts::text FROM day_summaries WHERE day = $1`, today)
	if err != nil || len(rows.Vals) != 1 {
		t.Fatalf("row %v %v", rows, err)
	}
	var f dayFacts
	if err := json.Unmarshal([]byte(*rows.Vals[0][1]), &f); err != nil {
		t.Fatal(err)
	}
	if !f.CheckedIn || f.Feeling != "tired, happy" || f.Why != "long swim, then too much food" || len(f.Notes) != 1 || f.Notes[0] != "A note of my own" {
		t.Fatalf("facts %+v", f)
	}
	sum := *rows.Vals[0][0]
	if !strings.Contains(sum, "You said you felt tired, happy: “long swim, then too much food”") || strings.Contains(sum, "photo at Lakka") || strings.Contains(sum, "health ,") {
		t.Fatalf("summary %q", sum)
	}
	if rows, _ := db.Query(`SELECT count(*) FROM memories WHERE kind = 'day' AND source_ref = $1`, "day:"+today); *rows.Vals[0][0] != "1" {
		t.Fatal("today is not in the memories feed after the check-in")
	}
	if !checkedIn(db, today) || checkedIn(db, "2001-01-01") {
		t.Fatal("checkedIn")
	}
}
