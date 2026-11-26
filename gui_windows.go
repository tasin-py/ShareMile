package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

var (
	wv           webview2.WebView
	dwmapi       = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetAt = dwmapi.NewProc("DwmSetWindowAttribute")
)

func setDarkTitle(dark bool) {
	if mainHwnd == 0 {
		return
	}
	v := int32(0)
	if dark {
		v = 1
	}
	procDwmSetAt.Call(mainHwnd, 20, uintptr(unsafe.Pointer(&v)), 4)
}

// guiActive tells whether the app window exists (not headless).
func guiActive() bool { return mainHwnd != 0 }

func guiQuit() {
	if wv != nil {
		wv.Dispatch(func() { wv.Terminate() })
	}
}

// singleInstance makes sure one ShareMile runs per data folder. A second copy
// brings the first one's window forward and returns false.
func singleInstance(updated bool) bool {
	h := sha256.Sum256([]byte(strings.ToLower(appDataDir())))
	mtx, _ := windows.UTF16PtrFromString("Local\\ShareMile-" + hex.EncodeToString(h[:6]))
	for tries := 0; ; tries++ {
		hm, err := windows.CreateMutex(nil, false, mtx)
		if err != windows.ERROR_ALREADY_EXISTS {
			return true
		}
		windows.CloseHandle(hm)
		// Right after a self-update the old copy is still shutting down: wait for it.
		if !updated || tries >= 60 {
			focusExisting()
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func runGUI() {
	// Keep the UI shell light: no GPU process, one renderer, no background extras.
	if os.Getenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS") == "" {
		os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", "--disable-gpu --disable-gpu-compositing --renderer-process-limit=1 --disable-extensions --disable-background-networking --disable-component-update --autoplay-policy=no-user-gesture-required --disable-features=msSmartScreenProtection,Translate,AutofillServerCommunication,msEdgeShopping")
	}
	wv = webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath:  filepath.Join(appDataDir(), "webview"),
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  appName,
			Width:  1040,
			Height: 720,
			IconId: 1,
			Center: true,
		},
	})
	if wv == nil {
		if messageBox(appName, "ShareMile needs the Microsoft Edge WebView2 Runtime, which is missing on this PC.\n\nOpen the download page now?", 0x34) == 6 {
			openURL("https://go.microsoft.com/fwlink/p/?LinkId=2124703")
		}
		return
	}
	defer wv.Destroy()
	mainHwnd = uintptr(wv.Window())
	wv.SetSize(780, 560, webview2.HintMin)
	setDarkTitle(isDarkTheme(cfg.Theme))
	wv.Bind("smSetDarkTitle", func(dark bool) error { setDarkTitle(dark); return nil })
	installDropHandler(wv)
	hookClose(mainHwnd, onWindowClose)
	syncTray()
	wv.Navigate(ctrlURL)
	wv.Run()
	removeTray()
}

// openPath opens a file or folder with its default app.
func openPath(p string) error { return shellExec("open", p, "") }

// guiAvailable: Windows always has a desktop to show the window on.
func guiAvailable() bool { return true }
