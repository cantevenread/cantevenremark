package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// makePDF writes a tiny valid PDF with n pages of w×h points.
func makePDF(t *testing.T, path string, n int, w, h float64) {
	t.Helper()
	var b bytes.Buffer
	var offs []int
	obj := func(s string) {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offs), s)
	}
	b.WriteString("%PDF-1.4\n")
	kids := ""
	for i := 0; i < n; i++ {
		kids += fmt.Sprintf("%d 0 R ", 3+i*2)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, n))
	for i := 0; i < n; i++ {
		content := fmt.Sprintf("0 0 1 rg 20 20 %.0f %.0f re f", w/3, h/3)
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.0f %.0f] /Contents %d 0 R >>", w, h, 4+i*2))
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, xref)
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mediaBoxes(t *testing.T, path string) []types.Rectangle {
	t.Helper()
	ctx, err := api.ReadContextFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []types.Rectangle
	for p := 1; p <= ctx.PageCount; p++ {
		d, _, inh, err := ctx.XRefTable.PageDict(p, false)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, *pageBox(ctx.XRefTable, d, "MediaBox", inh.MediaBox))
	}
	return out
}

func TestPlanFor(t *testing.T) {
	all := Avail{Keynote: true, Pages: true, Numbers: true, Images: true}
	cases := map[string]Kind{
		"Lecture 3.PPTX": KindKeynote, "notes.docx": KindPages, "grades.xlsx": KindNumbers,
		"cell.HEIC": KindImage, "already.pdf": KindPDF, "deck.key": KindKeynote, "readme.md": KindPages,
	}
	for name, want := range cases {
		p, err := planFor(name, all)
		if err != nil || p.Kind != want {
			t.Errorf("%s: got %v %v, want %v", name, p.Kind, err, want)
		}
	}
	if _, err := planFor("slides.pptx", Avail{}); err == nil || !strings.Contains(err.Error(), "Keynote") {
		t.Errorf("missing Keynote should explain: %v", err)
	}
	if p, err := planFor("slides.pptx", Avail{LibreOffice: true}); err != nil || p.Kind != KindLibreOffice {
		t.Errorf("should fall back to LibreOffice")
	}
	if _, err := planFor("weird.xyz", all); err == nil {
		t.Errorf("unknown ext should fail")
	}
}

func TestNamesAndOptions(t *testing.T) {
	for in, want := range map[string]string{"../../etc/passwd": "passwd", "a/b\\c.pptx": "c.pptx", ".hidden.docx": "hidden.docx", "": "file", "x:y.pdf": "x-y.pdf"} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q)=%q want %q", in, got, want)
		}
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.pdf"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "a (2).pdf"), nil, 0o644)
	if got := uniquePath(dir, "a.pdf"); filepath.Base(got) != "a (3).pdf" {
		t.Fatal(got)
	}
	o := Options{Layout: "bogus", NoteSpace: "x", Paper: "y", Prefix: "../BIO: ", MergeName: " Week 4.pdf"}.normalized()
	if o.Layout != "slides" || o.NoteSpace != "none" || o.Paper != "blank" || o.Prefix != "-BIO- " || o.MergeName != "Week 4" {
		t.Fatalf("%+v", o)
	}
	if m, n := (Options{Layout: "handout4"}).keynoteMode(); m != "handout" || n != 4 {
		t.Fatal("keynote mode")
	}
}

func TestParseOsaError(t *testing.T) {
	e := parseOsaError("/x/k.applescript:412:530: execution error: Keynote got an error: Not authorized to send Apple events to Keynote. (-1743)\n", "Keynote")
	if e.Code != -1743 || !isAuthErr(e) || !strings.Contains(e.Error(), "Automation") {
		t.Fatalf("%+v %s", e, e.Error())
	}
	if parseOsaError("x.applescript:1:5: syntax error: Expected end of line (-2741)", "Keynote").Code != -2741 {
		t.Fatal("syntax")
	}
}

