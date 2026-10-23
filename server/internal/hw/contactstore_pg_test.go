package hw

import (
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/apparedis"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// The contacts: an upload written, a second replacing it (a contact gone from the phone leaves
// the box), the search, a name for a number in any of its forms and for an address, an office
// number shared by two naming nobody, the birthdays on a date, the meta.
func TestContactStoreAgainstPostgres(t *testing.T) {
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
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_contacts")
	if err := admin.ExecSimple("CREATE DATABASE lgtest_contacts"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_contacts")
	if _, err := ConvergeSchema(db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	ns := &NotifStore{rw: map[int]*poltergres.ReadWrite{0: db}, rd: map[int]*apparedis.ReadWrite{}, pgSocketFor: func(int) string { return tmp + "/postgres" }}

	ana := Contact{ID: 1, Lookup: "l1", Name: "Ana Pop", Given: "Ana", Family: "Pop", Starred: true,
		Numbers:  []ContactNumber{{Number: "07700 900123", E164: "+447700900123", Label: "mobile"}},
		Emails:   []ContactEmail{{Address: "Ana@Example.org", Label: "home"}},
		Birthday: "1986-10-17"}
	paul := Contact{ID: 2, Lookup: "l2", Name: "Paul", Org: "Acme", Title: "CTO",
		Numbers: []ContactNumber{{Number: "+44 20 7946 0000", Label: "work"}, {Number: "07700 900456", Label: "mobile"}}, Birthday: "--10-17"}
	desk := Contact{ID: 3, Lookup: "l3", Name: "Acme reception", Numbers: []ContactNumber{{Number: "020 7946 0000"}}}
	gone := Contact{ID: 4, Lookup: "l4", Name: "Old Landlord", Numbers: []ContactNumber{{Number: "01234 567890"}}}

	if _, ok := ns.ContactsMeta(0); ok {
		t.Fatal("no upload yet, no meta")
	}
	if _, err := ns.ContactsReplace(0, "phone", []Contact{ana, paul, desk, gone}); err != nil {
		t.Fatal(err)
	}
	m, err := ns.ContactsReplace(0, "phone", []Contact{ana, paul, desk})
	if err != nil || m.Removed != 1 || m.Contacts != 3 || m.Birthday != 2 {
		t.Fatalf("second upload: %+v %v", m, err)
	}
	all, _ := ns.ContactSearch(0, nil, 0)
	if len(all) != 3 || all[0].Name != "Ana Pop" {
		t.Fatalf("everyone, starred first: %+v", all)
	}
	if len(all[0].Numbers) != 1 || all[0].Numbers[0].Label != "mobile" || all[0].Emails[0].Address != "Ana@Example.org" {
		t.Fatalf("round trip: %+v", all[0])
	}
	if r, _ := ns.ContactSearch(0, []string{"acme"}, 0); len(r) != 2 {
		t.Fatalf("by organisation: %+v", r)
	}
	if r, _ := ns.ContactSearch(0, []string{"7700 900456"}, 0); len(r) != 1 || r[0].Name != "Paul" {
		t.Fatalf("by number typed with a space: %+v", r)
	}
	if r, _ := ns.ContactSearch(0, []string{"landlord"}, 0); len(r) != 0 {
		t.Fatalf("the removed one is gone: %+v", r)
	}
	for _, n := range []string{"+44 7700 900123", "07700900123", "0044 7700 900 123"} {
		if c, ok := ns.ContactFor(0, n); !ok || c.Name != "Ana Pop" {
			t.Fatalf("%s: %+v %v", n, c, ok)
		}
	}
	if c, ok := ns.ContactFor(0, "ana@example.org"); !ok || c.Name != "Ana Pop" {
		t.Fatalf("by address, any case: %+v %v", c, ok)
	}
	if _, ok := ns.ContactFor(0, "+44 20 7946 0000"); ok {
		t.Fatal("a number two contacts share, neither starred, names nobody")
	}
	if _, ok := ns.ContactFor(0, "123"); ok {
		t.Fatal("too short to name anyone")
	}
	day := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	bs, err := ContactBirthdays(db, []time.Time{day})
	if err != nil || len(bs) != 2 {
		t.Fatalf("birthdays: %+v %v", bs, err)
	}
	if got := BirthdayLine(bs[0], day); got != "Ana Pop's birthday (40)" {
		t.Fatalf("%q", got)
	}
	if got := BirthdayLine(bs[1], day); got != "Paul's birthday" {
		t.Fatalf("%q", got)
	}
	var paulRow Contact
	for _, c := range all {
		if c.Name == "Paul" {
			paulRow = c
		}
	}
	if line := ContactLine(paulRow); !strings.Contains(line, "Paul · CTO, Acme · work +44 20 7946 0000") || !strings.Contains(line, "birthday 17 October") {
		t.Fatalf("%q", line)
	}
	if err := ns.ContactsForget(0); err != nil {
		t.Fatal(err)
	}
	if r, _ := ns.ContactSearch(0, nil, 0); len(r) != 0 {
		t.Fatal("forget drops every contact")
	}
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_contacts")
}

func TestContactBirthday(t *testing.T) {
	cases := map[string]string{
		"1986-03-14": "1986-03-14", "--03-14": "--03-14", "--0314": "--03-14", "19860314": "1986-03-14",
		"14/03/1986": "1986-03-14", "14.03.1986": "1986-03-14", "1986-03-14T00:00:00Z": "1986-03-14",
		"--02-29": "--02-29", "1985-02-29": "", "31/02/1990": "", "hello": "", "": "", "1700-01-01": "",
	}
	for in, want := range cases {
		if got := ContactBirthday(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	if PhoneTail("+44 (0) 7700-900123") != "700900123" || PhoneTail("112") != "112" {
		t.Fatal("tail")
	}
}
