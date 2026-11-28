package main

import (
	"bytes"
	"runtime"
)

// themeBG is each theme's window colour (the page's --bg). The app page is
// served already in the saved theme, and on Windows the WebView paints this
// colour before the page loads, so launching never flashes another theme.
var themeBG = map[string]string{
	"pitch-black":   "000000",
	"midnight-blue": "060a16",
	"graphite":      "141416",
	"noir-gold":     "0b0a08",
	"aurora":        "04100e",
	"light":         "f5f5f2",
	"paper":         "f4eee2",
	"xp":            "ece9d8",
}

// savedTheme is the theme from the settings, or the default if it is unknown.
func savedTheme() string {
	cfgMu.Lock()
	t := cfg.Theme
	cfgMu.Unlock()
	if _, ok := themeBG[t]; !ok {
		return "pitch-black"
	}
	return t
}

// themedPage writes the saved theme into app.html before it is sent, so the
// first frame is already in the right theme. For Windows XP in the Windows
// window it also shows the XP title bar from the start.
func themedPage(b []byte) []byte {
	t := savedTheme()
	b = bytes.Replace(b, []byte(`<html lang="en" data-theme="pitch-black">`), []byte(`<html lang="en" data-theme="`+t+`">`), 1)
	if t == "xp" && runtime.GOOS == "windows" && guiActive() {
		b = bytes.Replace(b, []byte(`<body>`), []byte(`<body class="xp-chrome">`), 1)
		b = bytes.Replace(b, []byte(`<div class="xpbar" id="xpbar" hidden>`), []byte(`<div class="xpbar" id="xpbar">`), 1)
	}
	return b
}

// webPage fills in a browser page (the share page, the phone page, the
// "ended" page): the version, and the app's theme, so a link opened in a
// browser looks like the app, in the theme picked in Settings.
func webPage(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("{{VERSION}}"), []byte(appVersion))
	return bytes.Replace(b, []byte(`<html lang="en">`), []byte(`<html lang="en" data-theme="`+savedTheme()+`">`), 1)
}
