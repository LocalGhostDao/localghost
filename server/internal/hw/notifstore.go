package hw

import (
	"encoding/json"
	"fmt"
	"os"
	"log/slog"
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
	Hash    string   `json:"hash"`
	TakenAt int64    `json:"takenAt"`
	Kind    string   `json:"kind"`
	Bytes   int64    `json:"bytes"`
	Name    string   `json:"name,omitempty"` // derived (date + tags) until a user rename exists
	Tags    []string `json:"tags,omitempty"` // model + user tags; tombstoned removals excluded
	Place   string   `json:"place,omitempty"` // reverse-geocoded hierarchy, "" until geo data lands
	Description string `json:"description,omitempty"` // the caption's SCENE section
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
		"SELECT hash, taken_at, kind, bytes, display_name, place, description FROM frames WHERE taken_at < $1 ORDER BY taken_at DESC LIMIT "+strconv.Itoa(limit),
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
		if len(v) > 4 && v[4] != nil {
			r.Name = *v[4]
		}
		if len(v) > 5 && v[5] != nil {
			r.Place = *v[5]
		}
		if len(v) > 6 && v[6] != nil {
			r.Description = *v[6]
		}
		out = append(out, r)
	}
	// One query attaches every page row's tags , per-row queries would be 60x the round trips for
	// the same data. Tombstones ('user_removed') are how a correction outranks the model: excluded
	// here, and the tag worker's NOT EXISTS keeps them from being re-inserted.
	if len(out) > 0 {
		in := "'" + out[0].Hash + "'"
		idx := map[string]int{out[0].Hash: 0}
		for i := 1; i < len(out); i++ {
			in += ",'" + out[i].Hash + "'" // hashes are our own validated hex , no quoting risk
			idx[out[i].Hash] = i
		}
		trows, terr := c.Query("SELECT hash, tag FROM frame_tags WHERE source != 'user_removed' AND hash IN (" + in + ") ORDER BY created_at")
		if terr == nil {
			for _, v := range trows.Vals {
				if len(v) >= 2 && v[0] != nil && v[1] != nil {
					if i, ok := idx[*v[0]]; ok {
						out[i].Tags = append(out[i].Tags, *v[1])
					}
				}
			}
		}
	}
	return out, nil
}

// TagSet applies a user's tag correction. add: source 'user' (upsert over any tombstone , the user
// changed their mind, that wins too). remove: the row becomes a 'user_removed' TOMBSTONE rather than
// vanishing, so the model can never re-propose a tag a human explicitly rejected.
func (s *NotifStore) TagSet(slot int, hash, tag, action string) error {
	c, err := s.pg(slot)
	if err != nil {
		return err
	}
	now := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	switch action {
	case "add":
		return c.Exec(`INSERT INTO frame_tags (hash, tag, source, created_at) VALUES ($1,$2,'user',$3)
			ON CONFLICT (hash, tag) DO UPDATE SET source = 'user', created_at = EXCLUDED.created_at`,
			hash, tag, now)
	case "remove":
		return c.Exec(`INSERT INTO frame_tags (hash, tag, source, created_at) VALUES ($1,$2,'user_removed',$3)
			ON CONFLICT (hash, tag) DO UPDATE SET source = 'user_removed', created_at = EXCLUDED.created_at`,
			hash, tag, now)
	}
	return nil
}

// Stats plumbing , thin, typed access to the per-service ring buffers in Redis. Three keys per
// tracked target: stats:10s:<name> (600 entries), stats:1m:<name> (1440 = 24h), stats:24h:<name>
// (one computed-averages blob). Lists are LPUSH+LTRIM ring buffers: newest first, bounded forever.

func (s *NotifStore) StatsPush(slot int, key, entry string, max int) error {
	r, err := s.rds(slot)
	if err != nil {
		return err
	}
	if err := r.LPush(key, entry); err != nil {
		return err
	}
	return r.LTrim(key, 0, max-1)
}

func (s *NotifStore) StatsRange(slot int, key string, n int) ([]string, error) {
	r, err := s.rds(slot)
	if err != nil {
		return nil, err
	}
	return r.LRange(key, 0, n-1)
}

func (s *NotifStore) StatsSet(slot int, key, val string) error {
	r, err := s.rds(slot)
	if err != nil {
		return err
	}
	return r.Set(key, val)
}

func (s *NotifStore) StatsGet(slot int, key string) (string, bool, error) {
	r, err := s.rds(slot)
	if err != nil {
		return "", false, err
	}
	return r.Get(key)
}

// Chat reads , the API side of what ghost.synthd persists. Keyset pagination on (updated_at, id):
// stable under concurrent writes, no OFFSET scans. Search matches the title OR any message body ,
// parametrised, never spliced.

// ChatRow is one conversation in the list.
type ChatRow struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	Messages  int64  `json:"messages"`
}

