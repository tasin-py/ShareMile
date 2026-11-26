package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The Trash on Linux follows the freedesktop.org Trash spec, so files show up
// in Files, Dolphin, Thunar, Nemo… and can be restored from there. A file goes
// to the Trash of its own drive (moving it is a rename, so size doesn't
// matter). If no Trash can be used there, it is left alone and the user is
// asked before it's deleted for good.

const trashName = "Trash"

func devOf(p string) (uint64, bool) {
	var st syscall.Stat_t
	if syscall.Lstat(p, &st) != nil {
		return 0, false
	}
	return uint64(st.Dev), true
}

func homeTrash() string {
	d := os.Getenv("XDG_DATA_HOME")
	if d == "" || !filepath.IsAbs(d) {
		home, _ := os.UserHomeDir()
		d = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(d, "Trash")
}

// topDir finds the mount point that holds p (the highest folder on the same device).
func topDir(p string) string {
	dev, ok := devOf(p)
	if !ok {
		return ""
	}
	d := filepath.Dir(p)
	for {
		up := filepath.Dir(d)
		if up == d {
			return d
		}
		if ud, ok := devOf(up); !ok || ud != dev {
			return d
		}
		d = up
	}
}

// trashDirFor returns the Trash folder to use for p, creating it if needed,
// and the folder relative paths in .trashinfo are written against ("" means
// absolute paths).
func trashDirFor(p string) (trash, base string, ok bool) {
	dev, ok := devOf(p)
	if !ok {
		return "", "", false
	}
	ht := homeTrash()
	// The home Trash, if the file lives on the same drive as it.
	hd := ht
	for {
		if d, ok := devOf(hd); ok {
			if d == dev {
				if os.MkdirAll(filepath.Join(ht, "files"), 0o700) == nil && os.MkdirAll(filepath.Join(ht, "info"), 0o700) == nil {
					return ht, "", true
				}
			}
			break
		}
		up := filepath.Dir(hd)
		if up == hd {
			break
		}
		hd = up
	}
	top := topDir(p)
	if top == "" {
		return "", "", false
	}
	uid := strconv.Itoa(os.Getuid())
	// $topdir/.Trash/$uid: only when .Trash is a real folder with the sticky bit.
	if st, err := os.Lstat(filepath.Join(top, ".Trash")); err == nil && st.IsDir() && st.Mode()&os.ModeSticky != 0 && st.Mode()&os.ModeSymlink == 0 {
		t := filepath.Join(top, ".Trash", uid)
		if os.MkdirAll(filepath.Join(t, "files"), 0o700) == nil && os.MkdirAll(filepath.Join(t, "info"), 0o700) == nil {
			return t, top, true
		}
	}
	// $topdir/.Trash-$uid
	t := filepath.Join(top, ".Trash-"+uid)
	if st, err := os.Lstat(t); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return "", "", false
	}
	if os.MkdirAll(filepath.Join(t, "files"), 0o700) == nil && os.MkdirAll(filepath.Join(t, "info"), 0o700) == nil {
		return t, top, true
	}
	return "", "", false
}

// canRecycle tells whether the file can go to a Trash (rather than be
// deleted for good).
func canRecycle(path string, size int64) bool {
	_, _, ok := trashDirFor(path)
	return ok
}

// trashOne moves one file to the Trash with its .trashinfo record.
func trashOne(p string) error {
	abs, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	trash, base, ok := trashDirFor(abs)
	if !ok {
		return fmt.Errorf("no Trash for %s", abs)
	}
	orig := abs
	if base != "" {
		if r, err := filepath.Rel(base, abs); err == nil && !strings.HasPrefix(r, "..") {
			orig = r
		}
	}
	name := filepath.Base(abs)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; i < 10000; i++ {
		n := name
		if i > 1 {
			n = stem + "." + strconv.Itoa(i) + ext
		}
		info := filepath.Join(trash, "info", n+".trashinfo")
		f, err := os.OpenFile(info, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // claims the name
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		u := (&url.URL{Path: orig}).EscapedPath()
		if base != "" {
			u = strings.TrimPrefix(u, "/")
		}
		fmt.Fprintf(f, "[Trash Info]\nPath=%s\nDeletionDate=%s\n", u, time.Now().Format("2006-01-02T15:04:05"))
		f.Close()
		if err := os.Rename(abs, filepath.Join(trash, "files", n)); err != nil {
			os.Remove(info)
			return err
		}
		return nil
	}
	return fmt.Errorf("the Trash is full of files named %s", name)
}

// recycle moves files to the Trash and reports how many are gone.
func recycle(paths []string) (int, error) {
	gone, failed := 0, 0
	for _, p := range paths {
		if !fileExists(p) {
			continue
		}
		if trashOne(p) == nil {
			gone++
		} else {
			failed++
		}
	}
	if failed > 0 {
		return gone, fmt.Errorf("%d file(s) couldn't be moved to the Trash. Check that you may change that folder", failed)
	}
	return gone, nil
}
