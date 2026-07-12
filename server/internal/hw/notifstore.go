package hw

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/apparedis"
)

// NotifStore is the per-account notification data model, living INSIDE the encrypted volume (Redis
// last-100 hot cache + Postgres durable history). Notifications are always PRODUCED by the daemons
// (synthd, shadowd, cued, ...) regardless of mute , mute only affects what the poller PUSHES, not what
// is stored. Every notification persists in Postgres with a seen flag and can be deleted forever; the
// in-app list reads the full history, the poller reads only the push window.
//
// Push model (Option A): a per-DEVICE cursor (highest id pushed to that device) is a high-water mark
// in Redis. On a poll, ghost.secd reads the last-100 newer than the cursor, advances the cursor past
// ALL of them (muted included), then drops the muted ones from the returned payload. So a muted
// notification is skipped from push and never pushed later (the cursor moved past it), but it remains
// in the store with its seen/delete state for the in-app list. This is why the cursor must advance
// over the full not-yet-pushed set BEFORE muted ones are removed.
//
// Shell-out to redis-cli / psql (no DB driver dependency), matching DataStore/MuteStore.
//
// Redis:  notifications:recent       LIST of JSON notifications (newest first), trimmed to 100
//         notifications:cursor:<dev>  highest pushed id for a device
// Postgres: notifications(id, service, kind, title, body, seen, created)
//
// NOT validated in CI; exercise on the box.

const recentKey = "notifications:recent"
const recentCap = 100

// Ask errors, surfaced by the answer endpoint so the app can tell a stale/duplicate answer from a bad one.
var (
	ErrNoNotif         = notifErr("no such notification")
	ErrNotAsk          = notifErr("notification is not an ask")
	ErrBadChoice       = notifErr("choice is not one of the offered options")
	ErrAlreadyAnswered = notifErr("ask already answered")
)

type notifErr string

func (e notifErr) Error() string { return string(e) }

type Notification struct {
	ID      int64  `json:"id"`
	Service string `json:"service"`
	Kind    string `json:"kind"` // e.g. "message", "alert" , lets the app render generic vs specific
	Title   string `json:"title"`
	Body    string `json:"body"`
	Seen    bool   `json:"seen"`
	// Ask fields. A notification with Options is an "ask" the user can answer (ghost.cued nominations
	// and confirmations); one without is passive. Answer is the chosen option once picked, Answered
	// its unix time (0 = still pending). The app renders Options as buttons and posts the choice back.
	Options  []string `json:"options,omitempty"`
	Answer   string   `json:"answer,omitempty"`
	Answered int64    `json:"answered,omitempty"`
	Created  int64    `json:"created"` // unix seconds
}

// IsAsk reports whether this notification expects an answer (it carries options).
func (n Notification) IsAsk() bool { return len(n.Options) > 0 }

type NotifStore struct {
	pgSocketFor func(slot int) string
	mu          sync.Mutex
	rw          map[int]*poltergres.ReadWrite // per-slot ghost_rw pg connection
	rd          map[int]*apparedis.ReadWrite // per-slot ghost_rw redis connection
}

// FramesLatest reports the newest taken_at per media kind in the slot's frames table , the box-side
// "where was I" for the phone's sync. The app seeds its local cursor from this at sync start, so a
// killed/reinstalled app resumes from what the box ACTUALLY HAS instead of re-offering the whole
// camera roll. Zero values mean the table is empty (or predates the kind column) , the app then falls
// back to its local cursor.
func (s *NotifStore) FramesLatest(slot int) (photoTs, videoTs int64, err error) {
	c, err := s.pg(slot)
	if err != nil {
		return 0, 0, err
	}
	// Only frames whose taken time came from the CONTENT (exif) or the phone's explicit hint count
	// toward "where was I" , an mtime-derived taken_at is upload time, and a single such row would
	// report "latest = today" and make the phone fast-forward past its entire un-synced roll.
	rows, err := c.Query("SELECT kind, COALESCE(MAX(taken_at),0) FROM frames WHERE taken_src IN ('exif','hint') GROUP BY kind")
	if err != nil {
		return 0, 0, err
	}
	for _, v := range rows.Vals {
		if len(v) < 2 || v[0] == nil || v[1] == nil {
			continue
		}
		ts, perr := strconv.ParseInt(*v[1], 10, 64)
		if perr != nil {
			continue
		}
		switch *v[0] {
		case "photo":
			photoTs = ts
		case "video":
			videoTs = ts
		}
	}
	return photoTs, videoTs, nil
}

