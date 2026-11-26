package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const cloudflaredFile = "cloudflared.exe"

func cloudflaredAsset() string { return "cloudflared-windows-amd64.exe" }

// pathKey makes a path comparable: Windows paths ignore case.
func pathKey(p string) string { return strings.ToLower(p) }

// fileTimes returns a file's modified and created times in Unix ms.
func fileTimes(info fs.FileInfo) (mtime, ctime int64) {
	mtime = info.ModTime().UnixMilli()
	if d, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		ctime = time.Unix(0, d.CreationTime.Nanoseconds()).UnixMilli()
	}
	return
}

// setFileTimes applies the sender's created/modified times to a received file.
func setFileTimes(path string, mtime, ctime int64) {
	if mtime <= 0 && ctime <= 0 {
		return
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	h, err := syscall.CreateFile(p, syscall.FILE_WRITE_ATTRIBUTES, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return
	}
	defer syscall.CloseHandle(h)
	ft := func(ms int64) *syscall.Filetime {
		if ms <= 0 {
			return nil
		}
		f := syscall.NsecToFiletime(ms * int64(time.Millisecond))
		return &f
	}
	syscall.SetFileTime(h, ft(ctime), nil, ft(mtime))
}

// setSparse: parallel connections write out of order, and without this NTFS
// zero-fills every gap first, doubling the disk writes.
func setSparse(fh *os.File) {
	var ret uint32
	windows.DeviceIoControl(windows.Handle(fh.Fd()), 0x000900c4 /* FSCTL_SET_SPARSE */, nil, 0, nil, 0, &ret, nil)
}

func freeBytes(dir string) (uint64, bool) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false
	}
	var free uint64
	if windows.GetDiskFreeSpaceEx(p, &free, nil, nil) != nil {
		return 0, false
	}
	return free, true
}

func idmPath() string {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		if k, err := registry.OpenKey(root, `Software\DownloadManager`, registry.QUERY_VALUE); err == nil {
			p, _, err := k.GetStringValue("ExePath")
			k.Close()
			if err == nil && fileExists(p) {
				return p
			}
		}
	}
	for _, p := range []string{
		os.Getenv("ProgramFiles(x86)") + `\Internet Download Manager\IDMan.exe`,
		os.Getenv("ProgramFiles") + `\Internet Download Manager\IDMan.exe`,
	} {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

// windowsNames: file names must be valid on Windows.
const windowsNames = true

func downloadsFolder() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Downloads")
}

func canRead(p string) bool { return true }