// ChatsList returns up to limit chats, newest-updated first, strictly older than beforeUpdated
// (0 = from the top). q filters by title or content, case-insensitive.
func (s *NotifStore) ChatsList(slot int, limit int, beforeUpdated int64, q string) ([]ChatRow, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if beforeUpdated <= 0 {
		beforeUpdated = 1 << 62
	}
	sql := `SELECT c.id, c.title, c.created_at, c.updated_at,
	               (SELECT count(*) FROM chat_messages m WHERE m.chat_id = c.id) AS msgs
	        FROM chats c WHERE c.updated_at < $1`
	args := []any{strconv.FormatInt(beforeUpdated, 10)}
	if q != "" {
		sql += ` AND (c.title ILIKE $2 OR EXISTS (
		            SELECT 1 FROM chat_messages m WHERE m.chat_id = c.id AND m.content ILIKE $2))`
		args = append(args, "%"+q+"%")
	}
	sql += ` ORDER BY c.updated_at DESC, c.id DESC LIMIT ` + strconv.Itoa(limit)
	rows, err := c.Query(sql, args...)
	if err != nil {
		return nil, err
	}
	out := make([]ChatRow, 0, len(rows.Vals))
	for _, v := range rows.Vals {
		if len(v) < 5 || v[0] == nil {
			continue
		}
		var r ChatRow
		r.ID, _ = strconv.ParseInt(*v[0], 10, 64)
		if v[1] != nil {
			r.Title = *v[1]
		}
		if v[2] != nil {
			r.CreatedAt, _ = strconv.ParseInt(*v[2], 10, 64)
		}
		if v[3] != nil {
			r.UpdatedAt, _ = strconv.ParseInt(*v[3], 10, 64)
		}
		if v[4] != nil {
			r.Messages, _ = strconv.ParseInt(*v[4], 10, 64)
		}
		out = append(out, r)
	}
	return out, nil
}

// ChatMsg is one message in a conversation.
type ChatMsg struct {
	ID      int64  `json:"id"`
	Role    string `json:"role"`
	Content string `json:"content"`
	TS      int64  `json:"ts"`
}

// ChatMessages returns up to limit messages for a chat, NEWEST first, strictly older than beforeID
// (0 = from the newest). The app reverses for display and passes the smallest id back to page.
func (s *NotifStore) ChatMessages(slot int, chatID int64, limit int, beforeID int64) ([]ChatMsg, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if beforeID <= 0 {
		beforeID = 1 << 62
	}
	rows, err := c.Query(
		`SELECT id, role, content, ts FROM chat_messages WHERE chat_id = $1 AND id < $2
		 ORDER BY id DESC LIMIT `+strconv.Itoa(limit),
		strconv.FormatInt(chatID, 10), strconv.FormatInt(beforeID, 10))
	if err != nil {
		return nil, err
	}
	out := make([]ChatMsg, 0, len(rows.Vals))
	for _, v := range rows.Vals {
		if len(v) < 4 || v[0] == nil {
			continue
		}
		var m ChatMsg
		m.ID, _ = strconv.ParseInt(*v[0], 10, 64)
		if v[1] != nil {
			m.Role = *v[1]
		}
		if v[2] != nil {
			m.Content = *v[2]
		}
		if v[3] != nil {
			m.TS, _ = strconv.ParseInt(*v[3], 10, 64)
		}
		out = append(out, m)
	}
	return out, nil
}

// DatastoreHealth pings both datastores on the mounted slot and returns what a status surface
// should say. LIVE probes, not cached state: a wedged Postgres fails HERE, visibly, instead of
// surfacing as mystery query errors three features away. Empty string = healthy.
func (s *NotifStore) DatastoreHealth(slot int) (pgErr, redisErr string) {
	if c, err := s.pg(slot); err != nil {
		pgErr = err.Error()
	} else if err := c.Ping(); err != nil {
		pgErr = err.Error()
	}
	if r, err := s.rds(slot); err != nil {
		redisErr = err.Error()
	} else if err := r.Ping(); err != nil {
		redisErr = err.Error()
	}
	return pgErr, redisErr
}

// CursorSet stores a device's sync position , the box remembering where the phone was, so an app
// reinstall (which wipes the phone's local cursor) resumes instead of re-walking the whole roll.
func (s *NotifStore) CursorSet(slot int, device, kind string, ts, id int64) error {
	c, err := s.pg(slot)
	if err != nil {
		return err
	}
	err = c.Exec(`INSERT INTO sync_cursors (device, kind, ts, id) VALUES ($1,$2,$3,$4)
		ON CONFLICT (device, kind) DO UPDATE SET ts = GREATEST(sync_cursors.ts, EXCLUDED.ts),
		id = CASE WHEN EXCLUDED.ts >= sync_cursors.ts THEN EXCLUDED.id ELSE sync_cursors.id END`,
		device, kind, strconv.FormatInt(ts, 10), strconv.FormatInt(id, 10))
	if err != nil {
		return err
	}
	// Mirror to Redis , the read fast path AND a live exercise of apparedis on a real feature.
	// Best effort: Postgres above is the durable truth; a Redis miss just means the next read
	// falls back and says so (the "src" field in the cursor response makes this visible).
	if rd, rerr := s.rds(slot); rerr == nil {
		if serr := rd.Set("sync:cursor:"+device+":"+kind, strconv.FormatInt(ts, 10)+":"+strconv.FormatInt(id, 10)); serr != nil {
			slog.Warn("cursor redis mirror failed (postgres holds it)", "fn", "CursorSet", "err", serr)
		}
	}
	return nil
}

// CursorFull is a device's stored position for one kind , ms epoch + the last MediaStore id.
type CursorFull struct {
	TS  int64
	ID  int64
	Src string // which store answered: "redis" | "postgres"
}

// CursorGet returns the stored ts per kind for a device (ms epoch as stored by the app).
func (s *NotifStore) CursorGet(slot int, device string) (map[string]int64, error) {
	full, err := s.CursorGetFull(slot, device)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for k, v := range full {
		out[k] = v.TS
	}
	return out, nil
}

