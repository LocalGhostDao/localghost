package secd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
)

// notificationServices is the allow-list of notification-producing daemons the mute can target. The
// front door (ghost.secd) is not here , it produces no notifications. A per-service mute scope MUST be
// one of these (or "*" for global); the handler rejects anything else, so a scope can never be an
// arbitrary string reaching the DB.
var notificationServices = map[string]bool{
	"ghost.noted":   true,
	"ghost.framed":  true,
	"ghost.voiced":  true,
	"ghost.tallyd":  true,
	"ghost.synthd":  true,
	"ghost.cued":    true,
	"ghost.mistd":   true,
	"ghost.shadowd": true,
	"ghost.watchd":  true,
}

// handleNotifications is what the app's background poller hits every ~15 minutes. Causes that converge
// on the same "appears down" 502 (an observer / a poller on a seized phone cannot tell them apart):
//
//   1. locked / DBs not up        -> down  (honest: nothing that holds notifications is running)
//   2. wrong / expired token      -> down  (appears-down deniability; shared fate with foreground)
//   3. unlocked + GLOBALLY muted  -> down  (the user's global notification mute)
//
// A PER-SERVICE mute does NOT make the poll appear down (the app is not "down", just one service is
// quiet); instead muted services are filtered out of the returned notification list. Only the GLOBAL
// mute collapses the whole surface to "down", because a globally-muted box should look exactly like a
// down one. The down response is a bare 502 -> nginx @down -> identical bytes to real downtime.
func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	// (2) token gate first. No valid token -> appears down (also covers the post-wrong-PIN revoke).
	if !s.session.Valid(bearer(r)) {
		s.appearsDown(w)
		return
	}
	// (1) must be unlocked with DBs up to read anything.
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	// (3) GLOBAL mute -> whole surface appears down.
	if s.globalMuted(mounted) {
		s.appearsDown(w)
		return
	}

	// Unlocked, valid token, not globally muted: read the not-yet-pushed notifications for THIS device
	// (cursor advances past muted ones too, so they are skipped from push, not delayed , Option A), with
	// per-service muted scopes removed from the payload. The full history (incl. muted) stays in the
	// store for the in-app list with seen/delete state.
	if s.notif == nil {
		writeJSON(w, map[string]any{"available": true, "notifications": []any{}})
		return
	}
	device := deviceKey(r)
	batch, err := s.notif.PushBatch(mounted, device, func(service string) bool {
		return s.serviceMuted(mounted, service)
	})
	if err != nil {
		// a read failure should look like the box is momentarily down, not leak an error surface.
		s.appearsDown(w)
		return
	}
	writeJSON(w, map[string]any{"available": true, "notifications": batch})
}

// appearsDown emits a bare 502; nginx proxy_intercept_errors + error_page maps it to the one fixed
// @down response, identical to a genuinely-down upstream. No distinguishing body.
func (s *Server) appearsDown(w http.ResponseWriter) {
	w.WriteHeader(http.StatusBadGateway) // 502 -> nginx @down
}

// globalMuted reports whether the GLOBAL mute is active (collapses the surface to down).
func (s *Server) globalMuted(slot int) bool {
	if s.mute == nil {
		return false
	}
	return s.mute.GlobalMuted(slot)
}

// serviceMuted reports whether a specific service is muted (global OR its own per-service mute).
func (s *Server) serviceMuted(slot int, service string) bool {
	if s.mute == nil {
		return false
	}
	return s.mute.IsMuted(slot, service)
}

// bearer extracts the session token from "Authorization: Bearer <t>" or the X-Ghost-Token header.
func bearer(r *http.Request) string {
	if h := r.Header.Get("X-Ghost-Token"); h != "" {
		return h
	}
	const p = "Bearer "
	if a := r.Header.Get("Authorization"); len(a) > len(p) && a[:len(p)] == p {
		return a[len(p):]
	}
	return ""
}

