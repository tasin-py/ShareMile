package main

import (
	"errors"
	"os/exec"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ole32            = windows.NewLazySystemDLL("ole32.dll")
	procCoInit       = ole32.NewProc("CoInitializeEx")
	procCoUninit     = ole32.NewProc("CoUninitialize")
	procCoCreate     = ole32.NewProc("CoCreateInstance")
	procCoTaskFree   = ole32.NewProc("CoTaskMemFree")
	user32           = windows.NewLazySystemDLL("user32.dll")
	procMessageBox   = user32.NewProc("MessageBoxW")
	procSetWinLong   = user32.NewProc("SetWindowLongPtrW")
	procCallWinProc  = user32.NewProc("CallWindowProcW")
	procSetFg        = user32.NewProc("SetForegroundWindow")
	procShowWindow   = user32.NewProc("ShowWindow")
	procFindWindow   = user32.NewProc("FindWindowW")
	procIsIconic     = user32.NewProc("IsIconic")
	shell32          = windows.NewLazySystemDLL("shell32.dll")
	procShellExecute = shell32.NewProc("ShellExecuteW")
)

var (
	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4dde, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog  = windows.GUID{Data1: 0xd57c7288, Data2: 0xd4ad, Data3: 0x4768, Data4: [8]byte{0xbe, 0x02, 0x9d, 0x96, 0x95, 0x32, 0xd9, 0x60}}
)

var mainHwnd uintptr

func comCall(obj uintptr, idx int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(idx)*unsafe.Sizeof(uintptr(0))))
	all := append([]uintptr{obj}, args...)
	r, _, _ := syscall.SyscallN(fn, all...)
	return r
}

// pickPaths opens the native Windows picker. folders=true picks folders.
// Runs on a dedicated locked OS thread with its own STA apartment.
func pickPaths(folders bool, title string) ([]string, error) {
	type res struct {
		paths []string
		err   error
	}
	ch := make(chan res, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		procCoInit.Call(0, 2) // COINIT_APARTMENTTHREADED
		defer procCoUninit.Call()
		var dlg uintptr
		hr, _, _ := procCoCreate.Call(uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, 1|4, uintptr(unsafe.Pointer(&iidIFileOpenDialog)), uintptr(unsafe.Pointer(&dlg)))
		if hr != 0 || dlg == 0 {
			ch <- res{nil, errors.New("cannot open file dialog")}
			return
		}
		defer comCall(dlg, 2)
		opts := uintptr(0x40 | 0x200 | 0x800 | 0x1000) // FORCEFILESYSTEM|ALLOWMULTISELECT|PATHMUSTEXIST|FILEMUSTEXIST
		if folders {
			opts |= 0x20 // PICKFOLDERS
		}
		comCall(dlg, 9, opts)
		t, _ := windows.UTF16PtrFromString(title)
		comCall(dlg, 17, uintptr(unsafe.Pointer(t)))
		if r := comCall(dlg, 3, mainHwnd); r != 0 {
			ch <- res{nil, nil} // cancelled
			return
		}
		var arr uintptr
		if comCall(dlg, 27, uintptr(unsafe.Pointer(&arr))) != 0 || arr == 0 {
			ch <- res{nil, errors.New("no selection")}
			return
		}
		defer comCall(arr, 2)
		var n uint32
		comCall(arr, 7, uintptr(unsafe.Pointer(&n)))
		var out []string
		for i := uint32(0); i < n; i++ {
			var item uintptr
			if comCall(arr, 8, uintptr(i), uintptr(unsafe.Pointer(&item))) != 0 || item == 0 {
				continue
			}
			var p *uint16
			if comCall(item, 5, 0x80058000, uintptr(unsafe.Pointer(&p))) == 0 && p != nil {
				out = append(out, windows.UTF16PtrToString(p))
				procCoTaskFree.Call(uintptr(unsafe.Pointer(p)))
			}
			comCall(item, 2)
		}
		ch <- res{out, nil}
	}()
	r := <-ch
	return r.paths, r.err
}

func messageBox(title, text string, flags uintptr) int {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	r, _, _ := procMessageBox.Call(mainHwnd, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), flags)
	return int(r)
}

func shellExec(verb, file, args string) error {
	v, _ := windows.UTF16PtrFromString(verb)
	f, _ := windows.UTF16PtrFromString(file)
	var a *uint16
	if args != "" {
		a, _ = windows.UTF16PtrFromString(args)
	}
	r, _, _ := procShellExecute.Call(mainHwnd, uintptr(unsafe.Pointer(v)), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(a)), 0, 1)
	if r <= 32 {
		return errors.New("action was cancelled or failed")
	}
	return nil
}

func openURL(u string) error { return shellExec("open", u, "") }

func revealPath(p string) error {
	return exec.Command("explorer.exe", "/select,", p).Start()
}

// --- window close interception -------------------------------------------

var oldWndProc uintptr

func hookClose(hwnd uintptr, onClose func() bool) {
	showMsg := showRequestMsg()
	cb := syscall.NewCallback(func(h, msg, wp, lp uintptr) uintptr {
		if msg == 0x0010 && !onClose() { // WM_CLOSE
			return 0
		}
		switch {
		case msg == wmTray:
			onTrayMessage(lp)
			return 0
		case msg == showMsg && showMsg != 0:
			showMainWindow()
			return 0
		case msg == taskbarMsg && taskbarMsg != 0: // Explorer restarted
			trayMu.Lock()
			trayShown = false
			trayMu.Unlock()
			syncTray()
		}
		r, _, _ := procCallWinProc.Call(oldWndProc, h, msg, wp, lp)
		return r
	})
	gwlpWndProc := -4
	oldWndProc, _, _ = procSetWinLong.Call(hwnd, uintptr(gwlpWndProc), cb)
}

func showRequestMsg() uintptr {
	p, _ := windows.UTF16PtrFromString("ShareMile-ShowWindow")
	m, _, _ := procRegisterWinMsg.Call(uintptr(unsafe.Pointer(p)))
	return m
}

// focusExisting brings an already running ShareMile window to the front.
func focusExisting() {
	t, _ := windows.UTF16PtrFromString(appName)
	h, _, _ := procFindWindow.Call(0, uintptr(unsafe.Pointer(t)))
	if h != 0 {
		// It may be hidden in the tray: ask it to show itself.
		if m := showRequestMsg(); m != 0 {
			procPostMessage.Call(h, m, 0, 0)
		}
		if r, _, _ := procIsIconic.Call(h); r != 0 {
			procShowWindow.Call(h, 9) // SW_RESTORE
		}
		procSetFg.Call(h)
	}
}