// CursorGetFull returns ts AND id per kind , the tuple the phone resumes from. Src on each entry
// names which store answered ("redis" fast path, "postgres" fallback) , the deliberate visible
// roundtrip test of both datastores on a feature that actually matters.
func (s *NotifStore) CursorGetFull(slot int, device string) (map[string]CursorFull, error) {
	out := map[string]CursorFull{}
	// Redis first. Both kinds present = done; any miss falls through to Postgres for everything.
	if rd, rerr := s.rds(slot); rerr == nil {
		hit := 0
		for _, kind := range []string{"photo", "video"} {
			v, ok, gerr := rd.Get("sync:cursor:" + device + ":" + kind)
			if gerr != nil || !ok {
				continue
			}
			parts := strings.SplitN(v, ":", 2)
			if len(parts) != 2 {
				continue
			}
			ts, e1 := strconv.ParseInt(parts[0], 10, 64)
			id, e2 := strconv.ParseInt(parts[1], 10, 64)
			if e1 == nil && e2 == nil {
				out[kind] = CursorFull{TS: ts, ID: id, Src: "redis"}
				hit++
			}
		}
		if hit == 2 {
			return out, nil
		}
		out = map[string]CursorFull{} // partial redis view: take the durable store's word instead
	}
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query("SELECT kind, ts, id FROM sync_cursors WHERE device = '" + sanitizeIdent(device) + "'")
	if err != nil {
		return nil, err
	}
	for _, v := range rows.Vals {
		if len(v) >= 3 && v[0] != nil && v[1] != nil {
			n, _ := strconv.ParseInt(*v[1], 10, 64)
			var id int64
			if v[2] != nil {
				id, _ = strconv.ParseInt(*v[2], 10, 64)
			}
			out[*v[0]] = CursorFull{TS: n, ID: id, Src: "postgres"}
		}
	}
	return out, nil
}

