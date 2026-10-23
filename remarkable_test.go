package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeCloud struct {
	mu         sync.Mutex
	uploads    []string // visible names
	userHits   int
	expireOnce bool
}

func (f *fakeCloud) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token/json/2/device/new", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		if r.Header.Get("Authorization") != "Bearer" || b["deviceDesc"] != "browser-chrome" || len(b["deviceID"]) != 36 {
			http.Error(w, "bad", 400)
			return
		}
		if b["code"] != "abcdefgh" {
			http.Error(w, "invalid code", 400)
			return
		}
		io.WriteString(w, "DEVTOKEN")
	})
	mux.HandleFunc("POST /token/json/2/user/new", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer DEVTOKEN" {
			http.Error(w, "no", 401)
			return
		}
		f.mu.Lock()
		f.userHits++
		f.mu.Unlock()
		io.WriteString(w, "USERTOKEN")
	})
	mux.HandleFunc("POST /doc/v2/files", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		if f.expireOnce {
			f.expireOnce = false
			f.mu.Unlock()
			http.Error(w, "expired", 401)
			return
		}
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer USERTOKEN" || r.Header.Get("Content-Type") != "application/pdf" || r.Header.Get("rm-source") != "RoR-Browser" {
			http.Error(w, "bad headers", 400)
			return
		}
		raw, err := base64.StdEncoding.DecodeString(r.Header.Get("rm-meta"))
		var meta map[string]string
		if err != nil || json.Unmarshal(raw, &meta) != nil || meta["file_name"] == "" {
			http.Error(w, "bad meta", 400)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.HasPrefix(string(body), "%PDF") {
			http.Error(w, "not a pdf", 400)
			return
		}
		f.mu.Lock()
		f.uploads = append(f.uploads, meta["file_name"])
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"docID": "d1", "hash": "h1"})
	})
	return mux
}

type fakeUSB struct {
	mu      sync.Mutex
	listed  bool
	uploads []string
}

func (f *fakeUSB) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /documents/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.listed = true
		f.mu.Unlock()
		io.WriteString(w, "[]")
	})
	mux.HandleFunc("POST /upload", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.listed || r.Header.Get("Origin") == "" {
			http.Error(w, "list first", 400)
			return
		}
		file, hdr, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b, _ := io.ReadAll(file)
		if !strings.HasPrefix(string(b), "%PDF") {
			http.Error(w, "not pdf", 400)
			return
		}
		f.uploads = append(f.uploads, hdr.Filename)
		w.WriteHeader(201)
		io.WriteString(w, `{"status":"Upload successful"}`)
	})
	return mux
}

func TestRMClient(t *testing.T) {
	dir := t.TempDir()
	p, _ := newPaths(filepath.Join(dir, "app"))
	cloud := &fakeCloud{}
	cs := httptest.NewServer(cloud.handler(t))
	defer cs.Close()
	usb := &fakeUSB{}
	us := httptest.NewServer(usb.handler())
	defer us.Close()

	c := newRMClient(p)
	c.authHost, c.uploadHost, c.usbBase = cs.URL, cs.URL, us.URL
	ctx := context.Background()

	if err := c.Pair(ctx, "abc"); err == nil {
		t.Fatal("short code accepted")
	}
	if err := c.Pair(ctx, "zzzzzzzz"); err == nil || !strings.Contains(err.Error(), "fresh") {
		t.Fatalf("bad code: %v", err)
	}
	if err := c.UploadCloud(ctx, "x", "x"); err == nil {
		t.Fatal("upload before pairing should fail")
	}
	if err := c.Pair(ctx, " ABCDEFGH "); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(p.State, "remarkable.json")); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Fatal("token file should be private")
	}
	// A new client picks the saved token up.
	c2 := newRMClient(p)
	if !c2.Connected() {
		t.Fatal("token not persisted")
	}

	pdf := filepath.Join(dir, "Week 6.pdf")
	makePDF(t, pdf, 1, 960, 540)
	if err := c.UploadCloud(ctx, pdf, "Week 6"); err != nil {
		t.Fatal(err)
	}
	cloud.expireOnce = true // user token expired → refresh and retry
	if err := c.UploadCloud(ctx, pdf, "Week 6 again"); err != nil {
		t.Fatal(err)
	}
	if len(cloud.uploads) != 2 || cloud.uploads[0] != "Week 6" || cloud.userHits != 2 {
		t.Fatalf("cloud uploads %v hits %d", cloud.uploads, cloud.userHits)
	}

	if !c.CheckUSB(ctx) || !c.USBReady() {
		t.Fatal("usb check")
	}
	if err := c.UploadUSB(ctx, pdf, "Week 6.pdf"); err != nil {
		t.Fatal(err)
	}
	if len(usb.uploads) != 1 || usb.uploads[0] != "Week 6.pdf" {
		t.Fatalf("usb uploads %v", usb.uploads)
	}

	c.Disconnect()
	if c.Connected() || newRMClient(p).Connected() {
		t.Fatal("disconnect")
	}
	c.usbBase = "http://127.0.0.1:1"
	if c.CheckUSB(ctx) {
		t.Fatal("usb should be unreachable")
	}
}

