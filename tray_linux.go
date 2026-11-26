package main

import (
	"sync"

	"fyne.io/systray"
	"github.com/godbus/dbus/v5"
)

// Tray icon on Linux: the StatusNotifierItem (KDE, Cinnamon, XFCE, MATE,
// Budgie, LXQt, and GNOME with the AppIndicator extension most distros add).
// Where no tray exists, closing the window minimizes it instead of hiding it,
// so ShareMile never runs invisible with no way back.

var (
	trayMu      sync.Mutex
	trayStarted bool
	trayEnd     func()
	trayPauseIt *systray.MenuItem
	exiting     bool
	hintShown   bool
)

func backgroundOn() bool {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return cfg.Background == nil || *cfg.Background
}

// trayHost tells whether the desktop shows tray icons right now.
func trayHost() bool {
	conn, err := dbus.SessionBus()
	if err != nil {
		return false
	}
	var has bool
	err = conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&has)
	return err == nil && has
}

func trayTip() string {
	if isPausedAll() {
		return appName + " (paused)"
	}
	return appName
}

func startTray() {
	trayMu.Lock()
	defer trayMu.Unlock()
	if trayStarted || !backgroundOn() {
		return
	}
	if _, err := dbus.SessionBus(); err != nil {
		return // no session bus: no tray, no notifications
	}
	trayStarted = true
	start, end := systray.RunWithExternalLoop(func() {
		if b, err := assetFS.ReadFile("assets/icon.png"); err == nil {
			systray.SetIcon(b)
		}
		systray.SetTitle(appName)
		systray.SetTooltip(trayTip())
		open := systray.AddMenuItem("Open ShareMile", "")
		trayPauseIt = systray.AddMenuItem(pauseLabel(), "")
		systray.AddSeparator()
		quit := systray.AddMenuItem("Exit", "")
		systray.SetOnTapped(showMainWindow)
		go func() {
			for {
				select {
				case <-open.ClickedCh:
					showMainWindow()
				case <-trayPauseIt.ClickedCh:
					on := !isPausedAll()
					setPausedAll(on)
					if on {
						trayBalloon("Sharing paused", "Links and networks are offline and downloads are paused. Use the tray icon to resume.")
					}
				case <-quit.ClickedCh:
					exitApp()
					return
				}
			}
		}()
	}, func() {})
	trayEnd = end
	start()
}

func pauseLabel() string {
	if isPausedAll() {
		return "Resume sharing"
	}
	return "Pause sharing"
}

// syncTray keeps the tray's menu and tooltip current (and starts the icon
// when "Run in the background" is turned on).
func syncTray() {
	if !guiActive() {
		return
	}
	startTray()
	trayMu.Lock()
	defer trayMu.Unlock()
	if !trayStarted {
		return
	}
	systray.SetTooltip(trayTip())
	if trayPauseIt != nil {
		trayPauseIt.SetTitle(pauseLabel())
	}
}

func removeTray() {
	trayMu.Lock()
	defer trayMu.Unlock()
	if trayStarted && trayEnd != nil {
		trayEnd()
	}
	trayStarted = false
}

// trayBalloon shows a desktop notification.
func trayBalloon(title, text string) {
	go func() {
		conn, err := dbus.SessionBus()
		if err != nil {
			return
		}
		obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
		obj.Call("org.freedesktop.Notifications.Notify", 0, appName, uint32(0), "sharemile", title, text,
			[]string{}, map[string]dbus.Variant{}, int32(6000))
	}()
}

func hideToTray() {
	hideWindow()
	cfgMu.Lock()
	first := !cfg.TrayHintShown
	if first {
		cfg.TrayHintShown = true
		saveConfigLocked()
	}
	cfgMu.Unlock()
	if first && !hintShown {
		hintShown = true
		trayBalloon("ShareMile is still running", "Your shares and networks keep working. Use the tray icon to open, pause or exit.")
	}
}

// exitApp closes ShareMile for real (tray → Exit).
func exitApp() {
	exiting = true
	quitApp()
}

// onWindowClose decides what the close button does. Runs on the GTK thread.
func onWindowClose() bool {
	if exiting {
		return true
	}
	if backgroundOn() {
		trayMu.Lock()
		started := trayStarted
		trayMu.Unlock()
		if started && trayHost() {
			hideToTray()
			return false
		}
		if busy() {
			// No tray on this desktop: keep sharing, just get out of the way.
			iconifyWindow()
			return false
		}
		return true
	}
	if !busy() {
		return true
	}
	return messageBox(appName, "ShareMile is still sharing or downloading.\n\nClose it and stop everything?", 0x34) == 6
}

// focusExisting is done through the instance socket on Linux.
func focusExisting() {}
