package main

/*
#cgo pkg-config: gtk+-3.0 webkit2gtk-4.1
#include <stdlib.h>
#include <stdio.h>
#include <string.h>
#include <gtk/gtk.h>
#include <webkit2/webkit2.h>

extern void goDispatch(int id);
extern int goOnClose(void);
extern void goOnDrop(char *uris);
extern void goOnMessage(char *msg);
extern void goOpenExternal(char *uri);

static GtkWidget *smWin;
static WebKitWebView *smWeb;
static char *smBase;     // the app page's own address; anything else opens in the browser
static gboolean smDropping;
static GString *smDragUris; // the file list WebKit fetched while the pointer moved

static gboolean sm_idle(gpointer p) { goDispatch(GPOINTER_TO_INT(p)); return G_SOURCE_REMOVE; }
static void sm_dispatch(int id) { g_idle_add(sm_idle, GINT_TO_POINTER(id)); }

static gboolean sm_delete(GtkWidget *w, GdkEvent *e, gpointer d) { return goOnClose() ? FALSE : TRUE; }
static void sm_destroy(GtkWidget *w, gpointer d) { smWin = NULL; gtk_main_quit(); }

// A drop: WebKit fetches the dragged data while the pointer moves and usually
// not again on the drop, so the last list seen is kept and sent on the drop.
// If none came yet, the data that arrives after the drop is sent instead.
static void sm_send_drop(void) {
	goOnDrop(smDragUris ? smDragUris->str : "");
	if (smDragUris) { g_string_free(smDragUris, TRUE); smDragUris = NULL; }
}
static gboolean sm_drag_motion(GtkWidget *w, GdkDragContext *c, gint x, gint y, guint t, gpointer d) {
	smDropping = FALSE; // a drag still moving: no drop is waiting for data
	return FALSE;
}
static gboolean sm_drag_drop(GtkWidget *w, GdkDragContext *c, gint x, gint y, guint t, gpointer d) {
	if (smDragUris) sm_send_drop();
	else smDropping = TRUE;
	return FALSE; // WebKit carries on and fires the page's drop event
}
static void sm_drag_data(GtkWidget *w, GdkDragContext *c, gint x, gint y, GtkSelectionData *s, guint info, guint t, gpointer d) {
	gchar **uris = gtk_selection_data_get_uris(s);
	if (uris) {
		if (smDragUris) g_string_free(smDragUris, TRUE);
		smDragUris = g_string_new(NULL);
		for (int i = 0; uris[i]; i++) { g_string_append(smDragUris, uris[i]); g_string_append_c(smDragUris, '\n'); }
		g_strfreev(uris);
	}
	if (smDropping && smDragUris) { smDropping = FALSE; sm_send_drop(); }
}

static void sm_script_msg(WebKitUserContentManager *m, WebKitJavascriptResult *r, gpointer d) {
	JSCValue *v = webkit_javascript_result_get_js_value(r);
	char *s = jsc_value_to_string(v);
	goOnMessage(s);
	g_free(s);
}

static gboolean sm_policy(WebKitWebView *v, WebKitPolicyDecision *dec, WebKitPolicyDecisionType type, gpointer d) {
	if (type != WEBKIT_POLICY_DECISION_TYPE_NAVIGATION_ACTION && type != WEBKIT_POLICY_DECISION_TYPE_NEW_WINDOW_ACTION) return FALSE;
	WebKitNavigationAction *a = webkit_navigation_policy_decision_get_navigation_action(WEBKIT_NAVIGATION_POLICY_DECISION(dec));
	const char *uri = webkit_uri_request_get_uri(webkit_navigation_action_get_request(a));
	if (!uri) return FALSE;
	if (type == WEBKIT_POLICY_DECISION_TYPE_NAVIGATION_ACTION && smBase && strncmp(uri, smBase, strlen(smBase)) == 0) return FALSE;
	if (strncmp(uri, "about:", 6) == 0) return FALSE;
	// Links out of the app open in the user's browser, never inside the app window.
	goOpenExternal((char *)uri);
	webkit_policy_decision_ignore(dec);
	return TRUE;
}

// The Paste buttons read the clipboard: allow that (and nothing else) for the app page.
static gboolean sm_permission(WebKitWebView *v, WebKitPermissionRequest *req, gpointer d) {
	if (strcmp(G_OBJECT_TYPE_NAME(req), "WebKitClipboardPermissionRequest") == 0) webkit_permission_request_allow(req);
	else webkit_permission_request_deny(req);
	return TRUE;
}

static int sm_init(void) { return gtk_init_check(NULL, NULL) ? 1 : 0; }

static void sm_set_icon(const void *png, int n) {
	GdkPixbufLoader *l = gdk_pixbuf_loader_new();
	if (gdk_pixbuf_loader_write(l, png, n, NULL) && gdk_pixbuf_loader_close(l, NULL)) {
		GdkPixbuf *pb = gdk_pixbuf_loader_get_pixbuf(l);
		if (pb) gtk_window_set_default_icon(pb);
	}
	g_object_unref(l);
}

static void sm_create(const char *title, const char *dataDir, const char *base, int w, int h, int minw, int minh) {
	smBase = g_strdup(base);
	smWin = gtk_window_new(GTK_WINDOW_TOPLEVEL);
	gtk_window_set_title(GTK_WINDOW(smWin), title);
	gtk_window_set_default_size(GTK_WINDOW(smWin), w, h);
	gtk_window_set_position(GTK_WINDOW(smWin), GTK_WIN_POS_CENTER);
	GdkGeometry g = { .min_width = minw, .min_height = minh };
	gtk_window_set_geometry_hints(GTK_WINDOW(smWin), NULL, &g, GDK_HINT_MIN_SIZE);

	char *cache = g_build_filename(dataDir, "cache", NULL);
	WebKitWebsiteDataManager *dm = webkit_website_data_manager_new("base-data-directory", dataDir, "base-cache-directory", cache, NULL);
	g_free(cache);
	WebKitWebContext *ctx = webkit_web_context_new_with_website_data_manager(dm);
	webkit_web_context_set_cache_model(ctx, WEBKIT_CACHE_MODEL_DOCUMENT_VIEWER); // smallest memory use
	WebKitUserContentManager *ucm = webkit_user_content_manager_new();
	g_signal_connect(ucm, "script-message-received::sm", G_CALLBACK(sm_script_msg), NULL);
	webkit_user_content_manager_register_script_message_handler(ucm, "sm");
	webkit_user_content_manager_add_script(ucm, webkit_user_script_new(
		"window.smNative='gtk';window.smSetDarkTitle=function(d){window.webkit.messageHandlers.sm.postMessage(JSON.stringify({op:'dark',on:!!d}))};",
		WEBKIT_USER_CONTENT_INJECT_TOP_FRAME, WEBKIT_USER_SCRIPT_INJECT_AT_DOCUMENT_START, NULL, NULL));
	smWeb = WEBKIT_WEB_VIEW(g_object_new(WEBKIT_TYPE_WEB_VIEW, "web-context", ctx, "user-content-manager", ucm, NULL));
	WebKitSettings *s = webkit_web_view_get_settings(smWeb);
	webkit_settings_set_javascript_can_access_clipboard(s, TRUE);
	webkit_settings_set_enable_developer_extras(s, FALSE);
	webkit_settings_set_enable_back_forward_navigation_gestures(s, FALSE);

	g_signal_connect(smWeb, "decide-policy", G_CALLBACK(sm_policy), NULL);
	g_signal_connect(smWeb, "permission-request", G_CALLBACK(sm_permission), NULL);
	g_signal_connect(smWeb, "drag-motion", G_CALLBACK(sm_drag_motion), NULL);
	g_signal_connect(smWeb, "drag-drop", G_CALLBACK(sm_drag_drop), NULL);
	g_signal_connect(smWeb, "drag-data-received", G_CALLBACK(sm_drag_data), NULL);
	g_signal_connect(smWin, "delete-event", G_CALLBACK(sm_delete), NULL);
	g_signal_connect(smWin, "destroy", G_CALLBACK(sm_destroy), NULL);
	gtk_container_add(GTK_CONTAINER(smWin), GTK_WIDGET(smWeb));
	webkit_web_view_load_uri(smWeb, base);
	gtk_widget_show_all(smWin);
	gtk_window_present(GTK_WINDOW(smWin));
}

static void sm_run(void) { gtk_main(); }
static void sm_quit(void) { if (smWin) gtk_widget_destroy(smWin); else gtk_main_quit(); }
static void sm_show(void) {
	if (!smWin) return;
	gtk_widget_show(smWin);
	gtk_window_deiconify(GTK_WINDOW(smWin));
	gtk_window_present(GTK_WINDOW(smWin));
}
static void sm_hide(void) { if (smWin) gtk_widget_hide(smWin); }
static void sm_close(void) { if (smWin) gtk_window_close(GTK_WINDOW(smWin)); } // like the title-bar X
static void sm_iconify(void) { if (smWin) gtk_window_iconify(GTK_WINDOW(smWin)); }
static int sm_visible(void) { return smWin && gtk_widget_get_visible(smWin); }
static int sm_iconified(void) {
	if (!smWin || !gtk_widget_get_window(smWin)) return 0;
	return (gdk_window_get_state(gtk_widget_get_window(smWin)) & GDK_WINDOW_STATE_ICONIFIED) != 0;
}

static void sm_eval(const char *js) {
	if (smWeb) webkit_web_view_evaluate_javascript(smWeb, js, -1, NULL, NULL, NULL, NULL, NULL);
}

static void sm_dark(int on) {
	g_object_set(gtk_settings_get_default(), "gtk-application-prefer-dark-theme", on ? TRUE : FALSE, NULL);
}

// sm_pick opens the desktop's file picker (through the portal on Flatpak-era
// desktops) and returns the chosen paths, one per line, or NULL.
static char *sm_pick(int folders, const char *title) {
	GtkFileChooserNative *d = gtk_file_chooser_native_new(title, smWin ? GTK_WINDOW(smWin) : NULL,
		folders ? GTK_FILE_CHOOSER_ACTION_SELECT_FOLDER : GTK_FILE_CHOOSER_ACTION_OPEN, NULL, NULL);
	gtk_file_chooser_set_select_multiple(GTK_FILE_CHOOSER(d), TRUE);
	gtk_file_chooser_set_local_only(GTK_FILE_CHOOSER(d), TRUE);
	char *out = NULL;
	if (gtk_native_dialog_run(GTK_NATIVE_DIALOG(d)) == GTK_RESPONSE_ACCEPT) {
		GSList *l = gtk_file_chooser_get_filenames(GTK_FILE_CHOOSER(d));
		GString *all = g_string_new(NULL);
		for (GSList *i = l; i; i = i->next) { g_string_append(all, (char *)i->data); g_string_append_c(all, '\n'); }
		g_slist_free_full(l, g_free);
		out = g_string_free(all, FALSE);
	}
	g_object_unref(d);
	return out;
}

// kind: 0 info, 1 warning, 2 error. yesno: Yes/No buttons. Returns 1 for Yes/OK.
static int sm_message(const char *title, const char *text, int kind, int yesno) {
	GtkMessageType t = kind == 2 ? GTK_MESSAGE_ERROR : kind == 1 ? GTK_MESSAGE_WARNING : GTK_MESSAGE_INFO;
	GtkWidget *d = gtk_message_dialog_new(smWin ? GTK_WINDOW(smWin) : NULL, GTK_DIALOG_MODAL, t,
		yesno ? GTK_BUTTONS_YES_NO : GTK_BUTTONS_OK, "%s", text);
	gtk_window_set_title(GTK_WINDOW(d), title);
	int r = gtk_dialog_run(GTK_DIALOG(d));
	gtk_widget_destroy(d);
	return r == GTK_RESPONSE_YES || r == GTK_RESPONSE_OK;
}

// Testing aid (only with SHAREMILE_DEBUG_DIR set): save what the window shows.
static void sm_snap_done(GObject *o, GAsyncResult *res, gpointer path) {
	cairo_surface_t *s = webkit_web_view_get_snapshot_finish(WEBKIT_WEB_VIEW(o), res, NULL);
	if (s) { cairo_surface_write_to_png(s, (char *)path); cairo_surface_destroy(s); }
	g_free(path);
}
static void sm_snapshot(const char *path) {
	if (smWeb) webkit_web_view_get_snapshot(smWeb, WEBKIT_SNAPSHOT_REGION_VISIBLE, WEBKIT_SNAPSHOT_OPTIONS_NONE, NULL, sm_snap_done, g_strdup(path));
}
*/
import "C"

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