func TestSendThroughServer(t *testing.T) {
	s, ts, p := newTestServer(t)
	cloud := &fakeCloud{}
	cs := httptest.NewServer(cloud.handler(t))
	defer cs.Close()
	s.q.rm = newRMClient(p)
	s.q.rm.authHost, s.q.rm.uploadHost = cs.URL, cs.URL

	// Two files → convert.
	dir := t.TempDir()
	for _, n := range []string{"a.pptx", "b.docx"} {
		os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644)
	}
	s.q.StagePaths([]string{dir})
	if code, _ := post(t, ts.URL+"/api/convert", defaultOptions()); code != 200 {
		t.Fatal("convert")
	}
	waitIdle(t, s)
	jobs := getState(t, ts.URL).Jobs
	if len(jobs) != 2 {
		t.Fatalf("jobs %d", len(jobs))
	}
	if code, m := post(t, ts.URL+"/api/send/"+jobs[0].ID, nil); code != 400 || !strings.Contains(m["error"].(string), "connect") {
		t.Fatalf("send before pairing: %d %v", code, m)
	}
	if code, _ := post(t, ts.URL+"/api/rm/pair", map[string]string{"code": "abcdefgh"}); code != 200 {
		t.Fatal("pair")
	}
	if code, _ := post(t, ts.URL+"/api/send/"+jobs[0].ID, nil); code != 200 {
		t.Fatal("send")
	}
	if code, _ := post(t, ts.URL+"/api/send-all", map[string]string{"batch": jobs[0].Batch}); code != 200 {
		t.Fatal("send all")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n := 0
		for _, j := range getState(t, ts.URL).Jobs {
			if j.SentTo == "cloud" {
				n++
			}
		}
		if n == 2 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if len(cloud.uploads) != 2 {
		t.Fatalf("uploads %v", cloud.uploads)
	}
	res, _ := http.Get(ts.URL + "/api/state")
	var st struct {
		Remarkable map[string]bool
	}
	json.NewDecoder(res.Body).Decode(&st)
	if !st.Remarkable["connected"] {
		t.Fatal("state should show connected")
	}
	post(t, ts.URL+"/api/rm/disconnect", nil)
	if s.q.rm.Connected() {
		t.Fatal("disconnect")
	}
}

func TestConvertAndSendPDFsAsIs(t *testing.T) {
	s, ts, p := newTestServer(t)
	cloud := &fakeCloud{}
	cs := httptest.NewServer(cloud.handler(t))
	defer cs.Close()
	s.q.rm = newRMClient(p)
	s.q.rm.authHost, s.q.rm.uploadHost = cs.URL, cs.URL
	if err := s.q.rm.Pair(context.Background(), "abcdefgh"); err != nil {
		t.Fatal(err)
	}
	// Real converter path for PDFs is a straight copy; use the queue's real
	// converter so the bytes are untouched.
	conv, err := newRealConverter(p, s.avail)
	if err != nil {
		t.Fatal(err)
	}
	s.q.conv = conv
	dir := t.TempDir()
	src := filepath.Join(dir, "Lecture 9.pdf")
	makePDF(t, src, 3, 960, 540)
	orig, _ := os.ReadFile(src)
	s.q.StagePaths([]string{src})

	saved := s.settings.Get().Defaults
	o := defaultOptions()
	body := map[string]any{"layout": o.Layout, "noteSpace": "none", "paper": "blank", "mergeName": "x", "send": true, "save": false}
	if code, _ := post(t, ts.URL+"/api/convert", body); code != 200 {
		t.Fatal("convert")
	}
	deadline := time.Now().Add(5 * time.Second)
	var jv JobView
	for time.Now().Before(deadline) {
		js := getState(t, ts.URL).Jobs
		if len(js) == 1 && js[0].SentTo == "cloud" {
			jv = js[0]
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if jv.SentTo != "cloud" || len(cloud.uploads) != 1 || cloud.uploads[0] != "Lecture 9" {
		t.Fatalf("auto-send: %+v %v", jv, cloud.uploads)
	}
	j, _ := s.q.Get(jv.ID)
	out, _ := os.ReadFile(j.OutPath)
	if !bytes.Equal(out, orig) {
		t.Fatal("a PDF with no style should be passed through untouched")
	}
	if s.settings.Get().Defaults != saved {
		t.Fatal("save:false must not overwrite the saved style")
	}
	// Nothing is kept between launches.
	wipeWork(p)
	if entries, _ := os.ReadDir(p.Converted); len(entries) != 0 {
		t.Fatal("converted files should be wiped")
	}
}
