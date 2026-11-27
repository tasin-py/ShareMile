package main

import (
	"sync"
	"syscall"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

// Notification-area (tray) icon: with "Run in the background" on, closing the
// window hides ShareMile there. Right-click: Open, Pause/Resume sharing, Exit.

const (
	wmTray       = 0x8000 + 1 // WM_APP+1
	trayOpen     = 1
	trayPause    = 2
	trayExit     = 3
	nimAdd       = 0
	nimModify    = 1
	nimDelete    = 2
	nifMessage   = 0x1
	nifIcon      = 0x2
	nifTip       = 0x4
	nifInfo      = 0x10
	niifInfo     = 0x1
	niifNoSound  = 0x10
	wmLButtonUp  = 0x202
	wmLButtonDbl = 0x203
	wmRButtonUp  = 0x205
)

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     uintptr
}

var (
	procShellNotify     = shell32.NewProc("Shell_NotifyIconW")
	procLoadImage       = user32.NewProc("LoadImageW")
	procCreatePopupMenu = user32.NewProc("CreatePopupMenu")
	procAppendMenu      = user32.NewProc("AppendMenuW")
	procTrackPopupMenu  = user32.NewProc("TrackPopupMenu")
	procDestroyMenu     = user32.NewProc("DestroyMenu")
	procGetCursorPos    = user32.NewProc("GetCursorPos")
	procPostMessage     = user32.NewProc("PostMessageW")
	procRegisterWinMsg  = user32.NewProc("RegisterWindowMessageW")
	procGetSysMetrics   = user32.NewProc("GetSystemMetrics")
	procIsWindowVisible = user32.NewProc("IsWindowVisible")

	trayMu        sync.Mutex
	trayShown     bool
	trayIcon      uintptr
	taskbarMsg    uintptr // "TaskbarCreated": Explorer restarted, add the icon again
	exiting       bool    // Exit chosen: let the window close for real
	hintShown     bool
	wvController  *edge.ICoreWebView2Controller
	trayMenuTexts = map[int]string{}
)

func backgroundOn() bool {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return cfg.Background == nil || *cfg.Background
}

func newTrayData() *notifyIconData {
	d := &notifyIconData{HWnd: mainHwnd, UID: 1}
	d.CbSize = uint32(unsafe.Sizeof(*d))
	return d
}

func setUTF16(dst []uint16, s string) {
	u, _ := windows.UTF16FromString(s)
	if len(u) > len(dst) {
		u = append(u[:len(dst)-1], 0)
	}
	copy(dst, u)
}

func trayTip() string {
	if isPausedAll() {
		return appName + " (paused)"
	}
	return appName
}

// syncTray shows or hides the tray icon to match the setting.
func syncTray() {
	if mainHwnd == 0 {
		return
	}
	trayMu.Lock()
	defer trayMu.Unlock()
	want := backgroundOn()
	if trayIcon == 0 {
		var h windows.Handle
		windows.GetModuleHandleEx(0, nil, &h)
		cx, _, _ := procGetSysMetrics.Call(49) // SM_CXSMICON
		cy, _, _ := procGetSysMetrics.Call(50)
		trayIcon, _, _ = procLoadImage.Call(uintptr(h), 1, 1, cx, cy, 0) // icon resource #1
		if taskbarMsg == 0 {
			p, _ := windows.UTF16PtrFromString("TaskbarCreated")
			taskbarMsg, _, _ = procRegisterWinMsg.Call(uintptr(unsafe.Pointer(p)))
		}
	}
	d := newTrayData()
	switch {
	case want && !trayShown:
		d.UFlags = nifMessage | nifIcon | nifTip
		d.UCallbackMessage = wmTray
		d.HIcon = trayIcon
		setUTF16(d.SzTip[:], trayTip())
		r, _, _ := procShellNotify.Call(nimAdd, uintptr(unsafe.Pointer(d)))
		trayShown = r != 0
	case want:
		d.UFlags = nifTip
		setUTF16(d.SzTip[:], trayTip())
		procShellNotify.Call(nimModify, uintptr(unsafe.Pointer(d)))
	case trayShown:
		procShellNotify.Call(nimDelete, uintptr(unsafe.Pointer(d)))
		trayShown = false
	}
}