var (
	guiOn     bool // the window exists and GTK runs
	gtkInited bool
	dispMu    sync.Mutex
	dispFns   = map[int]func(){}
	dispNext  int
)

func guiActive() bool { return guiOn }

// guiAvailable tells whether there is a desktop to show a window on.
func guiAvailable() bool {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return false
	}
	if !gtkInited {
		gtkInited = C.sm_init() != 0
	}
	return gtkInited
}

// onMain runs f on the GTK thread and waits for it (or runs it right away
// when already there).
func onMain(f func()) {
	if !guiOn {
		return
	}
	if onGTKThread() {
		f()
		return
	}
	done := make(chan struct{})
	dispMu.Lock()
	dispNext++
	id := dispNext
	dispFns[id] = func() { f(); close(done) }
	dispMu.Unlock()
	C.sm_dispatch(C.int(id))
	<-done
}

// onMainAsync queues f on the GTK thread without waiting.
func onMainAsync(f func()) {
	if !guiOn {
		return
	}
	dispMu.Lock()
	dispNext++
	id := dispNext
	dispFns[id] = f
	dispMu.Unlock()
	C.sm_dispatch(C.int(id))
}

//export goDispatch
func goDispatch(id C.int) {
	dispMu.Lock()
	f := dispFns[int(id)]
	delete(dispFns, int(id))
	dispMu.Unlock()
	if f != nil {
		f()
	}
}