func TestWritingArea(t *testing.T) {
	slide := types.Rectangle{UR: types.Point{X: 960, Y: 540}} // 16:9
	nb, area, ok := writingArea(slide, "remarkable")
	if !ok || nb.Width() != 960 || nb.Height() != 1280 || area.Height() != 740 || area.UR.Y != 0 {
		t.Fatalf("remarkable slide: %+v %+v", nb, area)
	}
	letter := types.Rectangle{UR: types.Point{X: 612, Y: 792}}
	nb, area, ok = writingArea(letter, "remarkable")
	if !ok || nb.Width()/nb.Height() < 0.749 || nb.Width()/nb.Height() > 0.751 || area.UR.Y != 0 {
		t.Fatalf("remarkable letter: %+v", nb)
	}
	tall := types.Rectangle{UR: types.Point{X: 300, Y: 800}}
	if nb, area, ok = writingArea(tall, "remarkable"); !ok || nb.Width() != 600 || area.LL.X != 300 {
		t.Fatalf("remarkable tall: %+v", nb)
	}
	if nb, _, ok = writingArea(slide, "right"); !ok || nb.Width() != 960*1.6 {
		t.Fatalf("right: %+v", nb)
	}
	if _, _, ok = writingArea(types.Rectangle{UR: types.Point{X: 600, Y: 800}}, "remarkable"); ok {
		t.Fatal("already 3:4 should be left alone")
	}
	for _, paper := range []string{"blank", "lined", "grid", "dots"} {
		c := string(paperContent(area, paper, 960, 540))
		if !strings.HasPrefix(c, "q\n1 g") || !strings.HasSuffix(c, "Q\n") {
			t.Fatalf("%s content: %q", paper, c[:20])
		}
	}
}

func TestPostProcessRealPDF(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "slides.pdf")
	makePDF(t, in, 3, 960, 540)

	out := filepath.Join(dir, "out.pdf")
	if err := postProcess(in, out, Options{NoteSpace: "remarkable", Paper: "lined", PageNumbers: true, Compress: true}.normalized()); err != nil {
		t.Fatal(err)
	}
	boxes := mediaBoxes(t, out)
	if len(boxes) != 3 {
		t.Fatalf("pages: %d", len(boxes))
	}
	for _, b := range boxes {
		if b.Width() != 960 || b.Height() != 1280 {
			t.Fatalf("box %+v", b)
		}
	}
	if err := api.ValidateFile(out, pdfConf()); err != nil {
		t.Fatalf("output doesn't validate: %v", err)
	}
	if pageCount(out) != 3 {
		t.Fatal("pageCount")
	}

	// Combine.
	in2 := filepath.Join(dir, "doc.pdf")
	makePDF(t, in2, 2, 612, 792)
	merged := filepath.Join(dir, "merged.pdf")
	if err := mergePDFs([]string{out, in2}, merged); err != nil {
		t.Fatal(err)
	}
	if pageCount(merged) != 5 {
		t.Fatalf("merged pages %d", pageCount(merged))
	}
	// No options → straight copy.
	plain := filepath.Join(dir, "plain.pdf")
	if err := postProcess(in, plain, defaultOptions()); err != nil || fileSize(plain) != fileSize(in) {
		t.Fatal("plain copy")
	}
}

// fakeConverter writes a small real PDF (or fails for names containing "bad").
type fakeConverter struct {
	mu    sync.Mutex
	idle  []Kind
	delay time.Duration
	t     *testing.T
}

func (f *fakeConverter) Convert(ctx context.Context, j *Job) (string, error) {
	time.Sleep(f.delay)
	if strings.Contains(j.Name, "bad") {
		return "", errors.New("Keynote: broken file")
	}
	out := filepath.Join(j.WorkDir, "out.pdf")
	makePDF(f.t, out, 2, 960, 540)
	return out, nil
}
func (f *fakeConverter) AppIdle(k Kind) { f.mu.Lock(); f.idle = append(f.idle, k); f.mu.Unlock() }

type fakeNative struct{ picked []string }

func (n *fakeNative) PickFiles() []string             { return n.picked }
func (n *fakeNative) PickFolder(prompt string) string { return filepath.Dir(n.picked[0]) }
func (n *fakeNative) Quit()                           {}

func newTestServer(t *testing.T) (*Server, *httptest.Server, Paths) {
	t.Helper()
	base := t.TempDir()
	dl := filepath.Join(base, "Downloads")
	t.Setenv("CANTEVENREMARK_DOWNLOADS", dl)
	p, err := newPaths(filepath.Join(base, "app"))
	if err != nil {
		t.Fatal(err)
	}
	avail := Avail{Keynote: true, Pages: true, Numbers: true, Images: true}
	st := loadSettings(p)
	q := NewQueue(context.Background(), p, avail, &fakeConverter{delay: 20 * time.Millisecond, t: t}, st, true)
	q.idleWait = 50 * time.Millisecond
	s := newServer(q, st, avail, "tok")
	s.opener = func(a ...string) error { return nil }
	ts := httptest.NewServer(s.routes())
	t.Cleanup(ts.Close)
	return s, ts, p
}

