package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ---------- AppleScripts (written to disk at start-up, run with osascript) ----------

const scriptKeynoteRich = `on run argv
	set inPath to item 1 of argv
	set outPath to item 2 of argv
	set layoutMode to item 3 of argv
	set perPage to (item 4 of argv) as integer
	set withBuilds to ((item 5 of argv) is "1")
	set withSkipped to ((item 6 of argv) is "1")
	set inFile to POSIX file inPath
	set outFile to POSIX file outPath
	with timeout of 900 seconds
		tell application id "com.apple.iWork.Keynote"
			set theDoc to open inFile
			if theDoc is missing value then set theDoc to front document
			try
				if layoutMode is "notes" then
					export theDoc to outFile as PDF with properties {export style:SlideWithNotes, all stages:withBuilds, skipped slides:withSkipped, PDF image quality:Best}
				else if layoutMode is "handout" then
					export theDoc to outFile as PDF with properties {export style:Handouts, slides per page:perPage, all stages:withBuilds, skipped slides:withSkipped, PDF image quality:Best}
				else
					export theDoc to outFile as PDF with properties {export style:IndividualSlides, all stages:withBuilds, skipped slides:withSkipped, PDF image quality:Best}
				end if
			on error errMsg number errNum
				try
					close theDoc saving no
				end try
				error errMsg number errNum
			end try
			close theDoc saving no
		end tell
	end timeout
end run
`

// Plain export with Keynote's defaults — used if the rich script fails to compile
// on an older/newer Keynote whose dictionary differs.
const scriptKeynoteBasic = `on run argv
	set inFile to POSIX file (item 1 of argv)
	set outFile to POSIX file (item 2 of argv)
	with timeout of 900 seconds
		tell application id "com.apple.iWork.Keynote"
			set theDoc to open inFile
			if theDoc is missing value then set theDoc to front document
			try
				export theDoc to outFile as PDF
			on error errMsg number errNum
				try
					close theDoc saving no
				end try
				error errMsg number errNum
			end try
			close theDoc saving no
		end tell
	end timeout
end run
`

func iworkScript(bundleID string, rich bool) string {
	props := ""
	if rich {
		props = " with properties {image quality:Best}"
	}
	return `on run argv
	set inFile to POSIX file (item 1 of argv)
	set outFile to POSIX file (item 2 of argv)
	with timeout of 900 seconds
		tell application id "` + bundleID + `"
			set theDoc to open inFile
			if theDoc is missing value then set theDoc to front document
			try
				export theDoc to outFile as PDF` + props + `
			on error errMsg number errNum
				try
					close theDoc saving no
				end try
				error errMsg number errNum
			end try
			close theDoc saving no
		end tell
	end timeout
end run
`
}

// Quit an Apple app only if it has no documents open (never touches the user's work).
const scriptQuitIfIdle = `on run argv
	tell application id (item 1 of argv)
		if (count of documents) is 0 then quit
	end tell
end run
`

type scriptSet struct {
	KeynoteRich, KeynoteBasic, PagesRich, PagesBasic, NumbersRich, NumbersBasic, QuitIfIdle string
}

func writeScripts(dir string) (scriptSet, error) {
	files := map[string]string{
		"keynote-rich.applescript":  scriptKeynoteRich,
		"keynote-basic.applescript": scriptKeynoteBasic,
		"pages-rich.applescript":    iworkScript(KindPages.bundleID(), true),
		"pages-basic.applescript":   iworkScript(KindPages.bundleID(), false),
		"numbers-rich.applescript":  iworkScript(KindNumbers.bundleID(), true),
		"numbers-basic.applescript": iworkScript(KindNumbers.bundleID(), false),
		"quit-if-idle.applescript":  scriptQuitIfIdle,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			return scriptSet{}, err
		}
	}
	j := func(n string) string { return filepath.Join(dir, n) }
	return scriptSet{
		KeynoteRich: j("keynote-rich.applescript"), KeynoteBasic: j("keynote-basic.applescript"),
		PagesRich: j("pages-rich.applescript"), PagesBasic: j("pages-basic.applescript"),
		NumbersRich: j("numbers-rich.applescript"), NumbersBasic: j("numbers-basic.applescript"),
		QuitIfIdle: j("quit-if-idle.applescript"),
	}, nil
}

// ---------- osascript helpers ----------

type osaError struct {
	Code int
	Msg  string
	App  string
}