//export goOnClose
func goOnClose() C.int {
	if onWindowClose() {
		return 1
	}
	return 0
}

//export goOnDrop
func goOnDrop(uris *C.char) {
	var paths []string
	for _, u := range strings.Split(C.GoString(uris), "\n") {
		u = strings.TrimSpace(u)
		if !strings.HasPrefix(u, "file://") {
			continue
		}
		pu, err := url.Parse(u)
		if err != nil || (pu.Host != "" && pu.Host != "localhost") {
			continue // a file on another computer
		}
		paths = append(paths, filepath.Clean(pu.Path))
	}
	if paths == nil {
		paths = []string{}
	}
	b, _ := json.Marshal(paths)
	js := C.CString("window.smDropped && window.smDropped(" + string(b) + ")")
	C.sm_eval(js)
	C.free(unsafe.Pointer(js))
}

//export goOnMessage
func goOnMessage(msg *C.char) {
	var m struct {
		Op   string `json:"op"`
		On   bool   `json:"on"`
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(C.GoString(msg)), &m) != nil {
		return
	}
	switch m.Op {
	case "dark":
		C.sm_dark(boolInt(m.On))
	case "log":
		debugLog(m.Text)
	}
}

//export goOpenExternal
func goOpenExternal(uri *C.char) {
	u := C.GoString(uri)
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "mailto:") {
		go openURL(u)
	}
}