// sanitizeIdent strips anything outside [a-z0-9_-] , device ids are ours, but defense costs a line.
func sanitizeIdent(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
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
// FramePreviewPath , the big derived JPEG for the full-screen viewer (falls back to the thumb when
// a preview was never rendered).
func (s *NotifStore) FramePreviewPath(slot int, hash string) (string, error) {
	c, err := s.pg(slot)
	if err != nil {
		return "", err
	}
	rows, err := c.Query("SELECT preview_path, thumb_path FROM frames WHERE hash = $1", hash)
	if err != nil || len(rows.Vals) == 0 {
		return "", err
	}
	v := rows.Vals[0]
	if len(v) > 0 && v[0] != nil && *v[0] != "" {
		return *v[0], nil
	}
	if len(v) > 1 && v[1] != nil {
		return *v[1], nil
	}
	return "", nil
}

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




// ChatRename sets a conversation's title. The one WRITE in the otherwise read-only chats surface
// secd exposes , justified the same way the tag override was: a title the PERSON chose outranks the
// derived one, permanently (synthd's auto-titling only ever fills empty titles, so a rename sticks).
// Returns whether a row actually changed, so the endpoint can appear-down on a bogus id instead of
// answering ok about nothing.
func (s *NotifStore) ChatRename(slot int, chatID int64, title string) (bool, error) {
	db, err := s.pg(slot)
	if err != nil {
		return false, err
	}
	rows, err := db.Query(
		"UPDATE chats SET title = $1, updated_at = (extract(epoch from now())*1000)::bigint WHERE id = $2 RETURNING id",
		title, chatID)
	if err != nil {
		return false, err
	}
	return len(rows.Vals) > 0, nil
}

// ChatDelete removes a conversation and its messages. Deletion is the person's call and it is
// REAL: rows gone, not flagged , the box stores conversations for the person, not about them.
// Messages first, then the chat, so a failure between the two leaves an empty chat (visible,
// deletable again) rather than orphaned messages under a missing chat.
func (s *NotifStore) ChatDelete(slot int, chatID int64) (bool, error) {
	db, err := s.pg(slot)
	if err != nil {
		return false, err
	}
	if err := db.Exec("DELETE FROM chat_messages WHERE chat_id = $1", chatID); err != nil {
		return false, err
	}
	rows, err := db.Query("DELETE FROM chats WHERE id = $1 RETURNING id", chatID)
	if err != nil {
		return false, err
	}
	return len(rows.Vals) > 0, nil
}

// GeoPoint is one photo location for the map overlay.
type GeoPoint struct {
	Hash    string  `json:"hash"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	TakenAt int64   `json:"taken_at"`
	Place   string  `json:"place"`
}

// FramesGeo returns GPS-bearing frames inside a bounding box (or everything geotagged when the box
// is the whole world), newest first, capped. The map overlay's data: dots, not imagery.
func (s *NotifStore) FramesGeo(slot int, minLat, maxLat, minLon, maxLon float64, limit int) ([]GeoPoint, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 5000 {
		limit = 5000
	}
	rows, err := c.Query(
		"SELECT hash, lat, lon, taken_at, place FROM frames WHERE has_gps AND lat BETWEEN $1 AND $2 AND lon BETWEEN $3 AND $4 ORDER BY taken_at DESC LIMIT "+strconv.Itoa(limit),
		minLat, maxLat, minLon, maxLon)
	if err != nil {
		return nil, err
	}
	out := make([]GeoPoint, 0, len(rows.Vals))
	for _, v := range rows.Vals {
		if len(v) < 5 || v[0] == nil {
			continue
		}
		g := GeoPoint{Hash: *v[0]}
		if v[1] != nil {
			g.Lat, _ = strconv.ParseFloat(*v[1], 64)
		}
		if v[2] != nil {
			g.Lon, _ = strconv.ParseFloat(*v[2], 64)
		}
		if v[3] != nil {
			g.TakenAt, _ = strconv.ParseInt(*v[3], 10, 64)
		}
		if v[4] != nil {
			g.Place = *v[4]
		}
		out = append(out, g)
	}
	return out, nil
}

// FramesSearch matches frames whose PLACE hierarchy, display name, or tags contain every term ,
// "waterfall vancouver island" finds a photo tagged waterfall whose place mentions Vancouver
// Island, in any combination across the three surfaces. AND semantics per term, ILIKE per surface,
// newest first. This is the location-and-tags search; free-prose search stays with ghost.searchd.
func (s *NotifStore) FramesSearch(slot int, q string, limit int) ([]FrameRow, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	terms := strings.Fields(strings.TrimSpace(q))
	if len(terms) > 8 {
		terms = terms[:8]
	}
	if len(terms) == 0 {
		return nil, nil
	}
	sql := `SELECT f.hash, f.taken_at, f.kind, f.bytes, f.display_name FROM frames f WHERE true`
	args := []any{}
	for i, t := range terms {
		sql += ` AND (f.place ILIKE $` + strconv.Itoa(i+1) +
			` OR f.display_name ILIKE $` + strconv.Itoa(i+1) +
			` OR EXISTS (SELECT 1 FROM frame_tags ft WHERE ft.hash = f.hash AND ft.source <> 'user_removed' AND ft.tag ILIKE $` + strconv.Itoa(i+1) + `))`
		args = append(args, "%"+t+"%")
	}
	sql += ` ORDER BY f.taken_at DESC LIMIT ` + strconv.Itoa(limit)
	rows, err := c.Query(sql, args...)
	if err != nil {
		return nil, err
	}
	out := make([]FrameRow, 0, len(rows.Vals))
	for _, v := range rows.Vals {
		if len(v) < 5 || v[0] == nil {
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
		if v[4] != nil {
			r.Name = *v[4]
		}
		out = append(out, r)
	}
	return out, nil
}

// MemoryRow is one distilled memory for the app's MEMORIES screen.
type MemoryRow struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Kind      string `json:"kind"`
	Source    int64  `json:"source_chat,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// MemoriesList returns live (non-tombstoned) memories, newest first.
func (s *NotifStore) MemoriesList(slot int, limit int) ([]MemoryRow, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := c.Query(
		"SELECT id, title, body, kind, COALESCE(source_chat,0), created_at FROM memories WHERE NOT tombstoned ORDER BY created_at DESC LIMIT " + strconv.Itoa(limit))
	if err != nil {
		return nil, err
	}
	out := make([]MemoryRow, 0, len(rows.Vals))
	for _, v := range rows.Vals {
		if len(v) < 6 || v[0] == nil {
			continue
		}
		var m MemoryRow
		m.ID, _ = strconv.ParseInt(*v[0], 10, 64)
		if v[1] != nil {
			m.Title = *v[1]
		}
		if v[2] != nil {
			m.Body = *v[2]
		}
		if v[3] != nil {
			m.Kind = *v[3]
		}
		if v[4] != nil {
			m.Source, _ = strconv.ParseInt(*v[4], 10, 64)
		}
		if v[5] != nil {
			m.CreatedAt, _ = strconv.ParseInt(*v[5], 10, 64)
		}
		out = append(out, m)
	}
	return out, nil
}

// MemoryTombstone soft-deletes a memory: the row stays (so re-distillation of the same source chat
// cannot resurrect it , shadowd skips chats that already have memory rows, tombstoned included),
// but it leaves every list and every retrieval. The person's deletion outranks the model, forever.
func (s *NotifStore) MemoryTombstone(slot int, id int64) (bool, error) {
	c, err := s.pg(slot)
	if err != nil {
		return false, err
	}
	rows, err := c.Query("UPDATE memories SET tombstoned = TRUE, updated_at = (extract(epoch from now())*1000)::bigint WHERE id = $1 AND NOT tombstoned RETURNING id", id)
	if err != nil {
		return false, err
	}
	return len(rows.Vals) > 0, nil
}

// MemoryAdd inserts a user-authored memory. kind='user', user_edited from birth , the model never
// touches it.
func (s *NotifStore) MemoryAdd(slot int, title, body string) (int64, error) {
	c, err := s.pg(slot)
	if err != nil {
		return 0, err
	}
	now := time.Now().UnixMilli()
	rows, err := c.Query(
		"INSERT INTO memories (title, body, kind, created_at, updated_at, user_edited) VALUES ($1,$2,'user',$3,$3,TRUE) RETURNING id",
		title, body, now)
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return 0, err
	}
	id, _ := strconv.ParseInt(*rows.Vals[0][0], 10, 64)
	return id, nil
}

// MemoryEdit updates a memory's text and marks it user_edited , from then on re-distillation may
// never overwrite it. The person's version IS the memory.
func (s *NotifStore) MemoryEdit(slot int, id int64, title, body string) (bool, error) {
	c, err := s.pg(slot)
	if err != nil {
		return false, err
	}
	rows, err := c.Query(
		"UPDATE memories SET title=$1, body=$2, user_edited=TRUE, updated_at=$3 WHERE id=$4 AND NOT tombstoned RETURNING id",
		title, body, time.Now().UnixMilli(), id)
	if err != nil {
		return false, err
	}
	return len(rows.Vals) > 0, nil
}

// DaySummary is what framed knows about one day , the prefill data for the daily check-in ("how
// are you feeling and why") and anything else that wants "what did today look like" in one row.
type DaySummary struct {
	Photos int      `json:"photos"`
	Videos int      `json:"videos"`
	Places []string `json:"places,omitempty"`
	First  int64    `json:"first,omitempty"` // earliest capture, unix seconds
	Last   int64    `json:"last,omitempty"`
	Notes  []string `json:"notes,omitempty"` // journal entry titles from today (all sources)
	Steps           int      `json:"steps,omitempty"`
	SleepMinutes    int      `json:"sleep_minutes,omitempty"`
	ExerciseMinutes int      `json:"exercise_minutes,omitempty"`
	// Suggested feelings , GUESSES from the day's shape, offered first in the check-in, never
	// preselected: the box proposes, the person disposes. The heuristics are plain and stated:
	// short sleep suggests tired; real movement or exercise suggests energised; a park or trail
	// in the places suggests calm; a heavy-everything day suggests stressed as a candidate.
	Suggested []string `json:"suggested,omitempty"`
}

// DayContext summarizes one day from frames + journal. start/end are unix seconds bounding the day
// in the CALLER's timezone , the box does not guess where the person woke up.
func (s *NotifStore) DayContext(slot int, start, end int64) (DaySummary, error) {
	var out DaySummary
	c, err := s.pg(slot)
	if err != nil {
		return out, err
	}
	rows, err := c.Query(
		"SELECT kind, COALESCE(place,''), taken_at FROM frames WHERE taken_at >= $1 AND taken_at < $2 ORDER BY taken_at ASC LIMIT 2000",
		start, end)
	if err != nil {
		return out, err
	}
	for _, v := range rows.Vals {
		if len(v) < 3 || v[0] == nil || v[2] == nil {
			continue
		}
		ts, _ := strconv.ParseInt(*v[2], 10, 64)
		if out.First == 0 || ts < out.First {
			out.First = ts
		}
		if ts > out.Last {
			out.Last = ts
		}
		switch *v[0] {
		case "photo":
			out.Photos++
		case "video":
			out.Videos++
		}
		if v[1] != nil && *v[1] != "" {
			dup := false
			for _, p := range out.Places {
				if p == *v[1] {
					dup = true
					break
				}
			}
			if !dup && len(out.Places) < 5 {
				out.Places = append(out.Places, *v[1])
			}
		}
	}
	if jrows, jerr := c.Query(
		"SELECT title FROM journal_entries WHERE ts >= $1 AND ts < $2 AND title <> '' ORDER BY ts ASC LIMIT 8",
		start, end); jerr == nil {
		for _, v := range jrows.Vals {
			if len(v) >= 1 && v[0] != nil {
				out.Notes = append(out.Notes, *v[0])
			}
		}
	}
	// Health for the day (the PHONE's YYYY-MM-DD derived from the day start it sent).
	dayStr := time.Unix(start, 0).UTC().Format("2006-01-02")
	if hrows, herr := c.Query("SELECT metric, value FROM health_metrics WHERE day = $1", dayStr); herr == nil {
		for _, v := range hrows.Vals {
			if len(v) < 2 || v[0] == nil || v[1] == nil {
				continue
			}
			val, _ := strconv.ParseFloat(*v[1], 64)
			switch *v[0] {
			case "steps":
				out.Steps = int(val)
			case "sleep_minutes":
				out.SleepMinutes = int(val)
			case "exercise_minutes":
				out.ExerciseMinutes = int(val)
			}
		}
	}
	// Suggestions , transparent heuristics over the day's shape.
	sug := func(f string) {
		for _, s := range out.Suggested {
			if s == f {
				return
			}
		}
		if len(out.Suggested) < 4 {
			out.Suggested = append(out.Suggested, f)
		}
	}
	if out.SleepMinutes > 0 && out.SleepMinutes < 360 {
		sug("tired")
	}
	if out.ExerciseMinutes >= 30 || out.Steps >= 9000 {
		sug("energised")
	}
	for _, p := range out.Places {
		lp := strings.ToLower(p)
		if strings.Contains(lp, "park") || strings.Contains(lp, "trail") || strings.Contains(lp, "falls") {
			sug("calm")
			break
		}
	}
	if out.SleepMinutes > 0 && out.SleepMinutes < 360 && out.Steps >= 12000 {
		sug("stressed")
	}
	if out.SleepMinutes >= 480 {
		sug("calm")
	}
	return out, nil
}

// HealthSeries is one metric's daily values over a window , the stats screen's food.
type HealthSeries struct {
	Metric string    `json:"metric"`
	Days   []string  `json:"days"`
	Values []float64 `json:"values"`
}

// HealthStats returns every daily metric's series for the last n days, oldest first per series.
func (s *NotifStore) HealthStats(slot int, n int) ([]HealthSeries, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	if n <= 0 || n > 365 {
		n = 30
	}
	since := time.Now().AddDate(0, 0, -n).Format("2006-01-02")
	rows, err := c.Query(
		"SELECT metric, day, value FROM health_metrics WHERE day >= $1 ORDER BY metric, day ASC", since)
	if err != nil {
		return nil, err
	}
	out := []HealthSeries{}
	for _, v := range rows.Vals {
		if len(v) < 3 || v[0] == nil || v[1] == nil || v[2] == nil {
			continue
		}
		val, _ := strconv.ParseFloat(*v[2], 64)
		if len(out) == 0 || out[len(out)-1].Metric != *v[0] {
			out = append(out, HealthSeries{Metric: *v[0]})
		}
		last := &out[len(out)-1]
		last.Days = append(last.Days, *v[1])
		last.Values = append(last.Values, val)
	}
	return out, nil
}

// GeoCluster is one aggregated map point: a grid cell's centroid, how many frames fell in it, and
// (when the cell holds exactly one) that frame's hash so the app can open it.
type GeoCluster struct {
	Lat, Lon float64 `json:"lat,omitempty"`
	N        int     `json:"n"`
	Hash     string  `json:"hash,omitempty"`
	TakenAt  int64   `json:"takenAt,omitempty"`
}

// geoLevelPrecision , four LOD tiers. The map picks by zoom; POSTGRES does the aggregation, so a
// world view ships a few hundred rows instead of 50,000 points the canvas then has to overdraw.
// Level 3 is raw frames (already bbox-limited by the client's viewport at that zoom).
func geoLevelPrecision(level int) float64 {
	switch level {
	case 0:
		return 1.0 // continent , ~110km cells
	case 1:
		return 0.1 // region , ~11km
	case 2:
		return 0.01 // town , ~1.1km
	default:
		return 0 // raw
	}
}

// FramesGeoLOD returns aggregated map points for a bbox at one of four levels. Level 3 returns
// individual frames (hash included) so tapping opens the photo; lower levels return cell centroids
// with counts. The 100m-clump problem solves itself at level 3 plus deep zoom , the cells vanish
// and the individual dots spread.
func (s *NotifStore) FramesGeoLOD(slot, level int, minLat, maxLat, minLon, maxLon float64, limit int) ([]GeoCluster, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	// HARD CEILING 500 , the two-person archive froze the phone: 43k points into a Compose
	// canvas is not a map, it is a hang. Clusters are ordered biggest-first so the 500 you get
	// are the 500 that matter; raw tier keeps newest-first. The app never needs its own guard
	// because the API is incapable of flooding it.
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	prec := geoLevelPrecision(level)
	if prec == 0 {
		rows, qerr := c.Query(
			"SELECT lat, lon, hash, taken_at FROM frames WHERE has_gps AND lat BETWEEN $1 AND $2 AND lon BETWEEN $3 AND $4 ORDER BY taken_at DESC LIMIT "+strconv.Itoa(limit),
			minLat, maxLat, minLon, maxLon)
		if qerr != nil {
			return nil, qerr
		}
		out := make([]GeoCluster, 0, len(rows.Vals))
		for _, v := range rows.Vals {
			if len(v) < 4 || v[0] == nil || v[1] == nil {
				continue
			}
			g := GeoCluster{N: 1}
			g.Lat, _ = strconv.ParseFloat(*v[0], 64)
			g.Lon, _ = strconv.ParseFloat(*v[1], 64)
			if v[2] != nil {
				g.Hash = *v[2]
			}
			if v[3] != nil {
				g.TakenAt, _ = strconv.ParseInt(*v[3], 10, 64)
			}
			out = append(out, g)
		}
		return out, nil
	}
	rows, err := c.Query(`
		SELECT avg(lat), avg(lon), count(*), min(hash), max(taken_at)
		FROM frames
		WHERE has_gps AND lat BETWEEN $1 AND $2 AND lon BETWEEN $3 AND $4
		GROUP BY floor(lat/$5), floor(lon/$5)
		ORDER BY count(*) DESC LIMIT `+strconv.Itoa(limit),
		minLat, maxLat, minLon, maxLon, prec)
	if err != nil {
		return nil, err
	}
	out := make([]GeoCluster, 0, len(rows.Vals))
	for _, v := range rows.Vals {
		if len(v) < 5 || v[0] == nil || v[1] == nil || v[2] == nil {
			continue
		}
		var g GeoCluster
		g.Lat, _ = strconv.ParseFloat(*v[0], 64)
		g.Lon, _ = strconv.ParseFloat(*v[1], 64)
		nn, _ := strconv.Atoi(*v[2])
		g.N = nn
		if nn == 1 && v[3] != nil {
			g.Hash = *v[3]
		}
		if v[4] != nil {
			g.TakenAt, _ = strconv.ParseInt(*v[4], 10, 64)
		}
		out = append(out, g)
	}
	return out, nil
}

// NewestGeoFrame returns the most recent geotagged frame , the map's opening view centres here.
func (s *NotifStore) NewestGeoFrame(slot int) (GeoCluster, error) {
	c, err := s.pg(slot)
	if err != nil {
		return GeoCluster{}, err
	}
	rows, err := c.Query("SELECT lat, lon, hash, taken_at FROM frames WHERE has_gps ORDER BY taken_at DESC LIMIT 1")
	if err != nil || len(rows.Vals) == 0 {
		return GeoCluster{}, err
	}
	v := rows.Vals[0]
	g := GeoCluster{N: 1}
	if len(v) >= 4 {
		if v[0] != nil {
			g.Lat, _ = strconv.ParseFloat(*v[0], 64)
		}
		if v[1] != nil {
			g.Lon, _ = strconv.ParseFloat(*v[1], 64)
		}
		if v[2] != nil {
			g.Hash = *v[2]
		}
		if v[3] != nil {
			g.TakenAt, _ = strconv.ParseInt(*v[3], 10, 64)
		}
	}
	return g, nil
}

// DaemonKV is one row of a daemon's drill-in summary.
type DaemonKV struct {
	K string `json:"k"`
	V string `json:"v"`
}

// DaemonSummary , the per-daemon screens' feed. Each daemon's domain summarized from ITS OWN
// tables (rule 1 in DATA.md: single writer, single reader-of-record). All flat SELECTs.
func (s *NotifStore) DaemonSummary(slot int, name string) ([]DaemonKV, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	one := func(q string, args ...any) string {
		rows, qerr := c.Query(q, args...)
		if qerr != nil || len(rows.Vals) == 0 || len(rows.Vals[0]) == 0 || rows.Vals[0][0] == nil {
			return "0"
		}
		return *rows.Vals[0][0]
	}
	kv := []DaemonKV{}
	add := func(k, v string) { kv = append(kv, DaemonKV{k, v}) }
	switch name {
	case "ghost.framed":
		add("frames archived", one("SELECT count(*) FROM frames"))
		add("photos", one("SELECT count(*) FROM frames WHERE kind = 'photo'"))
		add("videos", one("SELECT count(*) FROM frames WHERE kind = 'video'"))
		add("geotagged", one("SELECT count(*) FROM frames WHERE has_gps"))
		add("placed", one("SELECT count(*) FROM frames WHERE place <> ''"))
		add("named", one("SELECT count(*) FROM frames WHERE display_name <> ''"))
		add("described", one("SELECT count(*) FROM frames WHERE description <> ''"))
		add("tagged", one("SELECT count(DISTINCT hash) FROM frame_tags WHERE source <> 'user_removed'"))
		add("caption queue", one("SELECT count(*) FROM search.jobs WHERE kind = 'caption' AND attempts < 5"))
		add("captions exhausted", one("SELECT count(*) FROM search.jobs WHERE kind = 'caption' AND attempts >= 5"))
		add("track points", one("SELECT count(*) FROM location_points"))
		add("geo places loaded", one("SELECT count(*) FROM geo_points"))
		if ts := one("SELECT to_char(to_timestamp(max(taken_at)), 'YYYY-MM-DD HH24:MI') FROM frames"); ts != "0" && ts != "" {
			add("newest capture", ts)
		}
		if ents, derr := os.ReadDir("/var/lib/ghost/backup"); derr == nil {
			seals, latest := 0, ""
			for _, e := range ents {
				if strings.HasSuffix(e.Name(), ".tar.seal") {
					seals++
					if e.Name() > latest {
						latest = e.Name()
					}
				}
			}
			if seals > 0 {
				add("backups on disk", strconv.Itoa(seals))
				add("latest backup", latest)
			} else {
				add("backups", "directory present, none written yet")
			}
		} else {
			add("backups", "idle , no key (ghost.restore keygen)")
		}
	case "ghost.noted":
		add("journal entries", one("SELECT count(*) FROM journal_entries"))
		add("from noted", one("SELECT count(*) FROM journal_entries WHERE source = 'ghost.noted'"))
		add("from framed", one("SELECT count(*) FROM journal_entries WHERE source = 'ghost.framed'"))
		add("from tallyd", one("SELECT count(*) FROM journal_entries WHERE source = 'ghost.tallyd'"))
		add("awaiting distillation", one("SELECT count(*) FROM journal_entries WHERE NOT distilled"))
	case "ghost.synthd":
		add("memories (live)", one("SELECT count(*) FROM memories WHERE NOT tombstoned"))
		add("yours (user-made)", one("SELECT count(*) FROM memories WHERE kind = 'user' AND NOT tombstoned"))
		add("tombstoned", one("SELECT count(*) FROM memories WHERE tombstoned"))
		add("distill queue", one("SELECT count(*) FROM journal_entries WHERE NOT distilled"))
		add("day episodes", one("SELECT count(*) FROM memories WHERE kind = 'episode' AND NOT tombstoned"))
		add("cached reports", one("SELECT count(*) FROM reports"))
	case "ghost.searchd":
		add("caption jobs pending", one("SELECT count(*) FROM search.jobs WHERE kind = 'caption' AND attempts < 5"))
		add("caption jobs exhausted", one("SELECT count(*) FROM search.jobs WHERE kind = 'caption' AND attempts >= 5"))
		add("tag jobs pending", one("SELECT count(*) FROM search.jobs WHERE kind = 'tag' AND attempts < 5"))
		add("embed jobs pending", one("SELECT count(*) FROM search.jobs WHERE kind = 'embed_text' AND attempts < 5"))
		add("other jobs pending", one("SELECT count(*) FROM search.jobs WHERE kind NOT IN ('caption','tag','embed_text') AND attempts < 5"))
		add("indexed chunks", one("SELECT count(*) FROM search.chunks"))
		add("tags written", one("SELECT count(*) FROM frame_tags WHERE source <> 'user_removed'"))
	case "ghost.tallyd":
		add("health days", one("SELECT count(DISTINCT day) FROM health_metrics"))
		add("metrics rows", one("SELECT count(*) FROM health_metrics"))
		add("high-res samples", one("SELECT count(*) FROM health_samples"))
		if d := one("SELECT min(day) FROM health_metrics"); d != "0" {
			add("earliest day", d)
		}
	case "ghost.shadowd":
		add("charter", "anti-possession: watches usage patterns FOR you, never for engagement")
		add("your messages (7d)", one("SELECT count(*) FROM chat_messages WHERE role = 'user' AND ts >= extract(epoch from now())::bigint - 7*86400"))
		add("prior 7d", one("SELECT count(*) FROM chat_messages WHERE role = 'user' AND ts >= extract(epoch from now())::bigint - 14*86400 AND ts < extract(epoch from now())::bigint - 7*86400"))
		add("days you talked (14d)", one("SELECT count(DISTINCT to_char(to_timestamp(ts),'YYYY-MM-DD')) FROM chat_messages WHERE role = 'user' AND ts >= extract(epoch from now())::bigint - 14*86400"))
		add("detector", "interaction trend LIVE; next: sunk-cost, topic narrowing")
	case "ghost.cued":
		add("charter", "surfaces reflections from your life , offerings, not homework")
		add("episodes to draw from", one("SELECT count(*) FROM memories WHERE kind = 'episode' AND NOT tombstoned"))
		add("last reflection", one("SELECT coalesce(value,'never') FROM settings WHERE key = 'cued_reflected'"))
	case "ghost.oracled":
		add("role", "the only daemon that talks to the model; everyone else asks it")
		add("queue + model state", "see Box Status sparklines (stats sampler)")
	default:
		add("note", "no drill-in for this daemon yet")
	}
	return kv, nil
}

// GetSetting / SetSetting , the shared settings KV (single row per key).
func (s *NotifStore) GetSetting(slot int, key string) (string, error) {
	c, err := s.pg(slot)
	if err != nil {
		return "", err
	}
	rows, err := c.Query("SELECT value FROM settings WHERE key = $1", key)
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return "", err
	}
	return *rows.Vals[0][0], nil
}

