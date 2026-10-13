package hw

import (
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/apparedis"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// The mail rows: an inbound message stored once however many times the pull sees it, listed by
// folder, searched, read with its attachments, threaded with the reply, and the counts.
func TestMailStoreAgainstPostgres(t *testing.T) {
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
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_mail")
	if err := admin.ExecSimple("CREATE DATABASE lgtest_mail"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_mail")
	if _, err := ConvergeSchema(db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	ns := &NotifStore{rw: map[int]*poltergres.ReadWrite{0: db}, rd: map[int]*apparedis.ReadWrite{}, pgSocketFor: func(int) string { return tmp + "/postgres" }}
	in := MailRow{Direction: "in", State: "new", MessageID: "m1@example.org", ThreadKey: "m1@example.org", FromAddr: "paul@example.org", FromName: "Paul",
		To: []string{"agent@lgs.example"}, Subject: "the flight times", SentAt: 1760000000, ReceivedAt: 1760000100, BodyKind: "text", RawSize: 1234, Spam: "low", RelayName: "1-abc.seal"}
	id, err := ns.MailInsert(0, in, "Thursday works.\nsecond line", []byte(`{"spam":"low"}`), "/vol/noted/mail/in/1-abc.seal")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := ns.MailInsert(0, in, "x", nil, "")
	if err != nil || id2 != id {
		t.Fatalf("a second insert of the same relay name must be the same row: %d %d %v", id, id2, err)
	}
	if have, _ := ns.MailHave(0, "in", "1-abc.seal"); !have {
		t.Fatal("MailHave")
	}
	if _, err := ns.MailAttachAdd(0, MailAttach{MessageID: id, Name: "tickets.pdf", Mime: "application/pdf", Bytes: 9, Kept: true, Path: "/vol/att/1-tickets.pdf", SHA256: "abc"}); err != nil {
		t.Fatal(err)
	}
	out := MailRow{Direction: "out", State: "queued", MessageID: "r1@lgs.example", InReplyTo: "m1@example.org", ThreadKey: "m1@example.org", FromAddr: "agent@lgs.example",
		To: []string{"paul@example.org"}, Subject: "Re: the flight times", SentAt: 1760000200, ReceivedAt: 1760000200, BodyKind: "text", RelayName: "out-1"}
	oid, err := ns.MailInsert(0, out, "Thursday it is.", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := ns.MailList(0, "inbox", "", 50, 0)
	if err != nil || len(inbox) != 1 || inbox[0].ID != id || inbox[0].Preview != "Thursday works." || inbox[0].AttachN != 1 || inbox[0].Body != "" {
		t.Fatalf("inbox %+v %v", inbox, err)
	}
	sent, _ := ns.MailList(0, "sent", "", 50, 0)
	if len(sent) != 1 || sent[0].ID != oid || sent[0].State != "queued" {
		t.Fatalf("sent %+v", sent)
	}
	found, _ := ns.MailList(0, "all", "FLIGHT", 50, 0)
	if len(found) != 2 {
		t.Fatalf("search %d", len(found))
	}
	none, _ := ns.MailList(0, "all", "zzz", 50, 0)
	if len(none) != 0 {
		t.Fatal("search miss")
	}
	got, err := ns.MailGet(0, id)
	if err != nil || got == nil || got.Body != "Thursday works.\nsecond line" || string(got.Verdicts) != `{"spam":"low"}` || len(got.Attach) != 1 || got.Attach[0].Path != "/vol/att/1-tickets.pdf" {
		t.Fatalf("get %+v %v", got, err)
	}
	thread, _ := ns.MailThread(0, "m1@example.org")
	if len(thread) != 2 || thread[0].Direction != "in" || thread[1].Direction != "out" {
		t.Fatalf("thread %+v", thread)
	}
	cnt, seen, _ := ns.MailSenderHistory(0, "paul@example.org")
	if cnt != 1 || !seen {
		t.Fatalf("history %d %v", cnt, seen)
	}
	if n, _ := ns.MailRecentFrom(0, "paul@example.org", 1760000000); n != 1 {
		t.Fatal("recent")
	}
	if err := ns.MailSetState(0, oid, "sent", ""); err != nil {
		t.Fatal(err)
	}
	if err := ns.MailSetRelayName(0, oid, "2-def.seal"); err != nil {
		t.Fatal(err)
	}
	if q, _ := ns.MailOutQueued(0); len(q) != 0 {
		t.Fatal("still queued")
	}
	i, u, o, qd, _ := ns.MailCounts(0)
	if i != 1 || u != 1 || o != 1 || qd != 0 {
		t.Fatalf("counts %d %d %d %d", i, u, o, qd)
	}
	ns.MailSetState(0, id, "archived", "")
	if l, _ := ns.MailList(0, "inbox", "", 50, 0); len(l) != 0 {
		t.Fatal("archived still in inbox")
	}
	if l, _ := ns.MailList(0, "archive", "", 50, 0); len(l) != 1 {
		t.Fatal("archive")
	}
	atts, seal, err := ns.MailDelete(0, id)
	if err != nil || len(atts) != 1 || seal != "/vol/noted/mail/in/1-abc.seal" {
		t.Fatalf("delete %v %q %v", atts, seal, err)
	}
	if g, _ := ns.MailGet(0, id); g != nil {
		t.Fatal("not deleted")
	}
}
