package mailseal

import (
	"bytes"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	priv, pub, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 1, Chunk - 1, Chunk, Chunk + 1, 3*Chunk + 17} {
		plain := bytes.Repeat([]byte("mail "), n/5+1)[:n]
		sealed, err := SealBytes(plain, pub)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Open(bytes.NewReader(sealed), priv, 1<<24)
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("n=%d: round trip differs", n)
		}
	}
}

func TestOpenRefusesTheWrongKeyTruncationAndTampering(t *testing.T) {
	priv, pub, _ := GenerateKey()
	other, _, _ := GenerateKey()
	sealed, _ := SealBytes([]byte("hello"), pub)
	if _, err := Open(bytes.NewReader(sealed), other, 1<<20); err == nil {
		t.Fatal("opened with the wrong key")
	}
	if _, err := Open(bytes.NewReader(sealed[:len(sealed)-3]), priv, 1<<20); err == nil {
		t.Fatal("opened a truncated stream")
	}
	bad := append([]byte{}, sealed...)
	bad[len(bad)-1] ^= 1
	if _, err := Open(bytes.NewReader(bad), priv, 1<<20); err == nil {
		t.Fatal("opened a tampered stream")
	}
	if _, err := Open(bytes.NewReader([]byte("GHSTBK1"+"x")), priv, 1<<20); err != ErrNotSealed {
		t.Fatalf("a backup is not a mail: %v", err)
	}
	big, _ := SealBytes(bytes.Repeat([]byte("x"), 1000), pub)
	if _, err := Open(bytes.NewReader(big), priv, 100); err == nil {
		t.Fatal("opened past the cap")
	}
}

func TestKeyFiles(t *testing.T) {
	priv, pub, _ := GenerateKey()
	p := t.TempDir() + "/k"
	if err := WriteKeyFile(p, priv); err != nil {
		t.Fatal(err)
	}
	got, err := ReadKeyFile(p)
	if err != nil || !bytes.Equal(got, priv) {
		t.Fatal("key file round trip")
	}
	pub2, _ := PublicOf(priv)
	if !bytes.Equal(pub, pub2) {
		t.Fatal("public half differs")
	}
}
