package main

import (
	"sync/atomic"
	"unsafe"
)

// Windows XP theme: the window drops the system title bar and the page draws
// an XP one (icon, title, and the blue minimize, maximize and close buttons).
// The window keeps its resize borders, Aero Snap, the taskbar menu and the
// minimize and maximize animations; only the caption is gone.

var (
	procGetWinLong     = user32.NewProc("GetWindowLongPtrW")
	procSetWinPos      = user32.NewProc("SetWindowPos")
	procReleaseCapture = user32.NewProc("ReleaseCapture")
	procSendMessage    = user32.NewProc("SendMessageW")
	procIsZoomed       = user32.NewProc("IsZoomed")
	procDpiForWindow   = user32.NewProc("GetDpiForWindow")
	procSysMetricsDpi  = user32.NewProc("GetSystemMetricsForDpi")
	procMonFromWin     = user32.NewProc("MonitorFromWindow")
	procMonInfo        = user32.NewProc("GetMonitorInfoW")
)

const (
	gwlStyle  = ^uintptr(15) // GWL_STYLE (-16)
	wsCaption = 0x00C00000
)

var xpChrome atomic.Bool

// setXPChrome hides (on) or brings back (off) the system title bar.
func setXPChrome(on bool) {
	if mainHwnd == 0 || xpChrome.Load() == on {
		return
	}
	xpChrome.Store(on)
	st, _, _ := procGetWinLong.Call(mainHwnd, gwlStyle)
	if on {
		st &^= wsCaption
	} else {
		st |= wsCaption
	}
	procSetWinLong.Call(mainHwnd, gwlStyle, st)
	// SWP_NOMOVE|SWP_NOSIZE|SWP_NOZORDER|SWP_NOACTIVATE|SWP_FRAMECHANGED
	procSetWinPos.Call(mainHwnd, 0, 0, 0, 0, 0, 0x0002|0x0001|0x0004|0x0010|0x0020)
}

// winOp runs a title-bar button or a drag from the page's XP title bar.
func winOp(op string) {
	if mainHwnd == 0 {
		return
	}
	switch op {
	case "drag": // hand the press to Windows as a caption press: move, Aero Snap, shake
		procReleaseCapture.Call()
		procSendMessage.Call(mainHwnd, 0x00A1, 2, 0) // WM_NCLBUTTONDOWN, HTCAPTION
	case "min":
		procShowWindow.Call(mainHwnd, 6) // SW_MINIMIZE
	case "max":
		if z, _, _ := procIsZoomed.Call(mainHwnd); z != 0 {
			procShowWindow.Call(mainHwnd, 9) // SW_RESTORE
		} else {
			procShowWindow.Call(mainHwnd, 3) // SW_MAXIMIZE
		}
	case "close": // the same as the system close button: goes to the tray if set
		procPostMessage.Call(mainHwnd, 0x0010, 0, 0) // WM_CLOSE
	}
}

// winMaximized tells the page whether to draw the restore glyph.
func winMaximized() bool {
	if mainHwnd == 0 {
		return false
	}
	z, _, _ := procIsZoomed.Call(mainHwnd)
	return z != 0
}

// chromeNCCalcSize keeps the client area flush with the top of the window when
// the caption is hidden, so Windows draws no white sizing strip there. Called
// from the window procedure for WM_NCCALCSIZE; ok=false lets Windows handle it.
func chromeNCCalcSize(h, wp, lp uintptr) (uintptr, bool) {
	if !xpChrome.Load() || wp == 0 {
		return 0, false
	}
	type rect struct{ L, T, R, B int32 }
	rc := (*rect)(unsafe.Pointer(lp)) // NCCALCSIZE_PARAMS.rgrc[0]
	top := rc.T
	r, _, _ := procCallWinProc.Call(oldWndProc, h, 0x0083, wp, lp)
	rc.T = top
	if z, _, _ := procIsZoomed.Call(h); z != 0 {
		// A maximized window hangs its frame off the screen: pull the top in by it.
		rc.T += frameSize(h)
	}
	return r, true
}

// frameSize is the width of the invisible sizing frame at the window's DPI.
func frameSize(h uintptr) int32 {
	dpi, _, _ := procDpiForWindow.Call(h)
	if dpi == 0 {
		dpi = 96
	}
	fy, _, _ := procSysMetricsDpi.Call(33, dpi)  // SM_CYSIZEFRAME
	pad, _, _ := procSysMetricsDpi.Call(92, dpi) // SM_CXPADDEDBORDER
	return int32(fy + pad)
}

// chromeMinMax keeps a maximized window inside the work area, so it stops at
// the taskbar: without a caption Windows would otherwise cover the whole screen.
// Called for WM_GETMINMAXINFO after the normal handler has set the minimum size.
func chromeMinMax(h, lp uintptr) {
	if !xpChrome.Load() {
		return
	}
	type rect struct{ L, T, R, B int32 }
	var mi struct {
		Size      uint32
		Mon, Work rect
		Flags     uint32
	}
	mi.Size = uint32(unsafe.Sizeof(mi))
	mon, _, _ := procMonFromWin.Call(h, 2) // MONITOR_DEFAULTTONEAREST
	if mon == 0 {
		return
	}
	if ok, _, _ := procMonInfo.Call(mon, uintptr(unsafe.Pointer(&mi))); ok == 0 {
		return
	}
	f := frameSize(h)
	mm := (*[5]struct{ X, Y int32 })(unsafe.Pointer(lp)) // reserved, max size, max position, min track, max track
	mm[1].X, mm[1].Y = mi.Work.R-mi.Work.L+2*f, mi.Work.B-mi.Work.T+2*f
	mm[2].X, mm[2].Y = mi.Work.L-mi.Mon.L-f, mi.Work.T-mi.Mon.T-f
}
