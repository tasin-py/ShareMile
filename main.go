package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

const appName = "ShareMile"

var appVersion = "1.0.0" // overridden at build time with -ldflags "-X main.appVersion=..."

var (
	dataDirOverride string
	wv              webview2.WebView
	headless        bool
	quitCh          = make(chan struct{})
)

var (
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

func isDarkTheme(t string) bool { return t != "light" && t != "paper" }

func quitApp() {
	if headless {
		close(quitCh)
		return
	}
	if wv != nil {
		wv.Dispatch(func() { wv.Terminate() })
	}
}

func busy() bool {
	sharesMu.RLock()
	n := len(shares)
	sharesMu.RUnlock()
	swarmMu.RLock()
	defer swarmMu.RUnlock()
	for _, h := range hosted {
		if h.cfg.Active {
			n++
		}
	}
	for _, j := range joined {
		for _, t := range j.taskList() {
			if t.State == "downloading" || t.State == "preparing" || t.State == "queued" {
				n++
			}
		}
	}
	return n > 0
}

func main() {
	var port int
	var pinTaskbar, pinStart bool
	flag.StringVar(&dataDirOverride, "data", "", "use a separate data folder (for running a second profile)")
	flag.IntVar(&port, "port", 0, "override the sharing port")
	flag.BoolVar(&headless, "headless", false, "run without a window and print the control URL")
	flag.BoolVar(&pinTaskbar, "pin-taskbar", false, "pin ShareMile to the taskbar and exit")
	flag.BoolVar(&pinStart, "pin-start", false, "pin ShareMile to Start and exit")
	desktop := flag.Bool("desktop-shortcut", false, "create a desktop shortcut and exit")
	updated := flag.Bool("updated", false, "internal: started after an update")
	flag.Parse()

	if *desktop {
		if makeDesktopShortcut() != nil {
			os.Exit(1)
		}
		return
	}
	if pinTaskbar || pinStart {
		mode := "taskbar"
		if pinStart {
			mode = "start"
		}
		if err := pinShortcut(mode); err != nil {
			os.Exit(1)
		}
		return
	}

	// One instance per data folder.
	h := sha256.Sum256([]byte(strings.ToLower(appDataDir())))
	mtx, _ := windows.UTF16PtrFromString("Local\\ShareMile-" + hex.EncodeToString(h[:6]))
	for tries := 0; ; tries++ {
		hm, err := windows.CreateMutex(nil, false, mtx)
		if err != windows.ERROR_ALREADY_EXISTS {
			break
		}
		windows.CloseHandle(hm)
		// Right after a self-update the old copy is still shutting down: wait for it.
		if !*updated || tries >= 60 {
			focusExisting()
			return
		}
		time.Sleep(250 * time.Millisecond)
	}

	go cleanupOldExe()
	loadConfig()
	if port > 0 {
		cfg.Port = port
	}
	if err := startPublicServer(cfg.Port); err != nil {
		// try a few neighbours so the app still works
		for p := cfg.Port + 1; p < cfg.Port+10; p++ {
			if startPublicServer(p) == nil {
				break
			}
		}
	}
	for _, n := range cfg.Networks {
		startHostedNet(n)
	}
	for _, j := range cfg.Joined {
		startJoined(j)
	}
	go netWatcher()
	go checkFirewallRule()
	if err := startControlServer(); err != nil {
		messageBox(appName, "Couldn't start: "+err.Error(), 0x10)
		return
	}
	defer func() {
		unmapUPnP()
		stopRelay()
	}()

	if headless {
		fmt.Println(ctrlURL)
		os.WriteFile(filepath.Join(appDataDir(), "control-url.txt"), []byte(ctrlURL), 0o600)
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		select {
		case <-sig:
		case <-quitCh:
		}
		return
	}

	// Keep the UI shell light: no GPU process, one renderer, no background extras.
	if os.Getenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS") == "" {
		os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", "--disable-gpu --disable-gpu-compositing --renderer-process-limit=1 --disable-extensions --disable-background-networking --disable-component-update --disable-features=msSmartScreenProtection,Translate,AutofillServerCommunication,msEdgeShopping")
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
	hookClose(mainHwnd, func() bool {
		if !busy() {
			return true
		}
		return messageBox(appName, "ShareMile is still sharing or downloading.\n\nClose it and stop everything?", 0x34) == 6
	})
	wv.Navigate(ctrlURL)
	wv.Run()
}

func appDataDirOverride() string { return dataDirOverride }
