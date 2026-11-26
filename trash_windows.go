package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const trashName = "Recycle Bin"

// canRecycle tells whether Windows would put a file in the Recycle Bin rather
// than delete it for good: the drive needs a Recycle Bin that is switched on
// and big enough for the file.
func canRecycle(path string, size int64) bool {
	vol := filepath.VolumeName(path)
	if len(vol) != 2 || vol[1] != ':' {
		return false // network path: no Recycle Bin
	}
	root, _ := windows.UTF16PtrFromString(vol + `\`)
	if t := windows.GetDriveType(root); t != windows.DRIVE_FIXED {
		return false
	}
	buf := make([]uint16, 64)
	if err := windows.GetVolumeNameForVolumeMountPoint(root, &buf[0], uint32(len(buf))); err != nil {
		return false
	}
	guid := windows.UTF16ToString(buf) // \\?\Volume{...}\
	if i, j := strings.Index(guid, "{"), strings.Index(guid, "}"); i >= 0 && j > i {
		guid = guid[i : j+1]
	} else {
		return false
	}
	maxMB := int64(-1)
	if k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Explorer\BitBucket\Volume\`+guid, registry.QUERY_VALUE); err == nil {
		if n, _, err := k.GetIntegerValue("NukeOnDelete"); err == nil && n != 0 {
			k.Close()
			return false
		}
		if n, _, err := k.GetIntegerValue("MaxCapacity"); err == nil {
			maxMB = int64(n)
		}
		k.Close()
	}
	if maxMB < 0 { // Windows' default: 10% of the first 40 GB and 5% of the rest
		var total uint64
		if windows.GetDiskFreeSpaceEx(root, nil, &total, nil) != nil {
			return false
		}
		t := int64(total) >> 20
		if t <= 40<<10 {
			maxMB = t / 10
		} else {
			maxMB = 4<<10 + (t-40<<10)/20
		}
	}
	return size <= maxMB<<20
}

var procSHFileOperation = windows.NewLazySystemDLL("shell32.dll").NewProc("SHFileOperationW")

type shFileOpStruct struct {
	hwnd    uintptr
	wFunc   uint32
	pFrom   *uint16
	pTo     *uint16
	fFlags  uint16
	aborted int32
	mapping uintptr
	title   *uint16
}

func shDelete(paths []string) bool {
	var buf []uint16
	for _, p := range paths {
		u, err := windows.UTF16FromString(p)
		if err != nil {
			return false
		}
		buf = append(buf, u...)
	}
	buf = append(buf, 0)
	op := shFileOpStruct{wFunc: 3 /* FO_DELETE */, pFrom: &buf[0],
		// ALLOWUNDO|NOCONFIRMATION|SILENT|NOERRORUI|WANTNUKEWARNING: if Windows
		// would still delete something for good, it asks first.
		fFlags: 0x40 | 0x10 | 0x04 | 0x400 | 0x4000}
	r, _, _ := procSHFileOperation.Call(uintptr(unsafe.Pointer(&op)))
	return r == 0 && op.aborted == 0
}

// recycle moves files to the Recycle Bin, a batch at a time, and reports how
// many are gone.
func recycle(paths []string) (int, error) {
	gone, failed := 0, 0
	for len(paths) > 0 {
		n := len(paths)
		if n > 200 {
			n = 200
		}
		batch := paths[:n]
		paths = paths[n:]
		if !shDelete(batch) {
			for _, p := range batch { // one at a time to find the ones that won't go
				if fileExists(p) {
					shDelete([]string{p})
				}
			}
		}
		for _, p := range batch {
			if fileExists(p) {
				failed++
			} else {
				gone++
			}
		}
	}
	if failed > 0 {
		return gone, fmt.Errorf("%d file(s) couldn't be removed. They may be open in another app", failed)
	}
	return gone, nil
}
