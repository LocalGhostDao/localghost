package secd

import (
	"strings"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
)

// An upload is checked and trimmed: a contact with no id and no lookup key, or with nothing to
// it, is dropped; numbers without digits and addresses without an @ go; a duplicate goes;
// the birthday is normalised; a name is made from the given and family names when missing.
func TestCleanContacts(t *testing.T) {
	u := contactsUpload{Contacts: []hw.Contact{
		{ID: 1, Lookup: "a", Given: "Ana", Family: "Pop", Numbers: []hw.ContactNumber{{Number: "07700 900123"}, {Number: "n/a"}},
			Emails: []hw.ContactEmail{{Address: "not an address"}, {Address: "ana@example.org"}}, Birthday: "14/03/1986"},
		{ID: 1, Lookup: "a", Name: "Ana again"},
		{Name: "no identity"},
		{ID: 5},
		{ID: 6, Name: strings.Repeat("x", 300)},
	}}
	if _, ok := cleanContacts(&u); !ok {
		t.Fatal("a good upload")
	}
	if u.Device != "phone" || len(u.Contacts) != 2 {
		t.Fatalf("kept: %+v", u.Contacts)
	}
	a := u.Contacts[0]
	if a.Name != "Ana Pop" || len(a.Numbers) != 1 || len(a.Emails) != 1 || a.Birthday != "1986-03-14" {
		t.Fatalf("trimmed: %+v", a)
	}
	if len(u.Contacts[1].Name) != 200 {
		t.Fatalf("name clipped: %d", len(u.Contacts[1].Name))
	}
}
