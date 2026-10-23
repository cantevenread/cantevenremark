// cantevenReMark — turn slides, documents, spreadsheets and images into clean,
// annotation-ready PDFs on macOS.
//
// Conversion uses the Apple apps already on the Mac (Keynote, Pages, Numbers),
// sips for images and LibreOffice if installed; pdfcpu adds writing space,
// page numbers, compression and merging. The window is a native NSWindow with
// the system WebKit view — no bundled browser.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var version = "2.3.0"

const preferredPort = 47821

func main() {
	var args []string
	for _, a := range os.Args[1:] {
		if !strings.HasPrefix(a, "-psn_") { // Finder adds this on old macOS
			args = append(args, a)
		}
	}

	fs := flag.NewFlagSet("cantevenremark", flag.ExitOnError)
	outDir := fs.String("o", ".", "where to put PDFs (command-line mode)")
	layout := fs.String("layout", "", "slides | notes | handout2 | handout3 | handout4 | handout6")
	builds := fs.Bool("builds", false, "one page per animation step (slides)")
	skipped := fs.Bool("skipped", false, "include skipped slides")
	space := fs.String("space", "", "writing space: none | bottom | right | remarkable")
	paper := fs.String("paper", "", "writing space style: blank | lined | grid | dots")
	numbers := fs.Bool("page-numbers", false, "add page numbers")
	compress := fs.Bool("compress", false, "make the PDF smaller")
	merge := fs.String("merge", "", "also combine everything into one PDF with this name")
	prefix := fs.String("prefix", "", "put this in front of every file name")
	browser := fs.Bool("browser", false, "use your web browser instead of the app window")
	native := fs.Bool("native", false, "force the app window even if it failed last time")
	showVersion := fs.Bool("version", false, "print version")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "cantevenReMark %s\n\n  cantevenremark                 open the app\n  cantevenremark [flags] files…  convert files right here\n\n", version)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	if *showVersion {
		fmt.Println("cantevenReMark", version)
		return
	}
	base, err := defaultBase()
	if err != nil {
		log.Fatal(err)
	}
	paths, err := newPaths(base)
	if err != nil {
		log.Fatal(err)
	}

	if fs.NArg() > 0 {
		st := loadSettings(paths)
		o := st.Get().Defaults
		if *layout != "" {
			o.Layout = *layout
		}
		if *space != "" {
			o.NoteSpace = *space
		}
		if *paper != "" {
			o.Paper = *paper
		}
		o.Builds = o.Builds || *builds
		o.Skipped = o.Skipped || *skipped
		o.PageNumbers = o.PageNumbers || *numbers
		o.Compress = o.Compress || *compress
		o.Merge = *merge != ""
		if *merge != "" {
			o.MergeName = *merge
		}
		if *prefix != "" {
			o.Prefix = *prefix
		}
		os.Exit(runCLI(paths, fs.Args(), *outDir, o.normalized()))
	}
	// Safety net: if the app window failed to load last time, use the browser instead.
	marker := filepath.Join(paths.State, "window-starting")
	useBrowser := *browser || !nativeSupported
	if !useBrowser && !*native {
		if _, err := os.Stat(marker); err == nil {
			useBrowser = true
			log.Printf("the app window didn't load last time; using the browser (run with --native to retry)")
		}
	}
	if *native {
		os.Remove(marker)
	}
	runApp(paths, useBrowser, marker)
}

// ---------- the app ----------

