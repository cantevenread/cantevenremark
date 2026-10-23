//go:build darwin

// Native macOS window: a plain NSWindow hosting the system's own WebKit view
// (the same engine as Safari — nothing bundled, no Chromium). Everything is
// called through the Objective-C runtime with purego, so no cgo/Xcode needed.
package main

import (
	"errors"
	"log"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

func init() {
	// Cocoa must run on the main thread; keep the main goroutine there.
	runtime.LockOSThread()
}

type nsPoint struct{ X, Y float64 }
type nsSize struct{ W, H float64 }
type nsRect struct {
	Origin nsPoint
	Size   nsSize
}

const (
	styleTitled         = 1 << 0
	styleClosable       = 1 << 1
	styleMiniaturizable = 1 << 2
	styleResizable      = 1 << 3
	backingBuffered     = 2
	autoresizeWH        = 2 | 16 // NSViewWidthSizable | NSViewHeightSizable
	modalOK             = 1
)

var selCache sync.Map

func sel(name string) objc.SEL {
	if v, ok := selCache.Load(name); ok {
		return v.(objc.SEL)
	}
	s := objc.RegisterName(name)
	selCache.Store(name, s)
	return s
}

func cls(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

func nsstr(s string) objc.ID {
	return cls("NSString").Send(sel("stringWithUTF8String:"), s)
}

func goStr(id objc.ID) string {
	if id == 0 {
		return ""
	}
	p := objc.Send[*byte](id, sel("UTF8String"))
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(p, n))
}

func urlsToPaths(arr objc.ID) []string {
	if arr == 0 {
		return nil
	}
	n := objc.Send[uint](arr, sel("count"))
	out := make([]string, 0, n)
	for i := uint(0); i < n; i++ {
		u := arr.Send(sel("objectAtIndex:"), i)
		if p := goStr(u.Send(sel("path"))); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---------- running code on the main thread ----------

var (
	dispatchAsyncF func(queue, ctx, fn uintptr)
	mainQueue      uintptr
	mainCallback   uintptr
	mainFuncs      = make(chan func(), 256)
)

func setupDispatch() error {
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	purego.RegisterLibFunc(&dispatchAsyncF, lib, "dispatch_async_f")
	if mainQueue, err = purego.Dlsym(lib, "_dispatch_main_q"); err != nil {
		return err
	}
	mainCallback = purego.NewCallback(func(ctx uintptr) uintptr {
		select {
		case f := <-mainFuncs:
			f()
		default:
		}
		return 0
	})
	return nil
}

func onMainAsync(f func()) {
	mainFuncs <- f
	dispatchAsyncF(mainQueue, 0, mainCallback)
}

func onMain(f func()) {
	done := make(chan struct{})
	onMainAsync(func() {
		defer close(done)
		f()
	})
	<-done
}

// ---------- the app ----------

type nativeApp struct {
	app, win, web objc.ID
}

var (
	nativeOnOpen      func([]string)
	nativeOnTerminate func()
	theNative         *nativeApp
)

func newNativeApp(onOpen func([]string), onTerminate func()) (*nativeApp, error) {
	for _, fw := range []string{
		"/System/Library/Frameworks/Cocoa.framework/Cocoa",
		"/System/Library/Frameworks/WebKit.framework/WebKit",
	} {
		if _, err := purego.Dlopen(fw, purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
			return nil, err
		}
	}
	if err := setupDispatch(); err != nil {
		return nil, err
	}
	nativeOnOpen, nativeOnTerminate = onOpen, onTerminate

	a := &nativeApp{}
	a.app = cls("NSApplication").Send(sel("sharedApplication"))
	if a.app == 0 {
		return nil, errors.New("no NSApplication")
	}
	a.app.Send(sel("setActivationPolicy:"), 0) // regular app with Dock icon

	var protocols []*objc.Protocol
	if p := objc.GetProtocol("NSApplicationDelegate"); p != nil {
		protocols = append(protocols, p)
	}
	delegateClass, err := objc.RegisterClass("CERAppDelegate", objc.GetClass("NSObject"), protocols, nil, []objc.MethodDef{
		{Cmd: sel("applicationShouldTerminateAfterLastWindowClosed:"), Fn: func(self objc.ID, cmd objc.SEL, sender objc.ID) bool {
			return true
		}},
		{Cmd: sel("application:openURLs:"), Fn: func(self objc.ID, cmd objc.SEL, app objc.ID, urls objc.ID) {
			if paths := urlsToPaths(urls); len(paths) > 0 && nativeOnOpen != nil {
				go nativeOnOpen(paths)
			}
		}},
		{Cmd: sel("applicationWillTerminate:"), Fn: func(self objc.ID, cmd objc.SEL, note objc.ID) {
			if nativeOnTerminate != nil {
				nativeOnTerminate()
			}
		}},
		{Cmd: sel("applicationShouldHandleReopen:hasVisibleWindows:"), Fn: func(self objc.ID, cmd objc.SEL, app objc.ID, visible bool) bool {
			if !visible && theNative != nil && theNative.win != 0 {
				theNative.win.Send(sel("makeKeyAndOrderFront:"), objc.ID(0))
			}
			return true
		}},
	})
	if err != nil {
		return nil, err
	}
	delegate := objc.ID(delegateClass).Send(sel("new"))
	a.app.Send(sel("setDelegate:"), delegate)
	a.buildMenu()
	theNative = a
	return a, nil
}

func menuItem(title, action, key string) objc.ID {
	var act objc.SEL
	if action != "" {
		act = sel(action)
	}
	return cls("NSMenuItem").Send(sel("alloc")).Send(sel("initWithTitle:action:keyEquivalent:"), nsstr(title), act, nsstr(key))
}

func submenu(bar objc.ID, title string, items ...objc.ID) {
	holder := cls("NSMenuItem").Send(sel("new"))
	m := cls("NSMenu").Send(sel("alloc")).Send(sel("initWithTitle:"), nsstr(title))
	for _, it := range items {
		m.Send(sel("addItem:"), it)
	}
	holder.Send(sel("setSubmenu:"), m)
	bar.Send(sel("addItem:"), holder)
}

func (a *nativeApp) buildMenu() {
	sep := func() objc.ID { return cls("NSMenuItem").Send(sel("separatorItem")) }
	bar := cls("NSMenu").Send(sel("new"))
	submenu(bar, "cantevenReMark",
		menuItem("About cantevenReMark", "orderFrontStandardAboutPanel:", ""),
		sep(),
		menuItem("Hide cantevenReMark", "hide:", "h"),
		menuItem("Show All", "unhideAllApplications:", ""),
		sep(),
		menuItem("Quit cantevenReMark", "terminate:", "q"),
	)
	submenu(bar, "Edit",
		menuItem("Undo", "undo:", "z"),
		menuItem("Redo", "redo:", "Z"),
		sep(),
		menuItem("Cut", "cut:", "x"),
		menuItem("Copy", "copy:", "c"),
		menuItem("Paste", "paste:", "v"),
		menuItem("Select All", "selectAll:", "a"),
	)
	submenu(bar, "View",
		menuItem("Enter Full Screen", "toggleFullScreen:", "f"),
	)
	submenu(bar, "Window",
		menuItem("Minimize", "performMiniaturize:", "m"),
		menuItem("Zoom", "performZoom:", ""),
		menuItem("Close", "performClose:", "w"),
	)
	a.app.Send(sel("setMainMenu:"), bar)
}

// Run opens the window on url and blocks in the macOS event loop.
func (a *nativeApp) Run(url string) {
	rect := nsRect{Size: nsSize{1140, 740}}
	a.win = cls("NSWindow").Send(sel("alloc")).Send(sel("initWithContentRect:styleMask:backing:defer:"),
		rect, styleTitled|styleClosable|styleMiniaturizable|styleResizable, backingBuffered, false)
	a.win.Send(sel("setTitle:"), nsstr("cantevenReMark"))
	a.win.Send(sel("setReleasedWhenClosed:"), false)
	a.win.Send(sel("setMinSize:"), nsSize{900, 620})
	a.win.Send(sel("center"))
	a.win.Send(sel("setFrameAutosaveName:"), nsstr("cantevenReMarkMain"))
	a.win.Send(sel("setTabbingMode:"), 2) // NSWindowTabbingModeDisallowed

	cfg := cls("WKWebViewConfiguration").Send(sel("new"))
	if os.Getenv("CANTEVENREMARK_DEVTOOLS") == "1" {
		prefs := cfg.Send(sel("preferences"))
		prefs.Send(sel("setValue:forKey:"), cls("NSNumber").Send(sel("numberWithBool:"), true), nsstr("developerExtrasEnabled"))
	}
	a.web = cls("WKWebView").Send(sel("alloc")).Send(sel("initWithFrame:configuration:"), rect, cfg)
	a.web.Send(sel("setAutoresizingMask:"), uint(autoresizeWH))
	// Let the window colour show until the page paints (no white flash in dark mode).
	a.web.Send(sel("setValue:forKey:"), cls("NSNumber").Send(sel("numberWithBool:"), false), nsstr("drawsBackground"))
	a.win.Send(sel("setContentView:"), a.web)

	nsurl := cls("NSURL").Send(sel("URLWithString:"), nsstr(url))
	a.web.Send(sel("loadRequest:"), cls("NSURLRequest").Send(sel("requestWithURL:"), nsurl))

	a.win.Send(sel("makeKeyAndOrderFront:"), objc.ID(0))
	a.app.Send(sel("activateIgnoringOtherApps:"), true)
	log.Printf("native window open")
	a.app.Send(sel("run"))
}

func (a *nativeApp) focus() {
	if a.win != 0 {
		a.win.Send(sel("makeKeyAndOrderFront:"), objc.ID(0))
	}
}

func (a *nativeApp) PickFiles() []string {
	var out []string
	onMain(func() {
		p := cls("NSOpenPanel").Send(sel("openPanel"))
		p.Send(sel("setCanChooseFiles:"), true)
		p.Send(sel("setCanChooseDirectories:"), true)
		p.Send(sel("setAllowsMultipleSelection:"), true)
		p.Send(sel("setMessage:"), nsstr("Choose files or folders to turn into PDFs"))
		p.Send(sel("setPrompt:"), nsstr("Add"))
		if objc.Send[int](p, sel("runModal")) == modalOK {
			out = urlsToPaths(p.Send(sel("URLs")))
		}
		a.focus()
	})
	return out
}

func (a *nativeApp) PickFolder(prompt string) string {
	var out string
	onMain(func() {
		p := cls("NSOpenPanel").Send(sel("openPanel"))
		p.Send(sel("setCanChooseFiles:"), false)
		p.Send(sel("setCanChooseDirectories:"), true)
		p.Send(sel("setCanCreateDirectories:"), true)
		p.Send(sel("setAllowsMultipleSelection:"), false)
		p.Send(sel("setMessage:"), nsstr(prompt))
		p.Send(sel("setPrompt:"), nsstr("Choose"))
		if objc.Send[int](p, sel("runModal")) == modalOK {
			if paths := urlsToPaths(p.Send(sel("URLs"))); len(paths) > 0 {
				out = paths[0]
			}
		}
		a.focus()
	})
	return out
}

func (a *nativeApp) Quit() {
	onMainAsync(func() { a.app.Send(sel("terminate:"), objc.ID(0)) })
}

const nativeSupported = true
