package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
)

const appName = "ShareMile"

var appVersion = "1.0.0" // overridden at build time with -ldflags "-X main.appVersion=..."

var (
	dataDirOverride string
	headless        bool
	quitCh          = make(chan struct{})
)

func init() {
	// The window toolkit (GTK on Linux) must stay on the main thread.
	runtime.LockOSThread()
}

func isDarkTheme(t string) bool { return t != "light" && t != "paper" }

func quitApp() {
	if headless || !guiActive() {
		select {
		case <-quitCh:
		default:
			close(quitCh)
		}
		return
	}
	guiQuit()
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
	return n > 0 || recvBusy()
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
	showVer := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVer {
		fmt.Println(appName, appVersion)
		return
	}
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

	if !singleInstance(*updated) {
		return
	}

	go cleanupOldExe()
	loadConfig()
	loadRecv()
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

	if !headless && !guiAvailable() {
		// No desktop to show a window on (an SSH session or a server):
		// keep running and serve the app page to a browser instead.
		headless = true
		fmt.Fprintln(os.Stderr, "No display found. Open this address in a browser on this computer to use ShareMile:")
	}
	if headless {
		fmt.Println(ctrlURL)
		os.WriteFile(filepath.Join(appDataDir(), "control-url.txt"), []byte(ctrlURL), 0o600)
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		select {
		case <-sig:
		case <-quitCh:
		}
		return
	}
	runGUI()
}

func appDataDirOverride() string { return dataDirOverride }
