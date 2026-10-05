package hw

import (
	"strconv"
	"strings"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// VoiceNote is one voice note as the app lists it.
type VoiceNote struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Day        string `json:"day"`
	TakenAt    int64  `json:"taken_at"`
	DurationMS int64  `json:"duration_ms"`
	Status     string `json:"status"`
	Transcript string `json:"transcript,omitempty"`
	Lang       string `json:"lang,omitempty"`
	Error      string `json:"error,omitempty"`
}

const voiceCols = "id, kind, day, taken_at, duration_ms, status, transcript, lang, error"

func voiceRow(v []*string) VoiceNote {
	s := func(i int) string {
		if i < len(v) && v[i] != nil {
			return *v[i]
		}
		return ""
	}
	n := VoiceNote{ID: s(0), Kind: s(1), Day: s(2), Status: s(5), Transcript: s(6), Lang: s(7), Error: s(8)}
	n.TakenAt, _ = strconv.ParseInt(s(3), 10, 64)
	n.DurationMS, _ = strconv.ParseInt(s(4), 10, 64)
	return n
}

// VoiceHave: the box already holds this note, archived or still in the inbox's row.
func (s *NotifStore) VoiceHave(slot int, id string) (bool, error) {
	c, err := s.pg(slot)
	if err != nil {
		return false, err
	}
	rows, err := c.Query("SELECT 1 FROM voice_notes WHERE id = $1", id)
	if err != nil {
		return false, err
	}
	return len(rows.Vals) > 0, nil
}

// VoiceNotes: the newest n notes, newest first.
func (s *NotifStore) VoiceNotes(slot, n int) ([]VoiceNote, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	if n <= 0 || n > 200 {
		n = 60
	}
	rows, err := c.Query("SELECT " + voiceCols + " FROM voice_notes ORDER BY taken_at DESC LIMIT " + strconv.Itoa(n))
	if err != nil {
		return nil, err
	}
	out := make([]VoiceNote, 0, len(rows.Vals))
	for _, v := range rows.Vals {
		out = append(out, voiceRow(v))
	}
	return out, nil
}

// VoicePath: where the note's audio sits, relative to the mount.
func (s *NotifStore) VoicePath(slot int, id string) (string, error) {
	c, err := s.pg(slot)
	if err != nil {
		return "", err
	}
	rows, err := c.Query("SELECT path FROM voice_notes WHERE id = $1", id)
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return "", err
	}
	return *rows.Vals[0][0], nil
}

// VoiceDelete removes the note's row and its journal entry and returns the audio's relative path
// for the caller to remove. The person's deletion is final: a transcription finishing afterwards
// finds no row and writes nothing.
func (s *NotifStore) VoiceDelete(slot int, id string) (string, bool, error) {
	c, err := s.pg(slot)
	if err != nil {
		return "", false, err
	}
	rows, err := c.Query("DELETE FROM voice_notes WHERE id = $1 RETURNING path", id)
	if err != nil {
		return "", false, err
	}
	if err := c.Exec("DELETE FROM journal_entries WHERE source = 'ghost.voiced' AND ref = $1", "voice:"+id); err != nil {
		return "", len(rows.Vals) > 0, err
	}
	if len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return "", false, nil
	}
	return *rows.Vals[0][0], true, nil
}

// voiceByIDs fills check-in rows with their notes (the "Voice: <id>" line of the check-in text).
// voiceOfDays , the notes of a kind on each of the days given, in the order said.
func voiceOfDays(c *poltergres.ReadWrite, kind string, days []string) map[string][]VoiceNote {
	out := map[string][]VoiceNote{}
	if len(days) == 0 {
		return out
	}
	rows, err := c.Query("SELECT "+voiceCols+" FROM voice_notes WHERE kind = $1 AND day = ANY($2) ORDER BY taken_at", kind, "{"+strings.Join(days, ",")+"}")
	if err != nil {
		return out
	}
	for _, v := range rows.Vals {
		n := voiceRow(v)
		out[n.Day] = append(out[n.Day], n)
	}
	return out
}

func voiceByIDs(c *poltergres.ReadWrite, ids []string) map[string]VoiceNote {
	out := map[string]VoiceNote{}
	if len(ids) == 0 {
		return out
	}
	rows, err := c.Query("SELECT "+voiceCols+" FROM voice_notes WHERE id = ANY($1)", "{"+strings.Join(ids, ",")+"}")
	if err != nil {
		return out
	}
	for _, v := range rows.Vals {
		n := voiceRow(v)
		out[n.ID] = n
	}
	return out
}
