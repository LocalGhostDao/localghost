package main

import (
	"fmt"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// WHAT GHOST.SHADOWD NAMED ABOUT THIS CHAT, said in the chat itself. The arbiter reads an
// answer ten minutes after it is written (internal/shadow), so what it finds arrives as a
// notification, away from the conversation it is about. "A one-liner at the top of the next
// conversation" is the charter's other surface: when the person next writes in that chat, the
// trail of the new answer opens with what was named about the earlier ones since they last
// wrote. Once: the next question after that finds nothing new.
//
// synthd only reads shadowd's table here. A detector that was muted when it spoke is left out,
// as it is from the notifications.
func shadowSaid(db *poltergres.ReadWrite, chatID int64) []string {
	if db == nil || chatID == 0 {
		return nil
	}
	rows, err := db.Query(`SELECT title FROM shadow_observations WHERE NOT muted AND subject LIKE $1
		AND ts * 1000 >= COALESCE((SELECT max(ts) FROM chat_messages WHERE chat_id = $2 AND role = 'user'), 0)
		ORDER BY id LIMIT 3`, fmt.Sprintf("chat:%d/%%", chatID), chatID)
	if err != nil {
		return nil // an older box's schema, or the table not there yet: nothing to say
	}
	var out []string
	for _, v := range rows.Vals {
		if len(v) == 1 && v[0] != nil && *v[0] != "" {
			out = append(out, "ghost.shadowd, about an earlier answer in this chat: "+*v[0]+" (its notification has both sides)")
		}
	}
	return out
}