func removeTray() {
	trayMu.Lock()
	defer trayMu.Unlock()
	if trayShown {
		procShellNotify.Call(nimDelete, uintptr(unsafe.Pointer(newTrayData())))
		trayShown = false
	}
}

func trayBalloon(title, text string) {
	trayMu.Lock()
	defer trayMu.Unlock()
	if !trayShown {
		return
	}
	d := newTrayData()
	d.UFlags = nifInfo
	d.DwInfoFlags = niifInfo | niifNoSound
	setUTF16(d.SzInfoTitle[:], title)
	setUTF16(d.SzInfo[:], text)
	procShellNotify.Call(nimModify, uintptr(unsafe.Pointer(d)))
}

// setWebViewVisible tells WebView2 the page is (in)visible: hidden pages stop
// polling and WebView2 trims its memory while ShareMile sits in the tray.
func setWebViewVisible(on bool) {
	if wvController == nil {
		return
	}
	v := uintptr(0)
	if on {
		v = 1
	}
	c := uintptr(unsafe.Pointer(wvController))
	syscall.SyscallN(comMethod(c, 4), c, v) // put_IsVisible
}

func showMainWindow() {
	if mainHwnd == 0 {
		return
	}
	setWebViewVisible(true)
	uncloak()                        // asked for the window before the page finished its first frame
	procShowWindow.Call(mainHwnd, 5) // SW_SHOW
	if r, _, _ := procIsIconic.Call(mainHwnd); r != 0 {
		procShowWindow.Call(mainHwnd, 9) // SW_RESTORE
	}
	procSetFg.Call(mainHwnd)
}

func hideToTray() {
	procShowWindow.Call(mainHwnd, 0) // SW_HIDE
	setWebViewVisible(false)
	cfgMu.Lock()
	first := !cfg.TrayHintShown
	if first {
		cfg.TrayHintShown = true
		saveConfigLocked()
	}
	cfgMu.Unlock()
	if first && !hintShown {
		hintShown = true
		trayBalloon("ShareMile is still running", "Your shares and networks keep working. Right-click this icon to pause or exit.")
	}
}

// exitApp closes ShareMile for real (tray → Exit).
func exitApp() {
	exiting = true
	removeTray()
	quitApp()
}

// onWindowClose decides what the close button does.
func onWindowClose() bool {
	if exiting {
		return true
	}
	if backgroundOn() {
		syncTray()
		if trayShown {
			hideToTray()
			return false
		}
	}
	if !busy() {
		return true
	}
	return messageBox(appName, "ShareMile is still sharing or downloading.\n\nClose it and stop everything?", 0x34) == 6
}

// onTrayMessage handles clicks on the tray icon. Runs on the UI thread.
func onTrayMessage(lp uintptr) {
	switch lp & 0xffff {
	case wmLButtonUp, wmLButtonDbl:
		showMainWindow()
	case wmRButtonUp:
		m, _, _ := procCreatePopupMenu.Call()
		if m == 0 {
			return
		}
		defer procDestroyMenu.Call(m)
		add := func(id int, text string) {
			p, _ := windows.UTF16PtrFromString(text)
			procAppendMenu.Call(m, 0, uintptr(id), uintptr(unsafe.Pointer(p)))
		}
		add(trayOpen, "Open ShareMile")
		if isPausedAll() {
			add(trayPause, "Resume sharing")
		} else {
			add(trayPause, "Pause sharing")
		}
		procAppendMenu.Call(m, 0x800, 0, 0) // separator
		add(trayExit, "Exit")
		var pt struct{ X, Y int32 }
		procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
		procSetFg.Call(mainHwnd)
		cmd, _, _ := procTrackPopupMenu.Call(m, 0x100|0x2|0x20, uintptr(pt.X), uintptr(pt.Y), 0, mainHwnd, 0) // RETURNCMD|RIGHTBUTTON|BOTTOMALIGN
		procPostMessage.Call(mainHwnd, 0, 0, 0)
		switch cmd {
		case trayOpen:
			showMainWindow()
		case trayPause:
			go func() {
				on := !isPausedAll()
				setPausedAll(on)
				if on {
					trayBalloon("Sharing paused", "Links and networks are offline and downloads are paused. Right-click to resume.")
				}
			}()
		case trayExit:
			exitApp()
		}
	}
}