// FrameRow is one gallery entry , enough for the app to render a grid cell and ask for the thumb.
type FrameRow struct {
	Hash    string `json:"hash"`
	TakenAt int64  `json:"takenAt"`
	Kind    string `json:"kind"`
	Bytes   int64  `json:"bytes"`
}

// FramesList pages the archive newest-first: frames with taken_at strictly BEFORE the cursor (pass 0
// or a huge value for the first page), up to limit. The app paginates by passing the last row's
// takenAt as the next call's before.
func (s *NotifStore) FramesList(slot int, beforeTs int64, limit int) ([]FrameRow, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	if beforeTs <= 0 {
		beforeTs = 1 << 62
	}
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	rows, err := c.Query(
		"SELECT hash, taken_at, kind, bytes FROM frames WHERE taken_at < $1 ORDER BY taken_at DESC LIMIT "+strconv.Itoa(limit),
		strconv.FormatInt(beforeTs, 10))
	if err != nil {
		return nil, err
	}
	out := make([]FrameRow, 0, len(rows.Vals))
	for _, v := range rows.Vals {
		if len(v) < 4 || v[0] == nil {
			continue
		}
		r := FrameRow{Hash: *v[0]}
		if v[1] != nil {
			r.TakenAt, _ = strconv.ParseInt(*v[1], 10, 64)
		}
		if v[2] != nil {
			r.Kind = *v[2]
		}
		if v[3] != nil {
			r.Bytes, _ = strconv.ParseInt(*v[3], 10, 64)
		}
		out = append(out, r)
	}
	return out, nil
}

// FramesHave reports which of the given content hashes are already archived , the pre-upload
// existence check. The phone hashes a file locally (cheap) and skips the upload (expensive: a 4K
// video is hundreds of MB) for anything the box confirms. Input hashes are validated hex by the
// caller; this builds an IN list, capped, and returns the subset present.
func (s *NotifStore) FramesHave(slot int, hashes []string) (map[string]bool, error) {
	have := map[string]bool{}
	if len(hashes) == 0 {
		return have, nil
	}
	if len(hashes) > 200 {
		hashes = hashes[:200]
	}
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	in := "'" + strings.Join(hashes, "','") + "'" // caller validated: lowercase hex only, no quoting risk
	rows, err := c.Query("SELECT hash FROM frames WHERE hash IN (" + in + ")")
	if err != nil {
		return nil, err
	}
	for _, v := range rows.Vals {
		if len(v) > 0 && v[0] != nil {
			have[*v[0]] = true
		}
	}
	return have, nil
}

// FrameThumbPath returns the on-volume path of a frame's thumbnail ("" if none , e.g. videos).
func (s *NotifStore) FrameThumbPath(slot int, hash string) (string, error) {
	c, err := s.pg(slot)
	if err != nil {
		return "", err
	}
	rows, err := c.Query("SELECT thumb_path FROM frames WHERE hash = $1", hash)
	if err != nil {
		return "", err
	}
	if len(rows.Vals) == 0 || len(rows.Vals[0]) == 0 || rows.Vals[0][0] == nil {
		return "", nil
	}
	return *rows.Vals[0][0], nil
}

func NewNotifStore(pgSocketFor func(slot int) string) *NotifStore {
	return &NotifStore{
		pgSocketFor: pgSocketFor,
		rw:          map[int]*poltergres.ReadWrite{},
		rd:          map[int]*apparedis.ReadWrite{},
	}
}

// pg returns (lazily building) the slot's ghost_rw poltergres client. NotifStore both reads and writes, so
// it uses the write role for everything , simpler than juggling two connections for a store whose
// reads and writes interleave constantly.
func (s *NotifStore) pg(slot int) (*poltergres.ReadWrite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.rw[slot]; ok {
		return c, nil
	}
	mount := filepath.Dir(s.pgSocketFor(slot))
	cfg, err := LoadServicesConfig(mount)
	if err != nil {
		return nil, err
	}
	sockDir := s.pgSocketFor(slot)
	c := poltergres.NewReadWrite(sockDir, cfg.Postgres.Port, cfg.Postgres.RWUser, cfg.Postgres.RWPass, cfg.Postgres.Name)
	s.rw[slot] = c
	return c, nil
}

// rds returns (lazily building) the slot's ghost_rw redis client.
func (s *NotifStore) rds(slot int) (*apparedis.ReadWrite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.rd[slot]; ok {
		return c, nil
	}
	mount := filepath.Dir(s.pgSocketFor(slot))
	cfg, err := LoadServicesConfig(mount)
	if err != nil {
		return nil, err
	}
	c := apparedis.NewReadWrite(cfg.Redis.Port, cfg.Redis.RWUser, cfg.Redis.RWPass)
	s.rd[slot] = c
	return c, nil
}