func boolInt(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

func setDarkTitle(dark bool) { onMainAsync(func() { C.sm_dark(boolInt(dark)) }) }

func guiQuit() { onMainAsync(func() { C.sm_quit() }) }

func showMainWindow() { onMainAsync(func() { C.sm_show() }) }

func runGUI() {
	// A blank window on some graphics drivers (and in virtual machines) comes
	// from WebKit's DMA-BUF renderer; the plain one is light and works everywhere.
	if os.Getenv("WEBKIT_DISABLE_DMABUF_RENDERER") == "" {
		os.Setenv("WEBKIT_DISABLE_DMABUF_RENDERER", "1")
	}
	if !guiAvailable() {
		return
	}
	C.g_set_prgname(C.CString("sharemile")) // matches StartupWMClass in the .desktop file
	C.g_set_application_name(C.CString(appName))
	if b, err := assetFS.ReadFile("assets/icon.png"); err == nil && len(b) > 0 {
		C.sm_set_icon(unsafe.Pointer(&b[0]), C.int(len(b)))
	}
	cdir := C.CString(filepath.Join(appDataDir(), "webview"))
	ctitle := C.CString(appName)
	cbase := C.CString(ctrlURL)
	defer C.free(unsafe.Pointer(cdir))
	defer C.free(unsafe.Pointer(ctitle))
	defer C.free(unsafe.Pointer(cbase))
	C.sm_dark(boolInt(isDarkTheme(cfg.Theme)))
	C.sm_create(ctitle, cdir, cbase, 1040, 720, 780, 560)
	guiOn = true
	startTray()
	startDebugHook()
	C.sm_run()
	guiOn = false
	removeTray()
}

func pickPaths(folders bool, title string) ([]string, error) {
	if !guiOn {
		return nil, errors.New("there is no window to show a file picker in")
	}
	var out []string
	onMain(func() {
		ct := C.CString(title)
		defer C.free(unsafe.Pointer(ct))
		r := C.sm_pick(boolInt(folders), ct)
		if r == nil {
			return
		}
		defer C.g_free(C.gpointer(r))
		for _, p := range strings.Split(C.GoString(r), "\n") {
			if p != "" {
				out = append(out, p)
			}
		}
	})
	return out, nil
}

// messageBox mirrors the Windows call: flags 0x10 error, 0x30 warning,
// 0x40 info, 0x4 Yes/No. Returns 6 (Yes) or 1 (OK) like Windows, else 7.
func messageBox(title, text string, flags uintptr) int {
	kind := 0
	switch {
	case flags&0xF0 == 0x10:
		kind = 2
	case flags&0xF0 == 0x30:
		kind = 1
	}
	yesno := flags&0xF == 0x4
	if !guiOn && !gtkInited {
		os.Stderr.WriteString(title + ": " + text + "\n")
		return 7
	}
	ok := false
	show := func() {
		ct, cx := C.CString(title), C.CString(text)
		ok = C.sm_message(ct, cx, C.int(kind), boolInt(yesno)) != 0
		C.free(unsafe.Pointer(ct))
		C.free(unsafe.Pointer(cx))
	}
	if guiOn {
		onMain(show)
	} else {
		show()
	}
	switch {
	case ok && yesno:
		return 6
	case ok:
		return 1
	}
	return 7
}

// onGTKThread: callbacks from GTK (window close, dispatched work) already run
// on its thread, the main thread, where waiting for it would hang.
var mainTid = syscall.Gettid() // package init runs on the main thread

func onGTKThread() bool { return syscall.Gettid() == mainTid }

func windowVisible() bool {
	v := false
	onMain(func() { v = C.sm_visible() != 0 })
	return v
}

func windowIconified() bool {
	v := false
	onMain(func() { v = C.sm_iconified() != 0 })
	return v
}

func hideWindow()    { onMainAsync(func() { C.sm_hide() }) }
func iconifyWindow() { onMainAsync(func() { C.sm_iconify() }) }
func closeWindow()   { onMainAsync(func() { C.sm_close() }) }


// evalJS and snapshotTo run on the GTK thread.
func evalJS(js string) {
	c := C.CString(js)
	C.sm_eval(c)
	C.free(unsafe.Pointer(c))
}

func snapshotTo(p string) {
	c := C.CString(p)
	C.sm_snapshot(c)
	C.free(unsafe.Pointer(c))
}
