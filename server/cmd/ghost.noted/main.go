// ghost.noted , TEXT INGESTION (first real slice). Binds its loopback health port and reports OK so ghost.watchd can
// manage it (poll, restart, stop-before-unmount) before the real logic exists. The daemon's actual
// job is described in this directory's README; this binary is the honest placeholder , it does
// nothing but stay alive and answer health, so the supervisor and the app's Ghost Status screen work
// end to end today. Replace the body with real logic behind the same ghosthealth.Reporter contract.
//
// Runs only while the account is UNLOCKED (data lives on the encrypted volume). Exits cleanly on
// SIGTERM so the supervisor's stop-and-confirm-dead teardown never leaves it holding the mount.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"io"
	"log"
	"log/slog"
	"net/mail"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/ghosthealth"
	"github.com/LocalGhostDao/localghost/server/internal/harden"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/rotlog"
	"github.com/LocalGhostDao/localghost/server/internal/svcconf"
)

const service = "ghost.noted"

func main() {
	harden.NoDump() // same-user processes cannot read this one through /proc; no core file
	port := flag.Int("health-port", envPort("GHOST_HEALTH_PORT"), "loopback health/status port (required)")
	flag.Parse()
	if *port <= 0 {
		log.Fatalf("%s: no health port (set --health-port or GHOST_HEALTH_PORT)", service)
	}

	// Logs go through a self-rotating writer: <GHOST_LOG_DIR>/<service>-YYYY-MM-DD.log, a new file at
	// midnight with no restart (watchd sets GHOST_LOG_DIR when it spawns us). If GHOST_LOG_DIR is
	// unset (run by hand), fall back to stderr so nothing is lost.
	var lg *slog.Logger
	var lvl *slog.LevelVar
	if dir := os.Getenv("GHOST_LOG_DIR"); dir != "" {
		w, err := rotlog.New(dir, service)
		if err != nil {
			log.Fatalf("%s: open log: %v", service, err)
		}
		defer w.Close()
		lg, lvl = rotlog.Logger(w)
	} else {
		lvl = new(slog.LevelVar)
		lvl.Set(rotlog.LevelFromEnv())
		lg = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	runDir := os.Getenv("GHOST_RUN_DIR")
	if runDir == "" {
		if ld := os.Getenv("GHOST_LOG_DIR"); ld != "" {
			runDir = filepath.Join(filepath.Dir(ld), "run")
		}
	}
	// THE HEALTH LINE: the journal entries kept, and this week's; "stub ok" was the first
	// week's line, from before the notes, the check-ins and the voice notes were real.
	srv := ghosthealth.NewServer(service, ghosthealth.Cached(service, time.Minute, func() string {
		if runDir == "" {
			return "the notes, the check-ins and the voice notes: no volume yet"
		}
		mount := filepath.Dir(runDir)
		cfg, err := hw.LoadServicesConfig(mount)
		if err != nil {
			return "the journal: the volume's database is not up"
		}
		db := poltergres.NewReadWrite(hw.SocketForMount(mount), cfg.Postgres.Port, cfg.Postgres.RWUser, cfg.Postgres.RWPass, cfg.Postgres.Name)
		one := func(q string, args ...any) string {
			rows, err := db.Query(q, args...)
			if err != nil || len(rows.Vals) != 1 || rows.Vals[0][0] == nil {
				return "?"
			}
			return *rows.Vals[0][0]
		}
		return "journal entries " + one("SELECT count(*) FROM journal_entries") + " · this week " +
			one("SELECT count(*) FROM journal_entries WHERE ts > $1", time.Now().Add(-7*24*time.Hour).Unix())
	}))
	go func() {
		if err := srv.Serve(*port); err != nil {
			lg.Error("health server stopped", "fn", "main", "err", err)
		}
	}()
	if runDir != "" {
		ctl := ctlsock.NewServer(service, runDir, lg)
		svcconf.BindBase(ctl, service, lvl, func() (svcconf.Base, map[string]string, error) {
			mount := filepath.Dir(runDir)
			base := svcconf.DefaultBase()
			_ = svcconf.Load(svcconf.Path(mount, service), &base)
			svcconf.FillBaseDefaults(&base)
			return base, nil, nil
		})
		defer ctl.Cleanup()
		go func() {
			if err := ctl.Serve(ctx); err != nil {
				lg.Error("control server exited", "fn", "main", "err", err)
			}
		}()
	}

	// THE INBOX. Drop text at <mount>/noted/inbox and it becomes a journal entry and an archived
	// canonical copy , that is the whole contract. Formats: .eml (parsed with stdlib net/mail:
	// Subject/Date/From honored, headers stripped, text body kept) and anything else readable as
	// text (.txt, .md , notes, exports, whatever). Idempotent end to end: ref is the content hash,
	// so the same email dropped twice is one entry; the archive is content-addressed the same way.
	// Files land in inbox from wherever the operator likes , scp, a mail-fetch cron, the app's
	// share sheet once the secd upload endpoint exists (TODO). ghost.synthd distills from the
	// journal; noted never talks to the model.
	if runDir != "" {
		mount := filepath.Dir(runDir)
		go inboxLoop(ctx, mount, lg)
		lg.Info("text ingestion up", "fn", "main", "inbox", filepath.Join(mount, "noted", "inbox"))
	} else {
		lg.Warn("no run dir , inbox disabled (health/ctl only)", "fn", "main")
	}

	<-ctx.Done()
	lg.Info("shutting down", "fn", "main")
}

func envPort(key string) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

// inboxLoop polls the inbox every 30s and ingests whatever text has landed. Lazy pg (services.conf
// creds, reconnect on failure), same pattern as every volume daemon. Each file: parse -> journal
// entry (idempotent by content hash) -> archive under the hash -> remove from inbox. Unreadable or
// unparseable files are moved to inbox/rejected with the reason logged , never deleted, never
// looping forever.
func inboxLoop(ctx context.Context, mount string, lg *slog.Logger) {
	inbox := filepath.Join(mount, "noted", "inbox")
	archive := filepath.Join(mount, "noted", "archive")
	rejected := filepath.Join(inbox, "rejected")
	for _, d := range []string{inbox, archive, rejected} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			lg.Error("inbox dirs", "fn", "inboxLoop", "err", err)
			return
		}
	}
	var db *poltergres.ReadWrite
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if db == nil {
			if cfg, cerr := hw.LoadServicesConfig(mount); cerr == nil {
				db = poltergres.NewReadWrite(hw.SocketForMount(mount), cfg.Postgres.Port,
					cfg.Postgres.RWUser, cfg.Postgres.RWPass, cfg.Postgres.Name)
			}
		}
		if db != nil {
			if err := journalChats(db, lg); err != nil {
				lg.Warn("chat journaling failed, will reconnect", "fn", "inboxLoop", "err", err)
				db = nil
			}
		}
		entries, err := os.ReadDir(inbox)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			path := filepath.Join(inbox, e.Name())
			if db == nil {
				cfg, cerr := hw.LoadServicesConfig(mount)
				if cerr != nil {
					lg.Warn("services.conf unreadable, pass skipped", "fn", "inboxLoop", "err", cerr)
					break
				}
				db = poltergres.NewReadWrite(hw.SocketForMount(mount), cfg.Postgres.Port,
					cfg.Postgres.RWUser, cfg.Postgres.RWPass, cfg.Postgres.Name)
			}
			if err := ingestOne(db, path, archive, lg); err != nil {
				lg.Warn("ingest failed, moved to rejected", "fn", "inboxLoop", "file", e.Name(), "err", err)
				_ = os.Rename(path, filepath.Join(rejected, e.Name()))
				if strings.Contains(err.Error(), "connect") || strings.Contains(err.Error(), "refused") {
					db = nil // pg trouble, not file trouble: reconnect next tick
				}
			}
		}
	}
}

