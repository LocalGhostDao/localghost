package hw

import (
	"encoding/json"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// SynthStatus is what ghost.synthd says of itself for Box Status: written to settings
// 'synthd_status' at the end of every pass (synthd is the single writer of its own key), read by
// the status rows here. The counts of memories, days and the queue are read from the tables; this
// carries what the tables cannot say: when the last pass ran and what it did, the Wikipedia file as
// the daemon sees it (open, downloading, missing, failed), and the last daily consolidation.
type SynthStatus struct {
	At   int64          `json:"at"`   // the end of the last pass, unix seconds
	Pass map[string]int `json:"pass"` // what the last pass did, by step: distilled, outings, days, prose, folded, trips, people, named, about, checkins, places, insights
	Wiki SynthWiki      `json:"wiki"`
	// ConsolidatedDay is the last day the model wrote the people (the daily consolidation).
	ConsolidatedDay string `json:"consolidatedDay,omitempty"`
}

// SynthWiki is the box's Wikipedia as synthd sees it: in Postgres once the file is imported.
type SynthWiki struct {
	State       string `json:"state"`                 // ready | importing | downloading | missing | failed | locked
	Name        string `json:"name,omitempty"`        // the edition ("Wikipedia, 2026-06")
	File        string `json:"file,omitempty"`        // the file being imported or downloaded
	Articles    int64  `json:"articles,omitempty"`    // in the database (so far, while importing)
	Redirects   int64  `json:"redirects,omitempty"`   // in the database
	Next        int64  `json:"next,omitempty"`        // entries of the file read so far
	Total       int64  `json:"total,omitempty"`       // entries in the file
	Downloading int64  `json:"downloading,omitempty"` // bytes of the part file, when one is being fetched
	Left        int64  `json:"left,omitempty"`        // seconds the import has to go at its pace so far, while importing
	Error       string `json:"error,omitempty"`
	Answers     int    `json:"answers,omitempty"` // chat questions an article answered since synthd started
}

const SynthStatusKey = "synthd_status"

// SaveSynthStatus writes the status (synthd).
func SaveSynthStatus(db *poltergres.ReadWrite, s SynthStatus) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return db.Exec("INSERT INTO settings (key, value) VALUES ($1,$2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", SynthStatusKey, string(b))
}

// LoadSynthStatus reads it (Box Status); ok is false when synthd has not written one yet.
func LoadSynthStatus(c *poltergres.ReadWrite) (SynthStatus, bool) {
	var s SynthStatus
	rows, err := c.Query("SELECT value FROM settings WHERE key = $1", SynthStatusKey)
	if err != nil || len(rows.Vals) == 0 || len(rows.Vals[0]) == 0 || rows.Vals[0][0] == nil {
		return s, false
	}
	if json.Unmarshal([]byte(*rows.Vals[0][0]), &s) != nil {
		return s, false
	}
	return s, s.At > 0
}
