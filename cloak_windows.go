package main

import (
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The window is created hidden from view (DWM "cloaked") and is shown only
// once the page has drawn itself in the saved theme. Without this, launching
// shows a blank white window with the plain title bar, then a dark flash,
// then the app. A fallback shows it anyway after a few seconds.

var (
	procSetHook      = user32.NewProc("SetWindowsHookExW")
	procUnhook       = user32.NewProc("UnhookWindowsHookEx")
	procNextHook     = user32.NewProc("CallNextHookEx")
	procGetClassName = user32.NewProc("GetClassNameW")
	procThreadID     = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetCurrentThreadId")
	cloakHook        uintptr
	cloakedHwnd      uintptr
	uncloakOnce      sync.Once
	cloakHookProc    = syscall.NewCallback(cloakHookFn)
	dwmaCloak        = uintptr(13) // DWMWA_CLOAK
	whCallWndRet     = uintptr(12) // WH_CALLWNDPROCRET
	cloakFallbackT   = 4 * time.Second
)

// cloakHookFn cloaks the app window as soon as its WM_CREATE has returned:
// the window exists by then, and Windows has not shown it yet.
func cloakHookFn(code, wp, lp uintptr) uintptr {
	if int32(code) >= 0 && cloakedHwnd == 0 {
		// lp is a CWPRETSTRUCT: lResult, lParam, wParam, message, hwnd.
		msg := *(*uint32)(unsafe.Pointer(lp + 3*unsafe.Sizeof(uintptr(0))))
		hwnd := *(*uintptr)(unsafe.Pointer(lp + 4*unsafe.Sizeof(uintptr(0))))
		if msg == 0x0001 { // WM_CREATE
			var cls [16]uint16
			n, _, _ := procGetClassName.Call(hwnd, uintptr(unsafe.Pointer(&cls[0])), uintptr(len(cls)))
			if windows.UTF16ToString(cls[:n]) == "webview" { // the app window's class in go-webview2
				cloakedHwnd = hwnd
				v := int32(1)
				procDwmSetAt.Call(hwnd, dwmaCloak, uintptr(unsafe.Pointer(&v)), 4)
			}
		}
	}
	r, _, _ := procNextHook.Call(cloakHook, code, wp, lp)
	return r
}

// startCloak must run on the GUI thread just before the window is created.
func startCloak() {
	tid, _, _ := procThreadID.Call()
	cloakHook, _, _ = procSetHook.Call(whCallWndRet, cloakHookProc, 0, tid)
}

// endCloakHook stops watching for new windows once the app window exists.
func endCloakHook() {
	if cloakHook != 0 {
		procUnhook.Call(cloakHook)
		cloakHook = 0
	}
}

// uncloak shows the window. The page calls it after its first frame; the
// fallback calls it if that never comes. Runs once.
func uncloak() {
	uncloakOnce.Do(func() {
		if cloakedHwnd == 0 {
			return
		}
		v := int32(0)
		procDwmSetAt.Call(cloakedHwnd, dwmaCloak, uintptr(unsafe.Pointer(&v)), 4)
	})
}