// ingestOne turns one inbox file into a journal entry and an archived canonical copy.
func ingestOne(db *poltergres.ReadWrite, path, archive string, lg *slog.Logger) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > 2<<20 {
		return io.ErrUnexpectedEOF // empty or absurd , rejected, not looped
	}
	sum := sha256.Sum256(raw)
	ref := hex.EncodeToString(sum[:])
	title, body, ts := parseText(raw, path)
	fi, _ := os.Stat(path)
	if ts == 0 && fi != nil {
		ts = fi.ModTime().Unix()
	}
	ts = checkinDayTs(title, ts)
	// Ref policy, split by kind: emails keep the PURE content hash (re-dropping the same .eml
	// must stay a no-op), but plain text folds in the file mtime , jotting "gym" twice on purpose
	// is two diary entries, not one silently swallowed. The canonical archive stays
	// content-addressed either way, so bytes are never stored twice.
	if !looksLikeEmail(raw) && fi != nil {
		ref = ref[:40] + "-" + strconv.FormatInt(fi.ModTime().Unix(), 10)
	}
	if err := db.Exec(
		"INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ('ghost.noted', $1, $2, $3, $4, $5) ON CONFLICT (source, ref) DO NOTHING",
		ref, ts, title, body, time.Now().UnixMilli()); err != nil {
		return err
	}
	dst := filepath.Join(archive, ref+".txt")
	if err := os.WriteFile(dst, raw, 0o640); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	lg.Info("ingested", "fn", "ingestOne", "ref", ref[:12], "title", title)
	return nil
}

// checkinPrefix opens a daily check-in's title: "Daily check-in 2026-10-09".
const checkinPrefix = "Daily check-in "

