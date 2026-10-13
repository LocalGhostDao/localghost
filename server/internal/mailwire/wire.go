// Package mailwire is what the relay and the box agree on: the note the relay seals beside each
// message, the queue listing, the names, the outbound envelope. Both binaries import it, nothing
// else does.
package mailwire

import (
	"encoding/json"
	"regexp"
)

// Note is what only the relay could know about a message and the box needs to judge it. It is
// sealed beside the message, never stored in the clear.
type Note struct {
	Name     string   `json:"name"`     // the queue name of the message file
	Box      string   `json:"box"`      // the box id it was sealed for
	IP       string   `json:"ip"`       // the connecting address
	Port     int      `json:"port"`     // its port
	Helo     string   `json:"helo"`     // the EHLO/HELO name as given
	MailFrom string   `json:"mailFrom"` // the envelope sender ("" for a bounce)
	RcptTo   []string `json:"rcptTo"`   // the envelope recipients at this box
	TLS      string   `json:"tls"`      // "TLS1.3 TLS_AES_128_GCM_SHA256" or "" (never, since TLS is required)
	Received int64    `json:"received"` // unix seconds the DATA finished
	Size     int64    `json:"size"`     // message bytes as received
	// PTR is the reverse name of IP when it resolved and resolved back to IP ("" otherwise);
	// PTRState is "ok", "none" (no PTR), "mismatch" (a PTR that does not point back), "error".
	PTR      string `json:"ptr,omitempty"`
	PTRState string `json:"ptrState"`
	Relay    string `json:"relay"` // the relay's own name, for the Received line and the row
}

// Entry is one queued message in the listing.
type Entry struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Note bool   `json:"note"` // whether a note file sits beside it
}

// Outbound is what the box posts to send: the signed RFC 5322 message, sealed to the relay's key,
// with the envelope in the clear fields the relay needs to deliver it.
type Outbound struct {
	From string   `json:"from"` // envelope sender, must be on an enrolled domain of the box
	To   []string `json:"to"`   // envelope recipients
	// Sealed is the message sealed to the relay's public key, base64 in JSON.
	Sealed []byte `json:"sealed"`
}

// OutStatus is the relay's account of one outbound message.
type OutStatus struct {
	Name     string `json:"name"`
	State    string `json:"state"` // queued, sent, failed, bounced
	Tries    int    `json:"tries"`
	LastErr  string `json:"lastErr,omitempty"`
	Updated  int64  `json:"updated"`
	Accepted int64  `json:"accepted"`
}

// NameRe is a queue file name as the relay makes them: unix nanoseconds, a dash, 16 hex chars,
// ".seal", optionally ".note" before it. Anything else is refused by the API before it reaches
// a path.
var NameRe = regexp.MustCompile(`^[0-9]{10,20}-[0-9a-f]{16}(\.note)?\.seal$`)

// BoxRe is a box id.
var BoxRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,31}$`)

// NoteName is the note file beside a message file.
func NoteName(msgName string) string {
	return msgName[:len(msgName)-len(".seal")] + ".note.seal"
}

// EncodeNote is the note as JSON bytes.
func EncodeNote(n Note) []byte {
	b, _ := json.Marshal(n)
	return b
}