// Produce stores a new notification: insert into Postgres (durable, returns the assigned id), then
// LPUSH the JSON onto the Redis last-100 list and trim. Called by the daemons; mute does NOT gate this
// (notifications are always produced and stored).
func (s *NotifStore) Produce(slot int, n Notification) error {
	id, err := s.insertPostgres(slot, n)
	if err != nil {
		return err
	}
	n.ID = id
	if n.Created == 0 {
		n.Created = time.Now().Unix()
	}
	blob, err := json.Marshal(n)
	if err != nil {
		return err
	}
	if err := s.redis(slot, "LPUSH", recentKey, string(blob)); err != nil {
		return err
	}
	return s.redis(slot, "LTRIM", recentKey, "0", strconv.Itoa(recentCap-1))
}

// PushBatch is what the poller calls: returns the notifications to push to `device` (those newer than
// the device cursor, with muted scopes removed), and advances the cursor past the FULL not-yet-pushed
// set (muted included) so muted ones are never pushed later. muted(service) decides per-service; a nil
// muted func means nothing is muted.
func (s *NotifStore) PushBatch(slot int, device string, muted func(service string) bool) ([]Notification, error) {
	recent, err := s.readRecent(slot)
	if err != nil {
		return nil, err
	}
	cursor, _ := s.getCursor(slot, device) // 0 if unset

	// (2) not-yet-pushed = id > cursor. Track the max id across ALL of them for the cursor advance.
	var maxID int64 = cursor
	fresh := make([]Notification, 0, len(recent))
	for _, n := range recent {
		if n.ID <= cursor {
			continue
		}
		if n.ID > maxID {
			maxID = n.ID
		}
		fresh = append(fresh, n)
	}

	// (5) advance the cursor past everything fresh, BEFORE removing muted , so muted ones are passed
	// over (Option A: skipped from push, not delivered later).
	if maxID > cursor {
		if err := s.setCursor(slot, device, maxID); err != nil {
			return nil, err
		}
	}

	// (3) drop muted from the returned payload.
	out := make([]Notification, 0, len(fresh))
	for _, n := range fresh {
		if muted != nil && muted(n.Service) {
			continue
		}
		out = append(out, n)
	}
	return out, nil
}

// List returns the in-app notification history (newest first, up to limit), from Postgres , the full
// list regardless of mute or push cursor, with seen/delete and ask (options/answer) state.
func (s *NotifStore) List(slot int, limit int) ([]Notification, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query("SELECT id, service, kind, title, body, seen, "+
		"coalesce(options,''), coalesce(answer,''), coalesce(extract(epoch from answered)::bigint,0), "+
		"extract(epoch from created)::bigint "+
		"FROM notifications ORDER BY id DESC LIMIT $1", limit)
	if err != nil {
		return nil, err
	}
	res := make([]Notification, 0, len(rows.Vals))
	for _, r := range rows.Vals {
		if len(r) < 10 {
			continue
		}
		cell := func(i int) string {
			if r[i] == nil {
				return ""
			}
			return *r[i]
		}
		id, _ := strconv.ParseInt(cell(0), 10, 64)
		answered, _ := strconv.ParseInt(cell(8), 10, 64)
		created, _ := strconv.ParseInt(cell(9), 10, 64)
		n := Notification{
			ID: id, Service: cell(1), Kind: cell(2), Title: cell(3), Body: cell(4),
			Seen: cell(5) == "t", Answer: cell(7), Answered: answered, Created: created,
		}
		if o := cell(6); o != "" {
			_ = json.Unmarshal([]byte(o), &n.Options) // options is a JSON array; ignore if malformed
		}
		res = append(res, n)
	}
	return res, nil
}

