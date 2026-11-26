package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

// Drag and drop from Explorer. A page can't see the real path of a dropped
// file, so the UI hands the dropped files to the host with
// chrome.webview.postMessageWithAdditionalObjects("sm-drop", files), and this
// handler reads each file's path (ICoreWebView2File) and passes the paths
// back to the page through window.smDropped([...]).

var (
	iidMsgArgs2 = windows.GUID{Data1: 0x06fc7ab7, Data2: 0xc90c, Data3: 0x4297, Data4: [8]byte{0x93, 0x89, 0x33, 0xca, 0x01, 0xcf, 0x6d, 0x5e}}
	iidWVFile   = windows.GUID{Data1: 0xf2c19559, Data2: 0x6bc1, Data3: 0x4583, Data4: [8]byte{0xa7, 0x57, 0x90, 0x02, 0x1b, 0xe9, 0xaf, 0xec}}
)

type comHandlerVtbl struct{ qi, addRef, release, invoke uintptr }
type comHandler struct{ vtbl *comHandlerVtbl }

var dropHandler *comHandler // kept alive for the life of the window

func comMethod(obj uintptr, idx int) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	return *(*uintptr)(unsafe.Pointer(vtbl + uintptr(idx)*unsafe.Sizeof(uintptr(0))))
}

func comQI(obj uintptr, iid *windows.GUID) uintptr {
	var out uintptr
	if r, _, _ := syscall.SyscallN(comMethod(obj, 0), obj, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out))); r != 0 {
		return 0
	}
	return out
}

func comRelease(obj uintptr) {
	if obj != 0 {
		syscall.SyscallN(comMethod(obj, 2), obj)
	}
}

// installDropHandler adds a second WebMessageReceived handler to the window's
// WebView2. Failures are silent: drag and drop just stays unavailable.
func installDropHandler(w webview2.WebView) {
	defer func() { recover() }()
	v := reflect.ValueOf(w)
	if v.Kind() != reflect.Ptr {
		return
	}
	f := v.Elem().FieldByName("browser")
	if !f.IsValid() {
		return
	}
	ch, ok := reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Interface().(*edge.Chromium)
	if !ok || ch == nil {
		return
	}
	wvController = ch.GetController()
	core := uintptr(unsafe.Pointer(ch.GetICoreWebView2_3())) // same object, ICoreWebView2 vtable first
	if core == 0 {
		return
	}
	dropHandler = &comHandler{vtbl: &comHandlerVtbl{
		qi: syscall.NewCallback(func(this, iid, out uintptr) uintptr {
			*(*uintptr)(unsafe.Pointer(out)) = this
			return 0
		}),
		addRef:  syscall.NewCallback(func(this uintptr) uintptr { return 1 }),
		release: syscall.NewCallback(func(this uintptr) uintptr { return 1 }),
		invoke:  syscall.NewCallback(onWebMessage),
	}}
	var token int64
	syscall.SyscallN(comMethod(core, 34), core, uintptr(unsafe.Pointer(dropHandler)), uintptr(unsafe.Pointer(&token))) // add_WebMessageReceived

	// go-webview2 means to allow clipboard reads but never reads the permission
	// kind correctly, so the Paste buttons would trigger a browser prompt.
	// Allow clipboard reads (and nothing else) for the app's own page.
	permHandler = &comHandler{vtbl: &comHandlerVtbl{qi: dropHandler.vtbl.qi, addRef: dropHandler.vtbl.addRef, release: dropHandler.vtbl.release,
		invoke: syscall.NewCallback(onPermission)}}
	syscall.SyscallN(comMethod(core, 23), core, uintptr(unsafe.Pointer(permHandler)), uintptr(unsafe.Pointer(&token))) // add_PermissionRequested
}

var permHandler *comHandler

func onPermission(this, sender, args uintptr) uintptr {
	defer func() { recover() }()
	var kind int32
	if r, _, _ := syscall.SyscallN(comMethod(args, 4), args, uintptr(unsafe.Pointer(&kind))); r != 0 || kind != 6 { // get_PermissionKind: CLIPBOARD_READ
		return 0
	}
	var uri *uint16
	if r, _, _ := syscall.SyscallN(comMethod(args, 3), args, uintptr(unsafe.Pointer(&uri))); r != 0 || uri == nil { // get_Uri
		return 0
	}
	u := windows.UTF16PtrToString(uri)
	windows.CoTaskMemFree(unsafe.Pointer(uri))
	// The uri is the page address or just its origin (http://127.0.0.1:port/).
	if ctrlURL != "" && u != "" && (strings.HasPrefix(u, ctrlURL) || (strings.HasSuffix(u, "/") && strings.HasPrefix(ctrlURL, u))) {
		syscall.SyscallN(comMethod(args, 7), args, 1) // put_State(ALLOW)
	}
	return 0
}

func onWebMessage(this, sender, args uintptr) uintptr {
	defer func() { recover() }()
	var msg *uint16
	if r, _, _ := syscall.SyscallN(comMethod(args, 5), args, uintptr(unsafe.Pointer(&msg))); r != 0 || msg == nil { // TryGetWebMessageAsString
		return 0
	}
	s := windows.UTF16PtrToString(msg)
	windows.CoTaskMemFree(unsafe.Pointer(msg))
	if s != "sm-drop" {
		return 0
	}
	paths := []string{}
	if a2 := comQI(args, &iidMsgArgs2); a2 != 0 {
		var coll uintptr
		if r, _, _ := syscall.SyscallN(comMethod(a2, 6), a2, uintptr(unsafe.Pointer(&coll))); r == 0 && coll != 0 { // get_AdditionalObjects
			var n uint32
			syscall.SyscallN(comMethod(coll, 3), coll, uintptr(unsafe.Pointer(&n))) // get_Count
			for i := uint32(0); i < n && i < 500; i++ {
				var obj uintptr
				if r, _, _ := syscall.SyscallN(comMethod(coll, 4), coll, uintptr(i), uintptr(unsafe.Pointer(&obj))); r != 0 || obj == 0 {
					continue
				}
				if file := comQI(obj, &iidWVFile); file != 0 {
					var p *uint16
					if r, _, _ := syscall.SyscallN(comMethod(file, 3), file, uintptr(unsafe.Pointer(&p))); r == 0 && p != nil { // get_Path
						paths = append(paths, windows.UTF16PtrToString(p))
						windows.CoTaskMemFree(unsafe.Pointer(p))
					}
					comRelease(file)
				}
				comRelease(obj)
			}
			comRelease(coll)
		}
		comRelease(a2)
	}
	b, _ := json.Marshal(paths)
	go wv.Dispatch(func() { wv.Eval("window.smDropped && window.smDropped(" + string(b) + ")") })
	return 0
}