// checkinDayTs files a check-in under the day it names. A check-in written at one in the morning
// for the day before (the app lets you pick the day) would otherwise land in the journal at the
// time it was written, and the day's story, which reads the journal by the clock, would file the
// feeling under the wrong day. When the title names a day other than the one the clock says, the
// entry is stamped at that day's noon (UTC, the day story's bounds); a check-in for the day it
// is written on keeps its real time, so the day's order of events holds. Any other title is left
// alone.
func checkinDayTs(title string, ts int64) int64 {
	if !strings.HasPrefix(title, checkinPrefix) {
		return ts
	}
	day, err := time.Parse("2006-01-02", strings.TrimSpace(strings.TrimPrefix(title, checkinPrefix)))
	if err != nil {
		return ts
	}
	if time.Unix(ts, 0).UTC().Format("2006-01-02") == day.Format("2006-01-02") {
		return ts
	}
	return day.Unix() + 12*3600
}

// looksLikeEmail mirrors parseText's detection , a parseable message with a Subject header.
func looksLikeEmail(raw []byte) bool {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	return err == nil && msg.Header.Get("Subject") != ""
}

// parseText understands .eml (stdlib net/mail: Subject/Date honored, headers stripped) and treats
// everything else as plain text (first non-empty line is the title). Body is capped at 4000 runes ,
// journal entries are the diary, not the archive; the canonical copy keeps every byte.
func parseText(raw []byte, path string) (title, body string, ts int64) {
	if msg, err := mail.ReadMessage(bytes.NewReader(raw)); err == nil && msg.Header.Get("Subject") != "" {
		title = msg.Header.Get("Subject")
		if d, derr := msg.Header.Date(); derr == nil {
			ts = d.Unix()
		}
		if from := msg.Header.Get("From"); from != "" {
			title = title + " (from " + from + ")"
		}
		b, _ := io.ReadAll(io.LimitReader(msg.Body, 1<<20))
		body = strings.TrimSpace(string(b))
	} else {
		text := strings.TrimSpace(string(raw))
		lines := strings.SplitN(text, "\n", 2)
		title = strings.TrimSpace(lines[0])
		if len(title) > 120 {
			title = title[:120]
		}
		if title == "" {
			title = filepath.Base(path)
		}
		body = text
	}
	r := []rune(body)
	if len(r) > 4000 {
		body = string(r[:4000]) + " …"
	}
	return title, body, ts
}

// journalChats brings CONVERSATIONS into the journal , they are text, so they are noted's domain,
// and routing them here gives ghost.synthd exactly ONE source to distill from. A chat qualifies
// once quiet for 10 minutes with at least 2 messages; the entry is the transcript (capped , the
// journal is the diary, the chats table keeps every byte), ref chat:<id>, idempotent like
// everything else in this table. Incognito never reaches the chats table, so it never reaches
// here , inherited, not enforced.
func journalChats(db *poltergres.ReadWrite, lg *slog.Logger) error {
	cutoff := time.Now().Add(-10 * time.Minute).UnixMilli()
	rows, err := db.Query(`
		SELECT c.id, c.title, c.updated_at FROM chats c
		WHERE c.updated_at < $1
		  AND NOT EXISTS (SELECT 1 FROM journal_entries j WHERE j.source = 'ghost.noted' AND j.ref = 'chat:' || c.id)
		  AND (SELECT count(*) FROM chat_messages cm WHERE cm.chat_id = c.id) >= 2
		ORDER BY c.updated_at DESC LIMIT 10`, cutoff)
	if err != nil {
		return err
	}
	for _, v := range rows.Vals {
		if len(v) < 3 || v[0] == nil {
			continue
		}
		chatID := *v[0]
		title := "conversation"
		if v[1] != nil && strings.TrimSpace(*v[1]) != "" {
			title = "conversation: " + strings.TrimSpace(*v[1])
		}
		var ts int64
		if v[2] != nil {
			if ms, perr := strconv.ParseInt(*v[2], 10, 64); perr == nil {
				ts = ms / 1000
			}
		}
		mrows, merr := db.Query("SELECT role, content FROM chat_messages WHERE chat_id = $1 ORDER BY id ASC LIMIT 60", chatID)
		if merr != nil {
			return merr
		}
		var b strings.Builder
		for _, m := range mrows.Vals {
			if len(m) >= 2 && m[0] != nil && m[1] != nil {
				b.WriteString(*m[0] + ": " + *m[1] + "\n")
			}
		}
		body := b.String()
		if r := []rune(body); len(r) > 4000 {
			body = string(r[:4000]) + " …"
		}
		if err := db.Exec(
			"INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ('ghost.noted', $1, $2, $3, $4, $5) ON CONFLICT (source, ref) DO NOTHING",
			"chat:"+chatID, ts, title, body, time.Now().UnixMilli()); err != nil {
			return err
		}
		lg.Info("chat journaled", "fn", "journalChats", "chat", chatID)
	}
	return nil
}
