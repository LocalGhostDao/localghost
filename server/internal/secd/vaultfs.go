package secd

// FILES ROOT WRITES INTO THE VAULT.
//
// ghost.secd runs as root; the daemons run as their own user, and that user owns the mounted
// volume from its root down. Whenever secd puts something there for a daemon to read (a spooled
// photo, a note, the phone's feed bytes, a voice note, a health batch, an outgoing mail, a
// refreshed binary) it used to do so by path: os.MkdirAll, os.WriteFile, then os.Chown of the
// file, its directory and sometimes the directory above. Every one of those follows symbolic
// links, and the names belong to the daemons' user. A daemon that had been taken over (they
// parse photos, video and mail from outside) could swap its own inbox for a link to any
// directory on the machine and wait for the phone's next upload: root would create the file
// there and hand the directory to the daemons' user. The second audit showed it end to end
// (10 Nov 2026); run 1 had it as an open lead.
//
// So nothing here is done by path any more. The way down from the volume's root is walked one
// directory at a time, each opened as an os.Root beneath the one above. A name that is a link,
// or anything other than a directory, is refused; and what was opened is compared with what
// was looked at (os.SameFile), so a swap between the look and the open is refused too. Files
// are made in the last directory by bare name and never through a link (O_EXCL), owned through
// their descriptors. A directory is handed to the daemons' user only when this walk made it,
// and then through its descriptor.
//
// What that leaves a taken-over daemon able to do with root's writes: refuse them (the upload
// fails, loudly), which it could always do by filling the disk.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// vaultSplit splits an absolute path inside a mounted volume into the volume's root
// (<state>/mnt/slotN) and the path beneath it. A path that is not under a slot (a test's own
// directory) is confined to its own parent directory, which is the least that still holds.
func vaultSplit(stateDir, p string) (root, rel string) {
	base := filepath.Join(stateDir, "mnt")
	if r, err := filepath.Rel(base, p); err == nil && r != "." && !strings.HasPrefix(r, "..") {
		parts := strings.SplitN(r, string(filepath.Separator), 2)
		if len(parts) == 2 && strings.HasPrefix(parts[0], "slot") && parts[1] != "" {
			return filepath.Join(base, parts[0]), parts[1]
		}
	}
	return filepath.Dir(p), filepath.Base(p)
}

// descend opens the directory name beneath cur. It is made when missing and create is set, and
// only a directory this call made is handed to uid:gid (uid > 0).
func descend(cur *os.Root, name string, create bool, perm os.FileMode, uid, gid int) (*os.Root, error) {
	made := false
	li, err := cur.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) && create {
		switch merr := cur.Mkdir(name, perm); {
		case merr == nil:
			made = true
		case !errors.Is(merr, fs.ErrExist):
			return nil, merr
		}
		li, err = cur.Lstat(name)
	}
	if err != nil {
		return nil, err
	}
	if !li.IsDir() {
		return nil, fmt.Errorf("%s: a link or a file stands where a directory should", name)
	}
	next, err := cur.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	fi, err := next.Stat(".")
	if err != nil || !os.SameFile(li, fi) {
		next.Close()
		return nil, fmt.Errorf("%s: changed while it was being opened", name)
	}
	if made && uid > 0 {
		if d, derr := next.Open("."); derr == nil {
			_ = d.Chown(uid, gid) // by descriptor: the directory just made, nothing else
			d.Close()
		}
	}
	return next, nil
}

// dirUnder walks rel beneath the directory at root (itself trusted: <state>/mnt is root's).
// rel "" or "." is root itself.
func dirUnder(root, rel string, create bool, perm os.FileMode, uid, gid int) (*os.Root, error) {
	cur, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	rel = filepath.Clean(rel)
	if rel == "." || rel == "" {
		return cur, nil
	}
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		cur.Close()
		return nil, fmt.Errorf("%s: not beneath %s", rel, root)
	}
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		next, derr := descend(cur, name, create, perm, uid, gid)
		cur.Close()
		if derr != nil {
			return nil, derr
		}
		cur = next
	}
	return cur, nil
}

// createIn makes a fresh file by bare name in d. Whatever stood at that name goes first, and
// the open is exclusive, so no link is ever written through.
func createIn(d *os.Root, name string, perm os.FileMode) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, filepath.Separator) {
		return nil, fmt.Errorf("%q: not a file name", name)
	}
	if err := d.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return d.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_EXCL, perm)
}

// writeIn writes a whole file by bare name in d, owned by uid:gid when uid > 0.
func writeIn(d *os.Root, name string, data []byte, perm os.FileMode, uid, gid int) error {
	f, err := createIn(d, name, perm)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if werr == nil && uid > 0 {
		werr = f.Chown(uid, gid)
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = d.Remove(name)
	}
	return werr
}

// vaultDir opens the directory of an absolute path inside the volume; the caller closes it.
func vaultDir(stateDir, dir string, create bool, perm os.FileMode, uid, gid int) (*os.Root, error) {
	root, rel := vaultSplit(stateDir, dir)
	if create {
		if _, err := os.Stat(root); err != nil {
			// only ever true outside a mounted slot (tests): the confining directory itself
			if err := os.MkdirAll(root, perm); err != nil {
				return nil, err
			}
		}
	}
	return dirUnder(root, rel, create, perm, uid, gid)
}

// vaultMkdirAll makes dir and what is missing above it, beneath the volume; what it makes is
// the daemons' (uid > 0).
func vaultMkdirAll(stateDir, dir string, perm os.FileMode, uid, gid int) error {
	d, err := vaultDir(stateDir, dir, true, perm, uid, gid)
	if err != nil {
		return err
	}
	return d.Close()
}

// vaultCreate makes a fresh file for writing at path. The caller owns it through the
// descriptor (f.Chown) before closing, never by name.
func vaultCreate(stateDir, path string, perm os.FileMode) (*os.File, error) {
	d, err := vaultDir(stateDir, filepath.Dir(path), false, 0, 0, 0)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return createIn(d, filepath.Base(path), perm)
}

// vaultWrite writes a whole file at path, owned by uid:gid when uid > 0.
func vaultWrite(stateDir, path string, data []byte, perm os.FileMode, uid, gid int) error {
	d, err := vaultDir(stateDir, filepath.Dir(path), false, 0, 0, 0)
	if err != nil {
		return err
	}
	defer d.Close()
	return writeIn(d, filepath.Base(path), data, perm, uid, gid)
}

// vaultRename names a file into place; both names are in the same directory.
func vaultRename(stateDir, from, to string) error {
	if filepath.Dir(from) != filepath.Dir(to) {
		return fmt.Errorf("rename across directories: %s to %s", from, to)
	}
	d, err := vaultDir(stateDir, filepath.Dir(from), false, 0, 0, 0)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Rename(filepath.Base(from), filepath.Base(to))
}

func vaultRemove(stateDir, path string) {
	if d, err := vaultDir(stateDir, filepath.Dir(path), false, 0, 0, 0); err == nil {
		_ = d.Remove(filepath.Base(path))
		d.Close()
	}
}