func runApp(p Paths, browserMode bool, marker string) {
	if !browserMode {
		// Finder launches apps with no terminal: keep a log for troubleshooting.
		logPath := filepath.Join(p.State, "log.txt")
		if st, e := os.Stat(logPath); e == nil && st.Size() > 2<<20 {
			_ = os.Remove(logPath)
		}
		if lf, e := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); e == nil {
			log.SetOutput(lf)
		}
	}

	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(preferredPort))
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Fatal(err)
		}
	}
	port := ln.Addr().(*net.TCPAddr).Port

	wipeWork(p) // converted files never outlive the app; only settings are kept
	avail := detectAvail()
	conv, err := newRealConverter(p, avail)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	settings := loadSettings(p)
	q := NewQueue(ctx, p, avail, conv, settings, false)
	q.rm = newRMClient(p)
	if a := settings.Get().Advanced; a.RMAuthHost != "" || a.RMUploadHost != "" || a.RMUSBAddress != "" {
		if a.RMAuthHost != "" {
			q.rm.authHost = strings.TrimRight(a.RMAuthHost, "/")
		}
		if a.RMUploadHost != "" {
			q.rm.uploadHost = strings.TrimRight(a.RMUploadHost, "/")
		}
		if a.RMUSBAddress != "" {
			q.rm.usbBase = strings.TrimRight(a.RMUSBAddress, "/")
		}
	}
	go q.rm.watchUSB(ctx, func() bool { return settings.Get().SendTo == "usb" })
	srv := newServer(q, settings, avail, newID()+newID())
	srv.port = port
	httpSrv := &http.Server{Handler: srv.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("server: %v", err)
		}
	}()
	u := "http://127.0.0.1:" + strconv.Itoa(port) + "/"
	log.Printf("cantevenReMark %s on %s (keynote=%v pages=%v numbers=%v libreoffice=%v)",
		version, u, avail.Keynote, avail.Pages, avail.Numbers, avail.LibreOffice)

	cleanup := func() {
		log.Printf("shutting down")
		sctx, scancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = httpSrv.Shutdown(sctx)
		scancel()
		cancel()
		for _, k := range []Kind{KindKeynote, KindPages, KindNumbers} {
			conv.AppIdle(k) // quit apps we opened (only if they have no documents open)
		}
		wipeWork(p)
	}

	if !browserMode {
		_ = os.WriteFile(marker, []byte(time.Now().String()), 0o644)
		srv.onLoad = func() { os.Remove(marker) }
		na, err := newNativeApp(func(paths []string) { q.StagePaths(paths) }, cleanup)
		if err == nil {
			srv.native = na
			na.Run(u) // blocks until the app quits
			return
		}
		os.Remove(marker)
		log.Printf("native window unavailable (%v); using the browser", err)
	}

	// Browser mode (also used for development on non-Mac systems).
	openBrowser(u)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
loop:
	for {
		select {
		case <-sig:
			break loop
		case <-srv.quit:
			break loop
		case <-tick.C:
			if os.Getenv("CANTEVENREMARK_STAY") == "" && !q.Busy() &&
				time.Since(time.UnixMilli(srv.lastSeen.Load())) > 3*time.Minute {
				break loop
			}
		}
	}
	cleanup()
}

func openBrowser(u string) {
	if runtime.GOOS != "darwin" {
		log.Printf("open %s in a browser", u)
		return
	}
	_ = exec.Command("open", u).Run()
}

// ---------- command-line mode ----------

func runCLI(p Paths, files []string, outDir string, opts Options) int {
	out, err := filepath.Abs(outDir)
	if err == nil {
		err = os.MkdirAll(out, 0o755)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "output folder:", err)
		return 2
	}
	avail := detectAvail()
	conv, err := newRealConverter(p, avail)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	q := NewQueue(context.Background(), p, avail, conv, nil, false)
	var jobs []*Job
	start := time.Now()
	for _, f := range files {
		abs, err := filepath.Abs(f)
		if err == nil {
			_, err = os.Stat(abs)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %s: %v\n", f, err)
			continue
		}
		work := filepath.Join(p.Inbox, newID())
		_ = os.MkdirAll(work, 0o755)
		jobs = append(jobs, q.Submit(newID(), work, abs, filepath.Base(abs), false, opts, out, ""))
	}
	failed, okCount := len(files)-len(jobs), 0
	var done []string
	for _, j := range jobs {
		q.Wait(j)
		if j.Status == StatusDone {
			okCount++
			done = append(done, j.OutPath)
			fmt.Printf("✓ %s → %s\n", j.Name, j.OutPath)
		} else {
			failed++
			fmt.Fprintf(os.Stderr, "✗ %s: %s\n", j.Name, j.Err)
		}
	}
	if opts.Merge && len(done) > 1 {
		dest := uniquePath(out, opts.Prefix+opts.MergeName+".pdf")
		if err := mergePDFs(done, dest); err != nil {
			fmt.Fprintf(os.Stderr, "✗ combine: %v\n", err)
			failed++
		} else {
			fmt.Printf("✓ combined → %s\n", dest)
		}
	}
	for _, k := range []Kind{KindKeynote, KindPages, KindNumbers} {
		conv.AppIdle(k)
	}
	fmt.Printf("%d converted, %d failed in %s\n", okCount, failed, time.Since(start).Round(time.Millisecond))
	if failed > 0 {
		return 1
	}
	return 0
}