func post(t *testing.T, url string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	ctype := "application/json"
	switch v := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case *bytes.Buffer:
		rd = bytes.NewReader(v.Bytes())
	case [2]any:
		rd = bytes.NewReader(v[0].(*bytes.Buffer).Bytes())
		ctype = v[1].(string)
	default:
		b, _ := json.Marshal(v)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest("POST", url, rd)
	req.Header.Set("X-CER-Token", "tok")
	req.Header.Set("Content-Type", ctype)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var m map[string]any
	json.NewDecoder(res.Body).Decode(&m)
	return res.StatusCode, m
}

type stateResp struct {
	Staged []Staged
	Jobs   []JobView
	Batch  string
	Native bool
}

func getState(t *testing.T, url string) stateResp {
	res, err := http.Get(url + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var s stateResp
	json.NewDecoder(res.Body).Decode(&s)
	return s
}

func waitIdle(t *testing.T, s *Server) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !s.q.Busy() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("still busy")
}

func TestEndToEnd(t *testing.T) {
	s, ts, p := newTestServer(t)

	// Protection.
	req, _ := http.NewRequest("POST", ts.URL+"/api/clear", nil)
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 403 {
		t.Fatal("missing token should be 403")
	}
	req, _ = http.NewRequest("GET", ts.URL+"/api/state", nil)
	req.Host = "evil.example:80"
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 403 {
		t.Fatal("foreign host should be 403")
	}
	res, _ := http.Get(ts.URL + "/")
	page := new(bytes.Buffer)
	page.ReadFrom(res.Body)
	if !strings.Contains(page.String(), `"tok"`) || !strings.Contains(page.String(), version) {
		t.Fatal("index template not filled")
	}

	// Step 1: upload → staged, nothing converts yet.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, n := range []string{"Lecture 1.pptx", "Notes.docx", "bad.pptx", "thing.xyz"} {
		w, _ := mw.CreateFormFile("files", n)
		w.Write([]byte("data for " + n))
	}
	mw.Close()
	if code, m := post(t, ts.URL+"/api/upload", [2]any{&body, mw.FormDataContentType()}); code != 200 || m["added"].(float64) != 4 {
		t.Fatalf("upload %d %v", code, m)
	}
	st := getState(t, ts.URL)
	if len(st.Staged) != 4 || len(st.Jobs) != 0 {
		t.Fatalf("staged %d jobs %d", len(st.Staged), len(st.Jobs))
	}
	var xyz string
	for _, sf := range st.Staged {
		if sf.Name == "thing.xyz" && sf.Error == "" {
			t.Fatal("xyz should be flagged before converting")
		}
		if sf.Name == "Lecture 1.pptx" && !sf.Slides {
			t.Fatal("pptx should be marked as slides")
		}
		if sf.Name == "thing.xyz" {
			xyz = sf.ID
		}
	}
	post(t, ts.URL+"/api/unstage/"+xyz, nil)
	if len(getState(t, ts.URL).Staged) != 3 {
		t.Fatal("unstage")
	}

	// Native picker: a folder adds the supported files inside it.
	folder := filepath.Join(t.TempDir(), "Week 5")
	os.MkdirAll(filepath.Join(folder, "sub"), 0o755)
	os.WriteFile(filepath.Join(folder, "a.xlsx"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(folder, "sub", "b.png"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(folder, "ignore.zzz"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(folder, ".DS_Store"), []byte("x"), 0o644)
	if code, _ := post(t, ts.URL+"/api/pick", nil); code != 400 {
		t.Fatal("pick without native should fail")
	}
	s.native = &fakeNative{picked: []string{folder}}
	if _, m := post(t, ts.URL+"/api/pick", nil); m["added"].(float64) != 2 {
		t.Fatalf("pick: %v", m)
	}

	// Step 2: convert with options (prefix + combine + writing space).
	opts := defaultOptions()
	opts.Prefix, opts.Merge, opts.MergeName, opts.NoteSpace, opts.Paper = "BIO ", true, "Week 5", "bottom", "grid"
	code, m := post(t, ts.URL+"/api/convert", opts)
	if code != 200 || m["count"].(float64) != 5 {
		t.Fatalf("convert %d %v", code, m)
	}
	batch := m["batch"].(string)
	if code, _ := post(t, ts.URL+"/api/convert", opts); code != 400 {
		t.Fatal("converting with nothing staged should fail")
	}
	time.Sleep(50 * time.Millisecond)
	waitIdle(t, s)
	time.Sleep(50 * time.Millisecond)
	waitIdle(t, s)

	st = getState(t, ts.URL)
	if st.Batch != batch || len(st.Staged) != 0 {
		t.Fatalf("batch/staged: %+v", st)
	}
	got := map[string]JobView{}
	for _, j := range st.Jobs {
		got[j.Name] = j
	}
	lec := got["Lecture 1.pptx"]
	if lec.Status != StatusDone || lec.OutName != "BIO Lecture 1.pdf" || lec.Pages != 2 {
		t.Fatalf("lecture: %+v", lec)
	}
	if got["bad.pptx"].Status != StatusFailed || !got["bad.pptx"].CanRetry {
		t.Fatalf("bad: %+v", got["bad.pptx"])
	}
	comb := got["Week 5.pdf"]
	if !comb.Combined || comb.Status != StatusDone || comb.Pages != 8 || comb.OutName != "BIO Week 5.pdf" {
		t.Fatalf("combined: %+v", comb)
	}
	if _, err := os.Stat(filepath.Join(p.Inbox, got["bad.pptx"].ID)); err != nil {
		// failed uploads keep their inbox folder for Retry (it's the staged dir)
	}
	// Writing space was applied.
	j, _ := s.q.Get(lec.ID)
	if b := mediaBoxes(t, j.OutPath)[0]; b.Height() != 540*1.8 {
		t.Fatalf("writing space not applied: %+v", b)
	}

	// Step 3: download one, then download all.
	_, d1 := post(t, ts.URL+"/api/download/"+lec.ID, nil)
	if d1["name"] != "BIO Lecture 1.pdf" {
		t.Fatalf("download: %v", d1)
	}
	_, all := post(t, ts.URL+"/api/download-all", map[string]string{"batch": batch})
	if all["saved"].(float64) != 4 { // docx, xlsx, png, combined
		t.Fatalf("download all: %v", all)
	}
	entries, _ := os.ReadDir(os.Getenv("CANTEVENREMARK_DOWNLOADS"))
	if len(entries) != 5 {
		t.Fatalf("downloads folder has %d files", len(entries))
	}

	// Retry, settings, folder chooser, library reload, remove, clear.
	if code, _ := post(t, ts.URL+"/api/retry/"+got["bad.pptx"].ID, nil); code != 200 {
		t.Fatal("retry")
	}
	waitIdle(t, s)
	set := s.settings.Get()
	set.AutoSave, set.Theme = true, "dark"
	if code, _ := post(t, ts.URL+"/api/settings", set); code != 200 || !s.settings.Get().AutoSave {
		t.Fatal("settings")
	}
	bad := set
	bad.Defaults.NoteSpace = "sideways"
	if code, _ := post(t, ts.URL+"/api/settings", bad); code != 400 {
		t.Fatal("bad settings accepted")
	}
	if _, m := post(t, ts.URL+"/api/choose-download-dir", nil); m["downloadDir"] != filepath.Dir(folder) {
		t.Fatalf("choose dir: %v", m)
	}
	if s.settings.Get().Defaults.Prefix != "BIO " {
		t.Fatal("choices should be remembered")
	}
	q2 := NewQueue(context.Background(), p, s.avail, &fakeConverter{t: t}, s.settings, true)
	if j, ok := q2.Get(comb.ID); !ok || !j.Combined || j.SavedTo == "" {
		t.Fatalf("library reload: %+v", j)
	}
	if code, _ := post(t, ts.URL+"/api/remove/"+lec.ID, nil); code != 200 {
		t.Fatal("remove")
	}
	if _, err := os.Stat(filepath.Join(p.Converted, lec.ID)); !os.IsNotExist(err) {
		t.Fatal("converted folder should be gone")
	}
	post(t, ts.URL+"/api/clear", map[string]string{"batch": ""})
	if n := len(getState(t, ts.URL).Jobs); n != 0 {
		t.Fatalf("clear left %d", n)
	}
}

func TestCheckPDF(t *testing.T) {
	dir := t.TempDir()
	good, bad := filepath.Join(dir, "g.pdf"), filepath.Join(dir, "b.pdf")
	os.WriteFile(good, []byte("%PDF-1.4 ..."), 0o644)
	os.WriteFile(bad, []byte("<html>"), 0o644)
	if checkPDF(good) != nil || checkPDF(bad) == nil || checkPDF(filepath.Join(dir, "none")) == nil {
		t.Fatal("checkPDF")
	}
}
