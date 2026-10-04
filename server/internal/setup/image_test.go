package setup

import "testing"

func TestImageOrDisk(t *testing.T) {
	for _, d := range []string{"/dev/nvme1n1", "/dev/disk/by-id/nvme-eui.1", "/dev/sdb"} {
		if IsImage(d) {
			t.Fatalf("%s taken for a file", d)
		}
	}
	for _, f := range []string{"/mnt/data/localghost.img", "/srv/ghost/volume", "/home/x/../mnt/d/v.img"} {
		if !IsImage(f) {
			t.Fatalf("%s taken for a disk", f)
		}
	}
	if IsImage("") {
		t.Fatal("nothing is not a file")
	}
}

func TestSizes(t *testing.T) {
	for in, want := range map[string]int64{
		"500G": 500 << 30, "500g": 500 << 30, "1T": 1 << 40, "1.5T": 1536 << 30, "750000M": 750000 << 20,
		"500GiB": 500 << 30, "500GB": 500 << 30, "21474836480": 20 << 30,
	} {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Fatalf("%s: %d %v (want %d)", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "10G", "abc", "-5T", "0", "500 bananas"} {
		if _, err := ParseSize(bad); err == nil {
			t.Fatalf("%q taken as a size", bad)
		}
	}
	for n, want := range map[int64]string{500 << 30: "500 GB", 1536 << 30: "1.5 TB", 1 << 40: "1 TB", 300 << 20: "300 MB"} {
		if got := SizeText(n); got != want {
			t.Fatalf("%d: %s (want %s)", n, got, want)
		}
	}
}