func (e *osaError) Error() string {
	switch e.Code {
	case -1743:
		return fmt.Sprintf("macOS blocked CantEvenRemark from using %s. Allow it in System Settings → Privacy & Security → Automation, then press Retry.", e.App)
	case -1712:
		return fmt.Sprintf("%s took too long to respond.", e.App)
	case -128:
		return "Cancelled."
	}
	if e.Msg == "" {
		return fmt.Sprintf("%s couldn't convert this file.", e.App)
	}
	return fmt.Sprintf("%s: %s", e.App, e.Msg)
}

var osaCodeRe = regexp.MustCompile(`\((-?\d+)\)\s*$`)
var osaPrefixRe = regexp.MustCompile(`^.*?execution error:\s*`)

func parseOsaError(stderr, app string) *osaError {
	s := strings.TrimSpace(stderr)
	e := &osaError{App: app}
	if m := osaCodeRe.FindStringSubmatch(s); m != nil {
		e.Code, _ = strconv.Atoi(m[1])
		s = strings.TrimSpace(osaCodeRe.ReplaceAllString(s, ""))
	}
	s = osaPrefixRe.ReplaceAllString(s, "")
	for _, pre := range []string{app + " got an error: ", "Keynote got an error: ", "Pages got an error: ", "Numbers got an error: "} {
		s = strings.TrimPrefix(s, pre)
	}
	if strings.Contains(s, "syntax error") {
		e.Code = -2741
	}
	e.Msg = strings.TrimSuffix(strings.TrimSpace(s), ".")
	return e
}

func isAuthErr(err error) bool {
	var oe *osaError
	return errors.As(err, &oe) && oe.Code == -1743
}

func runOsa(ctx context.Context, app, script string, args ...string) error {
	cmd := exec.CommandContext(ctx, "osascript", append([]string{script}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return &osaError{Code: -1712, App: app}
		}
		return parseOsaError(stderr.String(), app)
	}
	return nil
}

func appIsRunning(bundleID string) bool {
	out, err := exec.Command("osascript", "-e", `application id "`+bundleID+`" is running`).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// ---------- the converter ----------

type Converter interface {
	Convert(ctx context.Context, j *Job) (string, error)
	AppIdle(k Kind) // called when an app's queue has been empty for a while
}

type realConverter struct {
	paths   Paths
	avail   Avail
	scripts scriptSet
	ours    map[Kind]bool // apps CantEvenRemark launched itself
	mu      chanMutex
}

type chanMutex chan struct{}

func (m chanMutex) Lock()   { m <- struct{}{} }
func (m chanMutex) Unlock() { <-m }

func newRealConverter(p Paths, a Avail) (*realConverter, error) {
	s, err := writeScripts(p.Scripts)
	if err != nil {
		return nil, err
	}
	return &realConverter{paths: p, avail: a, scripts: s, ours: map[Kind]bool{}, mu: make(chanMutex, 1)}, nil
}

func (rc *realConverter) noteLaunch(k Kind) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if _, seen := rc.ours[k]; !seen {
		rc.ours[k] = !appIsRunning(k.bundleID())
	}
}

func (rc *realConverter) AppIdle(k Kind) {
	rc.mu.Lock()
	ours, seen := rc.ours[k]
	delete(rc.ours, k)
	rc.mu.Unlock()
	if seen && ours {
		_ = runOsa(context.Background(), k.Label(), rc.scripts.QuitIfIdle, k.bundleID())
	}
}

