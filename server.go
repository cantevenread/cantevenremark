package main

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed ui/index.html
var indexHTML string

//go:embed ui/icon.svg
var iconSVG []byte

var iconURI = "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(iconSVG)

// NativeUI is provided by the macOS window (nil when running in a browser).
type NativeUI interface {
	PickFiles() []string             // files and/or folders
	PickFolder(prompt string) string // a single folder ("" if cancelled)
	Quit()
}

type Server struct {
	q        *Queue
	settings *SettingsStore
	avail    Avail
	token    string
	port     int
	native   NativeUI
	lastSeen atomic.Int64 // unix ms of last UI poll
	quit     chan struct{}
	opener   func(args ...string) error // macOS "open"; stubbed in tests
	onLoad   func()                     // called once, the first time the page talks to us
	loadOnce sync.Once
}

func newServer(q *Queue, st *SettingsStore, a Avail, token string) *Server {
	s := &Server{q: q, settings: st, avail: a, token: token, quit: make(chan struct{}, 1)}
	s.opener = func(args ...string) error { return exec.Command("open", args...).Start() }
	s.lastSeen.Store(time.Now().UnixMilli())
	return s
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "cantevenremark") })
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/pdf/{id}", s.handlePDF)

	post := func(path string, h http.HandlerFunc) { mux.HandleFunc("POST "+path, s.guard(h)) }
	post("/api/upload", s.handleUpload)
	post("/api/pick", s.handlePick)
	post("/api/unstage/{id}", func(w http.ResponseWriter, r *http.Request) { s.q.Unstage(r.PathValue("id")); ok(w) })
	post("/api/unstage-all", func(w http.ResponseWriter, r *http.Request) { s.q.ClearStaged(); ok(w) })
	post("/api/convert", s.handleConvert)
	post("/api/download/{id}", s.handleDownload)
	post("/api/download-all", s.handleDownloadAll)
	post("/api/open/{id}", s.handleOpen)
	post("/api/reveal/{id}", s.handleReveal)
	post("/api/open-downloads", s.handleOpenDownloads)
	post("/api/retry/{id}", s.handleRetry)
	post("/api/remove/{id}", s.handleRemove)
	post("/api/clear", s.handleClear)
	post("/api/settings", s.handleSettings)
	post("/api/choose-download-dir", s.handleChooseDir)
	post("/api/quit", s.handleQuit)
	post("/api/rm/pair", s.handlePair)
	post("/api/rm/disconnect", func(w http.ResponseWriter, r *http.Request) { s.q.rm.Disconnect(); ok(w) })
	post("/api/send/{id}", s.handleSend)
	post("/api/send-all", s.handleSendAll)
	post("/api/open-pair-page", func(w http.ResponseWriter, r *http.Request) {
		if err := s.opener("https://my.remarkable.com/device/browser/connect"); err != nil {
			writeErr(w, 500, err)
			return
		}
		ok(w)
	})
	return s.hostCheck(mux)
}

// hostCheck blocks DNS-rebinding: only requests addressed to our loopback port.
func (s *Server) hostCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.Host)
		if err != nil || (host != "127.0.0.1" && host != "localhost") || (s.port != 0 && port != strconv.Itoa(s.port)) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// guard requires the per-launch token so other websites can't drive the app.
func (s *Server) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-CER-Token") != s.token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func ok(w http.ResponseWriter) { writeJSON(w, 200, map[string]bool{"ok": true}) }

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	st := s.settings.Get()
	splash := "0"
	if st.Splash {
		splash = "1"
	}
	page := strings.NewReplacer("{{TOKEN}}", s.token, "{{VERSION}}", version, "{{THEME}}", st.Theme, "{{FONT}}", st.Font, "{{MOTION}}", st.Motion, "{{SPLASH}}", splash, "{{ICON}}", iconURI).Replace(indexHTML)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:")
	io.WriteString(w, page)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	s.lastSeen.Store(time.Now().UnixMilli())
	if s.onLoad != nil {
		s.loadOnce.Do(s.onLoad)
	}
	writeJSON(w, 200, map[string]any{
		"version":     version,
		"native":      s.native != nil,
		"staged":      s.q.StagedList(),
		"jobs":        s.q.Snapshot(),
		"batch":       s.q.LatestBatch(),
		"settings":    s.settings.Get(),
		"downloadDir": s.settings.DownloadDir(),
		"engines":     s.avail,
		"busy":        s.q.Busy(),
		"remarkable":  map[string]bool{"connected": s.q.rm != nil && s.q.rm.Connected(), "usb": s.q.rm != nil && s.q.rm.USBReady()},
	})
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<30)
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	n := 0
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		if part.FormName() != "files" || part.FileName() == "" {
			part.Close()
			continue
		}
		name := cleanName(part.FileName())
		dir, err := s.q.NewInboxDir()
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		dst := filepath.Join(dir, name)
		if err := saveStream(part, dst); err != nil {
			os.RemoveAll(dir)
			writeErr(w, 500, err)
			return
		}
		part.Close()
		s.q.StageUpload(dir, dst, name)
		n++
	}
	if n == 0 {
		writeErr(w, 400, errors.New("no files received"))
		return
	}
	writeJSON(w, 200, map[string]int{"added": n})
}

