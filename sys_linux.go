package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

// windowsNames: on Linux, file names only need to avoid "/" and NUL.
const windowsNames = false

const cloudflaredFile = "cloudflared"

func cloudflaredAsset() string {
	switch runtime.GOARCH {
	case "arm64":
		return "cloudflared-linux-arm64"
	case "arm":
		return "cloudflared-linux-arm"
	case "386":
		return "cloudflared-linux-386"
	}
	return "cloudflared-linux-amd64"
}

// pathKey makes a path comparable: Linux paths are case-sensitive.
func pathKey(p string) string { return p }

// fileTimes returns a file's modified time in Unix ms. Linux has no settable
// creation time, so none is sent.
func fileTimes(info fs.FileInfo) (mtime, ctime int64) {
	return info.ModTime().UnixMilli(), 0
}

// setFileTimes applies the sender's modified time (Linux can't set a file's
// creation time).
func setFileTimes(path string, mtime, ctime int64) {
	if mtime <= 0 {
		return
	}
	os.Chtimes(path, time.Time{}, time.UnixMilli(mtime))
}

// setSparse: files on Linux are sparse already when grown with Truncate.
func setSparse(fh *os.File) {}

func freeBytes(dir string) (uint64, bool) {
	var st syscall.Statfs_t
	if syscall.Statfs(dir, &st) != nil {
		return 0, false
	}
	return st.Bavail * uint64(st.Bsize), true
}

// idmPath: IDM is a Windows program.
func idmPath() string { return "" }

// downloadsFolder is the user's Downloads folder (XDG_DOWNLOAD_DIR when set,
// so it follows the desktop's language, e.g. ~/Téléchargements).
func downloadsFolder() string {
	home, _ := os.UserHomeDir()
	cfgDir, err := os.UserConfigDir()
	if err == nil {
		if f, err := os.Open(filepath.Join(cfgDir, "user-dirs.dirs")); err == nil {
			defer f.Close()
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				if !strings.HasPrefix(line, "XDG_DOWNLOAD_DIR=") {
					continue
				}
				v := strings.Trim(strings.TrimPrefix(line, "XDG_DOWNLOAD_DIR="), `"`)
				v = strings.Replace(v, "$HOME", home, 1)
				if filepath.IsAbs(v) && filepath.Clean(v) != filepath.Clean(home) {
					return v
				}
			}
		}
	}
	return filepath.Join(home, "Downloads")
}

// hideCmd: helper programs (cloudflared) stop when ShareMile stops, even if
// it is killed.
func hideCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}

// startDetached runs a program without waiting for it (and reaps it later).
func startDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// openPath opens a file or folder with the desktop's default app.
func openPath(p string) error {
	for _, c := range [][]string{{"xdg-open", p}, {"gio", "open", p}} {
		if _, err := exec.LookPath(c[0]); err == nil {
			return startDetached(c[0], c[1:]...)
		}
	}
	return errors.New("couldn't find xdg-open to open " + p)
}

func openURL(u string) error {
	if err := openPath(u); err != nil {
		return errors.New("couldn't open a web browser. Open this address yourself: " + u)
	}
	return nil
}

// revealPath shows a file selected in the file manager (Files, Dolphin,
// Thunar, Nemo… all answer org.freedesktop.FileManager1), or opens its folder.
func revealPath(p string) error {
	if conn, err := dbus.SessionBus(); err == nil {
		u := "file://" + (&urlPath{p}).String()
		obj := conn.Object("org.freedesktop.FileManager1", "/org/freedesktop/FileManager1")
		call := obj.Call("org.freedesktop.FileManager1.ShowItems", 0, []string{u}, "")
		if call.Err == nil {
			return nil
		}
	}
	st, err := os.Stat(p)
	if err == nil && st.IsDir() {
		return openPath(p)
	}
	return openPath(filepath.Dir(p))
}

type urlPath struct{ p string }

func (u *urlPath) String() string {
	var b strings.Builder
	for _, c := range []byte(u.p) {
		if c == '/' || c == '-' || c == '_' || c == '.' || c == '~' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			b.WriteByte(c)
		} else {
			b.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{c})))
		}
	}
	return b.String()
}

// ---- one ShareMile per data folder ------------------------------------------

var instanceLock *os.File

func instanceName() string {
	h := sha256.Sum256([]byte(appDataDir()))
	return "@sharemile-" + hex.EncodeToString(h[:8]) // abstract socket: no file to clean up
}

// singleInstance makes sure one ShareMile runs per data folder. A second copy
// asks the first one to show its window and returns false.
func singleInstance(updated bool) bool {
	lockPath := filepath.Join(appDataDir(), ".instance.lock")
	for tries := 0; ; tries++ {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
		if err == nil {
			if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
				instanceLock = f // held until ShareMile exits
				go listenInstance()
				return true
			}
			f.Close()
		}
		// Right after a self-update the old copy is still shutting down: wait for it.
		if !updated || tries >= 60 {
			if c, err := net.DialTimeout("unix", instanceName(), 2*time.Second); err == nil {
				c.Write([]byte("show\n"))
				c.Close()
			}
			if err != nil {
				os.Stderr.WriteString("ShareMile is already running.\n")
			}
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func listenInstance() {
	ln, err := net.Listen("unix", instanceName())
	if err != nil {
		return
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			c.SetReadDeadline(time.Now().Add(5 * time.Second))
			line, _ := bufio.NewReader(c).ReadString('\n')
			if strings.TrimSpace(line) == "show" {
				showMainWindow()
			}
		}(c)
	}
}

// canRead tells whether this user may read a file (root-owned files in a
// shared folder would otherwise be listed and then fail to download).
func canRead(p string) bool { return syscall.Access(p, 4 /* R_OK */) == nil }