func (rc *realConverter) Convert(ctx context.Context, j *Job) (string, error) {
	src := j.Src
	stem := strings.TrimSuffix(filepath.Base(j.Name), filepath.Ext(j.Name))
	switch j.Plan.Pre {
	case preTextutil:
		dst := filepath.Join(j.WorkDir, stem+".docx")
		if out, err := exec.CommandContext(ctx, "textutil", "-convert", "docx", "-output", dst, src).CombinedOutput(); err != nil {
			return "", fmt.Errorf("couldn't read this file (%s)", strings.TrimSpace(string(out)))
		}
		src = dst
	case preAsText:
		dst := filepath.Join(j.WorkDir, stem+".txt")
		if err := copyFile(src, dst); err != nil {
			return "", err
		}
		src = dst
	}

	out := filepath.Join(j.WorkDir, "out.pdf")
	_ = os.Remove(out)

	var err error
	switch j.Plan.Kind {
	case KindPDF:
		return src, checkPDF(src)
	case KindKeynote:
		rc.noteLaunch(KindKeynote)
		err = rc.exportWithFallback(ctx, KindKeynote, j.ID, out, func(target string) error {
			mode, per := j.Opts.keynoteMode()
			e := runOsa(ctx, "Keynote", rc.scripts.KeynoteRich, src, target, mode, strconv.Itoa(per), b01(j.Opts.Builds), b01(j.Opts.Skipped))
			if e == nil || isAuthErr(e) || ctx.Err() != nil {
				return e
			}
			_ = os.Remove(target)
			if e2 := runOsa(ctx, "Keynote", rc.scripts.KeynoteBasic, src, target); e2 != nil {
				return e
			}
			return nil
		})
	case KindPages, KindNumbers:
		rich, basic := rc.scripts.PagesRich, rc.scripts.PagesBasic
		if j.Plan.Kind == KindNumbers {
			rich, basic = rc.scripts.NumbersRich, rc.scripts.NumbersBasic
		}
		rc.noteLaunch(j.Plan.Kind)
		app := j.Plan.Kind.Label()
		err = rc.exportWithFallback(ctx, j.Plan.Kind, j.ID, out, func(target string) error {
			e := runOsa(ctx, app, rich, src, target)
			if e == nil || isAuthErr(e) || ctx.Err() != nil {
				return e
			}
			_ = os.Remove(target)
			if e2 := runOsa(ctx, app, basic, src, target); e2 != nil {
				return e
			}
			return nil
		})
	case KindImage:
		if b, e := exec.CommandContext(ctx, "sips", "-s", "format", "pdf", src, "--out", out).CombinedOutput(); e != nil {
			err = fmt.Errorf("couldn't read this image (%s)", lastLine(string(b)))
		}
	case KindLibreOffice:
		err = rc.libreoffice(ctx, src, j.WorkDir, out)
	default:
		err = fmt.Errorf("no converter for this file")
	}
	if err != nil {
		return "", err
	}
	return out, checkPDF(out)
}

// exportWithFallback exports to the work folder; if a sandboxed Apple app refuses
// to write there, it retries inside the app's own container and moves the file out.
func (rc *realConverter) exportWithFallback(ctx context.Context, k Kind, id, out string, export func(string) error) error {
	err := export(out)
	if err == nil {
		if _, statErr := os.Stat(out); statErr == nil {
			return nil
		}
	}
	if isAuthErr(err) || ctx.Err() != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	box := filepath.Join(home, "Library", "Containers", k.bundleID(), "Data", "tmp")
	if st, e := os.Stat(box); e != nil || !st.IsDir() {
		if err == nil {
			return fmt.Errorf("%s finished but no PDF was written", k.Label())
		}
		return err
	}
	alt := filepath.Join(box, "cantevenremark-"+id+".pdf")
	_ = os.Remove(alt)
	if e := export(alt); e != nil {
		if err != nil {
			return err
		}
		return e
	}
	defer os.Remove(alt)
	return moveFile(alt, out)
}

func (rc *realConverter) libreoffice(ctx context.Context, src, work, out string) error {
	outDir := filepath.Join(work, "lo-out")
	_ = os.MkdirAll(outDir, 0o755)
	profile := (&url.URL{Scheme: "file", Path: filepath.Join(rc.paths.State, "lo-profile")}).String()
	cmd := exec.CommandContext(ctx, rc.avail.SofficePath, "--headless", "--norestore", "--nologo",
		"-env:UserInstallation="+profile, "--convert-to", "pdf", "--outdir", outDir, src)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("LibreOffice couldn't convert this file (%s)", lastLine(string(b)))
	}
	stem := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	return moveFile(filepath.Join(outDir, stem+".pdf"), out)
}

// ---------- file helpers ----------

func b01(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func checkPDF(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("no PDF was produced")
	}
	defer f.Close()
	head := make([]byte, 1024)
	n, _ := io.ReadFull(f, head)
	if !bytes.Contains(head[:n], []byte("%PDF-")) {
		return errors.New("the result wasn't a valid PDF")
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// uniquePath returns dir/name, or "name (2).ext", "name (3).ext"… if taken.
func uniquePath(dir, name string) string {
	p := filepath.Join(dir, name)
	if _, err := os.Lstat(p); os.IsNotExist(err) {
		return p
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			return p
		}
	}
}

var unsafeName = regexp.MustCompile(`[/\\:\x00-\x1f]`)

// cleanName keeps a browser-supplied filename safe to use on disk.
func cleanName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(unsafeName.ReplaceAllString(name, "-"))
	name = strings.TrimLeft(name, ".")
	if name == "" {
		name = "file"
	}
	if len(name) > 200 {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		name = name[:200-len(ext)] + ext
	}
	return name
}

func pdfName(original string) string {
	return strings.TrimSuffix(original, filepath.Ext(original)) + ".pdf"
}