func (s *Server) handlePick(w http.ResponseWriter, r *http.Request) {
	if s.native == nil {
		writeErr(w, 400, errors.New("only available in the Mac app"))
		return
	}
	paths := s.native.PickFiles()
	writeJSON(w, 200, map[string]int{"added": s.q.StagePaths(paths)})
}

func (s *Server) handleConvert(w http.ResponseWriter, r *http.Request) {
	body := struct {
		Options
		Send bool `json:"send"` // send each PDF to the reMarkable when ready
		Save bool `json:"save"` // remember these choices as the saved style
	}{Options: defaultOptions(), Save: true}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	o := body.Options
	if err := o.validate(); err != nil {
		writeErr(w, 400, err)
		return
	}
	sendTo := ""
	if body.Send {
		sendTo = s.sendMethod()
	}
	b, err := s.q.ConvertStagedAndSend(o, sendTo)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if st := s.settings.Get(); st.Remember && body.Save {
		st.Defaults = o
		_, _ = s.settings.Set(st)
	}
	writeJSON(w, 200, map[string]any{"batch": b.ID, "count": len(b.JobIDs)})
}

func (s *Server) handlePDF(w http.ResponseWriter, r *http.Request) {
	j, found := s.q.Get(r.PathValue("id"))
	if !found || j.Status != StatusDone {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="`+strings.ReplaceAll(filepath.Base(j.OutPath), `"`, "")+`"`)
	http.ServeFile(w, r, j.OutPath)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	dest, err := s.q.SaveToDownloads(r.PathValue("id"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]string{"path": dest, "name": filepath.Base(dest)})
}

func (s *Server) handleDownloadAll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Batch string `json:"batch"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	n, err := s.q.SaveAll(body.Batch)
	if err != nil && n == 0 {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"saved": n, "folder": filepath.Base(s.settings.DownloadDir())})
}

func (s *Server) doneJob(w http.ResponseWriter, r *http.Request) (*Job, bool) {
	j, found := s.q.Get(r.PathValue("id"))
	if !found || j.Status != StatusDone {
		writeErr(w, 404, errors.New("not found"))
		return nil, false
	}
	return j, true
}

func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	if j, found := s.doneJob(w, r); found {
		if err := s.opener(j.OutPath); err != nil {
			writeErr(w, 500, err)
			return
		}
		ok(w)
	}
}

func (s *Server) handleReveal(w http.ResponseWriter, r *http.Request) {
	if j, found := s.doneJob(w, r); found {
		target := j.OutPath
		if j.SavedTo != "" {
			if _, err := os.Stat(j.SavedTo); err == nil {
				target = j.SavedTo
			}
		}
		if err := s.opener("-R", target); err != nil {
			writeErr(w, 500, err)
			return
		}
		ok(w)
	}
}

func (s *Server) handleOpenDownloads(w http.ResponseWriter, r *http.Request) {
	if err := s.opener(s.settings.DownloadDir()); err != nil {
		writeErr(w, 500, err)
		return
	}
	ok(w)
}

func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	if err := s.q.Retry(r.PathValue("id")); err != nil {
		writeErr(w, 400, err)
		return
	}
	ok(w)
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.q.Remove(r.PathValue("id")); err != nil {
		writeErr(w, 400, err)
		return
	}
	ok(w)
}

func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Batch string `json:"batch"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	s.q.ClearFinished(body.Batch)
	ok(w)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	st := s.settings.Get()
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&st); err != nil {
		writeErr(w, 400, err)
		return
	}
	saved, err := s.settings.Set(st)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, saved)
}

func (s *Server) handleChooseDir(w http.ResponseWriter, r *http.Request) {
	if s.native == nil {
		writeErr(w, 400, errors.New("only available in the Mac app"))
		return
	}
	dir := s.native.PickFolder("Choose where downloads are saved")
	if dir == "" {
		writeJSON(w, 200, map[string]bool{"cancelled": true})
		return
	}
	st := s.settings.Get()
	st.DownloadDir = dir
	saved, err := s.settings.Set(st)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, saved)
}

func (s *Server) handleQuit(w http.ResponseWriter, r *http.Request) {
	ok(w)
	if s.native != nil {
		go s.native.Quit()
		return
	}
	select {
	case s.quit <- struct{}{}:
	default:
	}
}

func saveStream(r io.Reader, dst string) error {
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	if err := s.q.rm.Pair(r.Context(), body.Code); err != nil {
		writeErr(w, 400, err)
		return
	}
	st := s.settings.Get()
	if st.SendTo == "mac" {
		st.SendTo = "cloud"
		_, _ = s.settings.Set(st)
	}
	ok(w)
}

func (s *Server) sendMethod() string {
	m := s.settings.Get().SendTo
	if m == "mac" {
		m = "cloud"
	}
	return m
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	if err := s.q.Send(r.PathValue("id"), s.sendMethod()); err != nil {
		writeErr(w, 400, err)
		return
	}
	ok(w)
}

func (s *Server) handleSendAll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Batch string `json:"batch"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	method := s.sendMethod()
	if method == "cloud" && !s.q.rm.Connected() {
		writeErr(w, 400, errors.New("connect your reMarkable first"))
		return
	}
	go func() {
		if _, err := s.q.SendAll(body.Batch, method); err != nil {
			log.Printf("send all: %v", err)
		}
	}()
	ok(w)
}
