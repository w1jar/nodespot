package helper

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// openRegular opens path for reading without following a final symlink and
// refuses anything but a regular file.
func openRegular(path string) (*os.File, fs.FileInfo, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, nil, errors.New("not a regular file")
	}
	return f, fi, nil
}

// readRegular reads a regular file of at most max bytes.
func readRegular(path string, max int64) ([]byte, fs.FileInfo, error) {
	f, fi, err := openRegular(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	if fi.Size() > max {
		return nil, nil, errors.New("file too large")
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(b)) > max {
		return nil, nil, errors.New("file too large")
	}
	return b, fi, nil
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// lookupName returns the user or group name of a numeric id, or the number.
func lookupName(id int, group bool) string {
	s := strconv.Itoa(id)
	if group {
		if g, err := user.LookupGroupId(s); err == nil {
			return g.Name
		}
	} else if u, err := user.LookupId(s); err == nil {
		return u.Username
	}
	return s
}

// lookupIDs maps a user and a group name to ids; a name that does not exist
// maps to fallback (root when that does not exist either).
func lookupIDs(owner, group, fallback string) (uid, gid int) {
	for _, n := range []string{owner, fallback} {
		if u, err := user.Lookup(n); err == nil {
			if v, err := strconv.Atoi(u.Uid); err == nil {
				uid = v
				break
			}
		}
	}
	for _, n := range []string{group, fallback} {
		if g, err := user.LookupGroup(n); err == nil {
			if v, err := strconv.Atoi(g.Gid); err == nil {
				gid = v
				break
			}
		}
	}
	return uid, gid
}

// atomicWrite replaces abs (in the real directory parent) with data: a temp
// file in the same directory (created exclusively, no symlink following),
// owner and mode set on the open file, fsync, rename.
func atomicWrite(abs, parent string, data []byte, uid, gid int, mode fs.FileMode) error {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmp := filepath.Join(parent, ".nodespot-tmp-"+hex.EncodeToString(rnd[:]))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err := f.Chown(uid, gid); err != nil && os.Geteuid() == 0 {
		return err // the real helper is root; unprivileged test runs cannot chown
	}
	if err := f.Chmod(mode.Perm()); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if rp, err := filepath.EvalSymlinks(parent); err != nil || rp != parent {
		return errors.New("directory changed during write")
	}
	if fi, err := os.Lstat(abs); err == nil && !fi.Mode().IsRegular() {
		return errors.New("destination is not a regular file")
	}
	if err := os.Rename(tmp, abs); err != nil {
		return err
	}
	ok = true
	if d, err := os.Open(parent); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// writeFileAt writes data to path through atomicWrite, creating the
// directory (0755) when it is missing.
func writeFileAt(path string, data []byte, uid, gid int, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(parent, filepath.Base(path)), parent, data, uid, gid, mode)
}
