package main

import (
	"fmt"
	"html"
	"net"
	"net/http"
	"strings"
)

// Link previews: when a share link is pasted into Messenger, WhatsApp,
// Telegram, Discord etc., their bots fetch the page and read these tags to
// show a card with the share's name, size and the ShareMile image.

var previewBots = []string{"facebookexternalhit", "facebot", "whatsapp", "telegrambot", "discordbot",
	"twitterbot", "slackbot", "linkedinbot", "skypeuripreview", "viber", "googlebot", "bingbot", "embedly", "redditbot", "pinterest", "applebot"}

func isPreviewBot(r *http.Request) bool {
	ua := strings.ToLower(r.UserAgent())
	for _, b := range previewBots {
		if strings.Contains(ua, b) {
			return true
		}
	}
	return false
}

// viaRelay reports whether the request came through the Cloudflare tunnel.
func viaRelay(r *http.Request) bool {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	p := net.ParseIP(ip)
	return p != nil && p.IsLoopback() && r.Header.Get("Cf-Connecting-Ip") != ""
}

func requestBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || viaRelay(r) || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func humanBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f, i := float64(n), 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

func sharePage(r *http.Request, s *Share) string {
	b, _ := webFS.ReadFile("web/share.html")
	page := strings.ReplaceAll(string(b), "{{VERSION}}", appVersion)

	files := "1 file"
	if len(s.Files) != 1 {
		files = fmt.Sprintf("%d files", len(s.Files))
	}
	title := html.EscapeString(s.Name)
	desc := html.EscapeString(files + " · " + humanBytes(s.Total) + ". Shared with ShareMile. Open to preview and download, no app needed.")
	base := requestBase(r)
	img := base + "/assets/og.png?v=" + appVersion
	url := html.EscapeString(base + r.URL.Path)
	tags := `<title>` + title + ` · ShareMile</title>
<meta name="description" content="` + desc + `">
<meta property="og:type" content="website">
<meta property="og:site_name" content="ShareMile">
<meta property="og:title" content="` + title + `">
<meta property="og:description" content="` + desc + `">
<meta property="og:url" content="` + url + `">
<meta property="og:image" content="` + img + `">
<meta property="og:image:width" content="1200">
<meta property="og:image:height" content="630">
<meta property="og:image:alt" content="ShareMile">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:title" content="` + title + `">
<meta name="twitter:description" content="` + desc + `">
<meta name="twitter:image" content="` + img + `">
<meta name="theme-color" content="#2ee6a6">`
	page = strings.Replace(page, "<title>ShareMile</title>", tags, 1)

	if viaRelay(r) {
		page = strings.Replace(page,
			`title="Files come straight from the sharer's PC. No cloud in between."><i></i>Direct transfer`,
			`title="Files stream from the sharer's PC through an encrypted relay. Nothing is stored in the cloud."><i></i>Secure relay`, 1)
	}
	return page
}