func (s *NotifStore) SetSetting(slot int, key, value string) error {
	c, err := s.pg(slot)
	if err != nil {
		return err
	}
	return c.Exec("INSERT INTO settings (key, value) VALUES ($1,$2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", key, value)
}

// CheckinDoneToday , did a daily check-in land in the journal for this date.
func (s *NotifStore) CheckinDoneToday(slot int, day string) (bool, error) {
	c, err := s.pg(slot)
	if err != nil {
		return false, err
	}
	rows, err := c.Query(
		"SELECT 1 FROM journal_entries WHERE source = 'ghost.noted' AND title LIKE 'Daily check-in%' AND body LIKE '%' || $1 || '%' LIMIT 1", day)
	if err != nil {
		return false, err
	}
	return len(rows.Vals) > 0, nil
}

// CheckinRow is one past daily check-in, parsed back out of its journal entry.
type CheckinRow struct {
	Day      string `json:"day"`
	Feelings string `json:"feelings"`
	Why      string `json:"why,omitempty"`
}

// CheckinHistory , past check-ins, newest first. The check-in is a journal entry by design (one
// write path, full sovereignty); this parses the structured text back into rows for the strip and
// the "yesterday you felt" continuity line.
func (s *NotifStore) CheckinHistory(slot, n int) ([]CheckinRow, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	if n <= 0 || n > 90 {
		n = 30
	}
	rows, err := c.Query(
		"SELECT body FROM journal_entries WHERE source = 'ghost.noted' AND title LIKE 'Daily check-in%' ORDER BY ts DESC LIMIT "+strconv.Itoa(n))
	if err != nil {
		return nil, err
	}
	out := make([]CheckinRow, 0, len(rows.Vals))
	for _, v := range rows.Vals {
		if len(v) == 0 || v[0] == nil {
			continue
		}
		var r CheckinRow
		for _, line := range strings.Split(*v[0], "\n") {
			line = strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(line, "Daily check-in "):
				r.Day = strings.TrimPrefix(line, "Daily check-in ")
			case strings.HasPrefix(line, "Feeling: "):
				r.Feelings = strings.TrimPrefix(line, "Feeling: ")
			case strings.HasPrefix(line, "Why: "):
				r.Why = strings.TrimPrefix(line, "Why: ")
			}
		}
		if r.Day != "" {
			out = append(out, r)
		}
	}
	return out, nil
}

// FrameOriginalPath , the untouched archived original and its mime, for the full-quality viewer.
func (s *NotifStore) FrameOriginalPath(slot int, hash string) (string, string, error) {
	c, err := s.pg(slot)
	if err != nil {
		return "", "", err
	}
	rows, err := c.Query("SELECT archive_path, mime FROM frames WHERE hash = $1", hash)
	if err != nil || len(rows.Vals) == 0 {
		return "", "", err
	}
	v := rows.Vals[0]
	p, m := "", ""
	if len(v) > 0 && v[0] != nil {
		p = *v[0]
	}
	if len(v) > 1 && v[1] != nil {
		m = *v[1]
	}
	return p, m, nil
}

// ResetSyncCursors zeroes THIS device's sync cursors , the app then re-offers its entire library
// from the beginning, and hash dedup archives only what the box lacks. Per-device by design: one
// phone rewinding does not touch another's progress.
func (s *NotifStore) ResetSyncCursors(slot int, device string) error {
	c, err := s.pg(slot)
	if err != nil {
		return err
	}
	return c.Exec("DELETE FROM sync_cursors WHERE device = $1", device)
}
