package main

import (
	"os"
	"path/filepath"
	"time"
)

// Testing aid, off unless SHAREMILE_DEBUG_DIR is set: lets automated tests
// look at the window and drive the page on machines where screenshots of the
// desktop aren't possible. Files dropped into that folder:
//
//	eval.js  -> runs in the page (window.smLog(x) writes x to log.txt)
//	shot     -> saves what the window shows to shot.png
func startDebugHook() {
	dir := os.Getenv("SHAREMILE_DEBUG_DIR")
	if dir == "" {
		return
	}
	os.MkdirAll(dir, 0o755)
	go func() {
		for guiActive() || !debugStarted {
			debugStarted = true
			time.Sleep(300 * time.Millisecond)
			if b, err := os.ReadFile(filepath.Join(dir, "eval.js")); err == nil {
				os.Remove(filepath.Join(dir, "eval.js"))
				js := "window.smLog=function(x){window.webkit.messageHandlers.sm.postMessage(JSON.stringify({op:'log',text:String(x)}))};" + string(b)
				onMainAsync(func() { evalJS(js) })
			}
			if _, err := os.Stat(filepath.Join(dir, "close")); err == nil {
				os.Remove(filepath.Join(dir, "close"))
				closeWindow()
			}
			if _, err := os.Stat(filepath.Join(dir, "state")); err == nil {
				os.Remove(filepath.Join(dir, "state"))
				st := "hidden"
				if windowVisible() {
					st = "visible"
				}
				if windowIconified() {
					st = "minimized"
				}
				debugLog("window:" + st)
			}
			if _, err := os.Stat(filepath.Join(dir, "shot")); err == nil {
				os.Remove(filepath.Join(dir, "shot"))
				p := filepath.Join(dir, "shot.png")
				onMainAsync(func() { snapshotTo(p) })
			}
		}
	}()
}

var debugStarted bool

func debugLog(text string) {
	dir := os.Getenv("SHAREMILE_DEBUG_DIR")
	if dir == "" {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "log.txt"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		f.WriteString(text + "\n")
		f.Close()
	}
}