// handleMute is the app-settings control to arm or clear a notification mute, global or per-service.
// A DELIBERATE foreground action in the normal (working) app: requires a valid token + mounted volume,
// returns real JSON (not appears-down). The app's settings screen uses it to show active mutes and to
// set a duration (presets 1h/1d/1w, a custom length, or forever) or clear, for the global scope ("*")
// or a specific service.
//
// Body (POST):
//   {"scope":"*"|"ghost.synthd", "preset":"1h"|"1d"|"1w"|"forever"}   preset duration
//   {"scope":..., "minutes":N, "hours":H, "days":D}                   custom length (summed)
//   {"scope":..., "clear":true}                                       re-enable
// GET returns the active mutes for the screen.
func (s *Server) handleMute(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) {
		writeErr(w, http.StatusUnauthorized, "no session")
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 || s.mute == nil {
		writeErr(w, http.StatusConflict, "locked")
		return
	}

	if r.Method == http.MethodGet {
		status, err := s.mute.Status(mounted)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "mute status failed")
			return
		}
		out := map[string]int64{}
		for scope, until := range status {
			out[scope] = until.Unix()
		}
		writeJSON(w, map[string]any{"mutes": out})
		return
	}

	var req struct {
		Scope   string `json:"scope"`
		Preset  string `json:"preset"`
		Minutes int    `json:"minutes"`
		Hours   int    `json:"hours"`
		Days    int    `json:"days"`
		Clear   bool   `json:"clear"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad body")
		return
	}
	// validate scope: "*" (global) or a known notification service. Never an arbitrary string.
	if req.Scope != "*" && !notificationServices[req.Scope] {
		writeErr(w, http.StatusBadRequest, "unknown scope")
		return
	}

	var err error
	switch {
	case req.Clear:
		err = s.mute.ClearMute(mounted, req.Scope)
	case req.Preset == "forever":
		err = s.mute.MuteForever(mounted, req.Scope)
	default:
		d, derr := muteDuration(req.Preset, req.Minutes, req.Hours, req.Days)
		if derr != nil {
			writeErr(w, http.StatusBadRequest, derr.Error())
			return
		}
		err = s.mute.SetMute(mounted, req.Scope, time.Now().Add(d))
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "mute update failed")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// muteDuration resolves a preset (1h/1d/1w) or a custom length (days/hours/minutes summed) to a
// Duration. Forever is handled by the caller (MuteForever), not here.
func muteDuration(preset string, minutes, hours, days int) (time.Duration, error) {
	switch preset {
	case "1h":
		return time.Hour, nil
	case "1d":
		return 24 * time.Hour, nil
	case "1w":
		return 7 * 24 * time.Hour, nil
	case "":
		d := time.Duration(days)*24*time.Hour + time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute
		if d <= 0 {
			return 0, muteErr("specify a preset, a custom length, forever, or clear")
		}
		return d, nil
	default:
		return 0, muteErr("unknown preset")
	}
}

type muteErr string

func (e muteErr) Error() string { return string(e) }

// deviceKey is a compact, stable per-device id for the push cursor, derived from the client cert nginx
// passes (clientID). Hashed so the Redis key is short and contains no cert bytes.
func deviceKey(r *http.Request) string {
	sum := sha256.Sum256([]byte(clientID(r)))
	return hex.EncodeToString(sum[:8]) // 16 hex chars, enough to distinguish devices
}

// handleNotificationList returns the in-app notification history (newest first), the FULL list
// regardless of mute or push cursor, with seen/delete state. A deliberate foreground action: valid
// token + mounted required.
func (s *Server) handleNotificationList(w http.ResponseWriter, r *http.Request) {
	mounted, ok := s.notifGate(w, r)
	if !ok {
		return
	}
	items, err := s.notif.List(mounted, 100)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, map[string]any{"notifications": items})
}

// handleNotificationSeen marks a notification seen (POST {"id":N}).
func (s *Server) handleNotificationSeen(w http.ResponseWriter, r *http.Request) {
	mounted, ok := s.notifGate(w, r)
	if !ok {
		return
	}
	id, ok := decodeID(w, r)
	if !ok {
		return
	}
	if err := s.notif.MarkSeen(mounted, id); err != nil {
		writeErr(w, http.StatusInternalServerError, "seen failed")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleNotificationDelete removes a notification forever (POST {"id":N}).
func (s *Server) handleNotificationDelete(w http.ResponseWriter, r *http.Request) {
	mounted, ok := s.notifGate(w, r)
	if !ok {
		return
	}
	id, ok := decodeID(w, r)
	if !ok {
		return
	}
	if err := s.notif.Delete(mounted, id); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete failed")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleNotificationAnswer records the user's response to an ask (POST {"id":N,"answer":"Keep"}). A
// deliberate foreground action, like seen/delete: valid token + mounted, normal JSON errors (not
// appears-down). The store validates the choice against the ask's own options, so an answer can never
// be an arbitrary string reaching the data model, and a second answer to the same ask is rejected.
func (s *Server) handleNotificationAnswer(w http.ResponseWriter, r *http.Request) {
	mounted, ok := s.notifGate(w, r)
	if !ok {
		return
	}
	var req struct {
		ID     int64  `json:"id"`
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 || req.Answer == "" {
		writeErr(w, http.StatusBadRequest, "bad id or answer")
		return
	}
	switch err := s.notif.Answer(mounted, req.ID, req.Answer); err {
	case nil:
		writeJSON(w, map[string]any{"ok": true})
	case hw.ErrNoNotif:
		writeErr(w, http.StatusNotFound, "no such notification")
	case hw.ErrNotAsk:
		writeErr(w, http.StatusBadRequest, "not an ask")
	case hw.ErrBadChoice:
		writeErr(w, http.StatusBadRequest, "invalid choice")
	case hw.ErrAlreadyAnswered:
		writeErr(w, http.StatusConflict, "already answered")
	default:
		writeErr(w, http.StatusInternalServerError, "answer failed")
	}
}

// notifGate enforces valid token + mounted + store present for the deliberate (non-poll) notification
// actions, returning the mounted slot. These return normal errors, not appears-down (they are
// foreground actions in the working app, not the poll surface).
func (s *Server) notifGate(w http.ResponseWriter, r *http.Request) (int, bool) {
	if !s.session.Valid(bearer(r)) {
		writeErr(w, http.StatusUnauthorized, "no session")
		return 0, false
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 || s.notif == nil {
		writeErr(w, http.StatusConflict, "locked")
		return 0, false
	}
	return mounted, true
}

func decodeID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
		writeErr(w, http.StatusBadRequest, "bad id")
		return 0, false
	}
	return req.ID, true
}

// checkinReminderLoop , the box asking, once a day in the early evening, if the person has not
// already answered: "how are you feeling today?". The rule against engagement bait applies:
// exactly one ask, no streaks, no guilt language, silence after. If the person checked in
// (the app journals it via /v1/notes), no notification at all.
func (s *Server) checkinReminderLoop() {
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		mounted := s.mounted
		s.mu.Unlock()
		if mounted < 0 || s.notif == nil {
			continue
		}
		h := time.Now().Hour()
		if h < 19 || h > 21 {
			continue
		}
		today := time.Now().Format("2006-01-02")
		if v, err := s.notif.GetSetting(mounted, "checkin_reminded"); err == nil && v == today {
			continue
		}
		if done, err := s.notif.CheckinDoneToday(mounted, today); err != nil || done {
			if err == nil {
				_ = s.notif.SetSetting(mounted, "checkin_reminded", today) // answered on their own , stay silent
			}
			continue
		}
		_ = s.notif.Produce(mounted, hw.Notification{
			Service: "ghost.secd", Kind: "checkin",
			Title: "how are you feeling today?",
			Body:  "your day is already prefilled , 30 seconds on the MEMORIES screen",
		})
		_ = s.notif.SetSetting(mounted, "checkin_reminded", today)
	}
}
