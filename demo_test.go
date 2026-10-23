package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestServeDemo runs the UI with a fake converter for screenshots:
// DEMO_PORT=47900 go test -run TestServeDemo -timeout 0
func TestServeDemo(t *testing.T) {
	port := os.Getenv("DEMO_PORT")
	if port == "" {
		t.Skip()
	}
	base := os.Getenv("DEMO_HOME")
	t.Setenv("CANTEVENREMARK_DOWNLOADS", filepath.Join(base, "Downloads"))
	p, _ := newPaths(filepath.Join(base, "app"))
	avail := Avail{Keynote: true, Pages: true, Numbers: true, Images: true}
	st := loadSettings(p)
	q := NewQueue(context.Background(), p, avail, &fakeConverter{delay: 2500 * time.Millisecond, t: t}, st, true)
	cloud := &fakeCloud{}
	cs := httptest.NewServer(cloud.handler(t))
	q.rm = newRMClient(p)
	q.rm.authHost, q.rm.uploadHost = cs.URL, cs.URL
	s := newServer(q, st, avail, "tok")
	s.opener = func(a ...string) error { return nil }
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	http.Serve(ln, s.routes())
}