// Answer records the user's choice for an ask. It fetches the stored options for that id, rejects a
// choice that is not one of them (and rejects answering a non-ask or an already-answered one), then
// writes answer + answered. Postgres is the source of truth for answered state, same as seen; the
// Redis push cache is not rewritten (the ask was already pushed, and the app reads answered state
// from the list). Returns ErrNotAsk / ErrBadChoice / ErrAlreadyAnswered for the endpoint to surface.
func (s *NotifStore) Answer(slot int, id int64, choice string) error {
	c, err := s.pg(slot)
	if err != nil {
		return err
	}
	rows, err := c.Query(
		"SELECT coalesce(options,''), coalesce(answer,'') FROM notifications WHERE id = $1", id)
	if err != nil {
		return err
	}
	if len(rows.Vals) == 0 {
		return ErrNoNotif
	}
	optionsJSON, prevAnswer := "", ""
	if r := rows.Vals[0]; len(r) >= 2 {
		if r[0] != nil {
			optionsJSON = *r[0]
		}
		if r[1] != nil {
			prevAnswer = *r[1]
		}
	}
	if optionsJSON == "" {
		return ErrNotAsk
	}
	var options []string
	if json.Unmarshal([]byte(optionsJSON), &options) != nil || len(options) == 0 {
		return ErrNotAsk
	}
	if prevAnswer != "" {
		return ErrAlreadyAnswered
	}
	valid := false
	for _, o := range options {
		if o == choice {
			valid = true
			break
		}
	}
	if !valid {
		return ErrBadChoice
	}
	return c.Exec("UPDATE notifications SET answer = $1, answered = now() WHERE id = $2", choice, id)
}

// MarkSeen sets the seen flag on a notification (the app calls this when it is viewed).
func (s *NotifStore) MarkSeen(slot int, id int64) error {
	c, err := s.pg(slot)
	if err != nil {
		return err
	}
	return c.Exec("UPDATE notifications SET seen = TRUE WHERE id = $1", id)
}

// Delete removes a notification forever (Postgres). It stays in the Redis last-100 until it ages out
// of the window; the poller filters deleted ids out by virtue of the cursor having passed them, and
// the in-app list reads Postgres, so a deleted one is gone from the list immediately.
func (s *NotifStore) Delete(slot int, id int64) error {
	c, err := s.pg(slot)
	if err != nil {
		return err
	}
	return c.Exec("DELETE FROM notifications WHERE id = $1", id)
}

// --- helpers ---

func (s *NotifStore) readRecent(slot int) ([]Notification, error) {
	c, err := s.rds(slot)
	if err != nil {
		return nil, err
	}
	items, err := c.LRange(recentKey, 0, -1)
	if err != nil {
		return nil, fmt.Errorf("read recent: %w", err)
	}
	var res []Notification
	for _, line := range items {
		var n Notification
		if json.Unmarshal([]byte(line), &n) == nil {
			res = append(res, n)
		}
	}
	return res, nil
}

func (s *NotifStore) getCursor(slot int, device string) (int64, error) {
	c, err := s.rds(slot)
	if err != nil {
		return 0, err
	}
	v, ok, err := c.Get(cursorKey(device))
	if err != nil || !ok || v == "" {
		return 0, err
	}
	id, perr := strconv.ParseInt(v, 10, 64)
	if perr != nil {
		return 0, nil
	}
	return id, nil
}

func (s *NotifStore) setCursor(slot int, device string, id int64) error {
	return s.redis(slot, "SET", cursorKey(device), strconv.FormatInt(id, 10))
}

func cursorKey(device string) string { return "notifications:cursor:" + device }

func (s *NotifStore) insertPostgres(slot int, n Notification) (int64, error) {
	created := n.Created
	if created == 0 {
		created = time.Now().Unix()
	}
	// options is stored as a JSON array of choices (empty string when this is not an ask). A freshly
	// produced ask is always pending: answer='' answered NULL.
	optionsJSON := ""
	if len(n.Options) > 0 {
		b, err := json.Marshal(n.Options)
		if err != nil {
			return 0, fmt.Errorf("marshal options: %w", err)
		}
		optionsJSON = string(b)
	}
	c, err := s.pg(slot)
	if err != nil {
		return 0, err
	}
	rows, err := c.Query(
		"INSERT INTO notifications (service, kind, title, body, seen, options, created) "+
			"VALUES ($1,$2,$3,$4, FALSE, $5, to_timestamp($6)) RETURNING id",
		n.Service, n.Kind, n.Title, n.Body, optionsJSON, created)
	if err != nil {
		return 0, err
	}
	if len(rows.Vals) == 0 || len(rows.Vals[0]) == 0 || rows.Vals[0][0] == nil {
		return 0, fmt.Errorf("insert returned no id")
	}
	id, err := strconv.ParseInt(*rows.Vals[0][0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad id: %w", err)
	}
	return id, nil
}

// redis runs a fire-and-forget write via the native ghost_rw client.
func (s *NotifStore) redis(slot int, args ...string) error {
	c, err := s.rds(slot)
	if err != nil {
		return err
	}
	_, err = c.Do(args...)
	return err
}



