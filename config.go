package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Paths holds every folder the app uses. Everything lives under
// ~/Library/Application Support/CantEvenRemark so the home folder stays clean.
type Paths struct {
	Base      string // root of app data
	Inbox     string // incoming originals + scratch, one sub-folder per file
	Converted string // finished PDFs, one sub-folder per job
	State     string // settings, library, logs, scripts
	Scripts   string // AppleScript files written at start-up
	Downloads string // default place the Download button saves to
}

func defaultBase() (string, error) {
	if v := os.Getenv("CANTEVENREMARK_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "CantEvenRemark"), nil
}

func newPaths(base string) (Paths, error) {
	home, _ := os.UserHomeDir()
	p := Paths{
		Base:      base,
		Inbox:     filepath.Join(base, "inbox"),
		Converted: filepath.Join(base, "converted"),
		State:     filepath.Join(base, "state"),
		Downloads: filepath.Join(home, "Downloads"),
	}
	if v := os.Getenv("CANTEVENREMARK_DOWNLOADS"); v != "" {
		p.Downloads = v
	}
	p.Scripts = filepath.Join(p.State, "scripts")
	for _, d := range []string{p.Base, p.Inbox, p.Converted, p.State, p.Scripts} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return p, err
		}
	}
	return p, nil
}

// Options control how a batch of files becomes PDFs.
type Options struct {
	// Slides (Keynote)
	Layout  string `json:"layout"`  // slides | notes | handout2 | handout3 | handout4 | handout6
	Builds  bool   `json:"builds"`  // one page per animation step
	Skipped bool   `json:"skipped"` // include hidden/skipped slides

	// Writing space added to every page
	NoteSpace string `json:"noteSpace"` // none | bottom | right | remarkable
	Paper     string `json:"paper"`     // blank | lined | grid | dots

	// Extras
	PageNumbers bool   `json:"pageNumbers"`
	Compress    bool   `json:"compress"`
	Merge       bool   `json:"merge"`
	MergeName   string `json:"mergeName"`
	Prefix      string `json:"prefix"`
}

var (
	validLayouts = map[string]bool{"slides": true, "notes": true, "handout2": true, "handout3": true, "handout4": true, "handout6": true}
	validSpace   = map[string]bool{"none": true, "bottom": true, "right": true, "remarkable": true}
	validPaper   = map[string]bool{"blank": true, "lined": true, "grid": true, "dots": true}
)

func defaultOptions() Options {
	return Options{Layout: "slides", NoteSpace: "none", Paper: "blank", MergeName: "Combined"}
}

func (o Options) normalized() Options {
	if !validLayouts[o.Layout] {
		o.Layout = "slides"
	}
	if !validSpace[o.NoteSpace] {
		o.NoteSpace = "none"
	}
	if !validPaper[o.Paper] {
		o.Paper = "blank"
	}
	o.Prefix = cleanPrefix(o.Prefix)
	o.MergeName = cleanPrefix(strings.TrimSuffix(strings.TrimSpace(o.MergeName), ".pdf"))
	if o.MergeName == "" {
		o.MergeName = "Combined"
	}
	return o
}

func (o Options) validate() error {
	if !validLayouts[o.Layout] || !validSpace[o.NoteSpace] || !validPaper[o.Paper] {
		return errors.New("unknown option value")
	}
	return nil
}

// needsPost reports whether any PDF post-processing step is switched on.
func (o Options) needsPost() bool {
	return o.NoteSpace != "none" || o.PageNumbers || o.Compress
}

// keynoteMode returns the Keynote export style and slides-per-page.
func (o Options) keynoteMode() (string, int) {
	switch o.Layout {
	case "notes":
		return "notes", 1
	case "handout2":
		return "handout", 2
	case "handout3":
		return "handout", 3
	case "handout4":
		return "handout", 4
	case "handout6":
		return "handout", 6
	}
	return "slides", 1
}

func cleanPrefix(s string) string {
	s = unsafeName.ReplaceAllString(s, "-")
	s = strings.TrimLeft(s, ". ")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

// Settings are app-wide preferences.
type Settings struct {
	Defaults    Options  `json:"defaults"`    // last-used / remembered choices
	Remember    bool     `json:"remember"`    // remember choices for next time
	DownloadDir string   `json:"downloadDir"` // "" = ~/Downloads
	AutoSave    bool     `json:"autoSave"`    // save to the download folder as soon as each PDF is ready
	SendTo      string   `json:"sendTo"`      // cloud (Wi-Fi) | usb | mac
	AutoSend    bool     `json:"autoSend"`    // send to the reMarkable as soon as each PDF is ready
	Theme       string   `json:"theme"`       // system | light | dark | eink
	Font        string   `json:"font"`        // normal | hand
	Motion      string   `json:"motion"`      // ghost (e-ink refresh) | smooth
	Splash      bool     `json:"splash"`      // play the start-up animation
	SkipStyle   bool     `json:"skipStyle"`   // quick mode: skip step 2 and use the saved style
	Version     int      `json:"version"`     // settings format, for future upgrades
	Advanced    Advanced `json:"advanced"`    // hidden knobs, only edited by hand if reMarkable changes things
}

type SettingsStore struct {
	mu    sync.Mutex
	path  string
	paths Paths
	s     Settings
}

func loadSettings(p Paths) *SettingsStore {
	st := &SettingsStore{
		path: filepath.Join(p.State, "settings.json"), paths: p,
		s: Settings{Defaults: defaultOptions(), Remember: true, Theme: "system", Font: "normal", Motion: "ghost", SendTo: "cloud", Splash: true},
	}
	if b, err := os.ReadFile(st.path); err == nil {
		s := st.s
		if json.Unmarshal(b, &s) == nil {
			s.Defaults = s.Defaults.normalized()
			st.s = s.clean()
		}
	}
	return st
}

func (st *SettingsStore) Get() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.s
}

func (st *SettingsStore) Set(s Settings) (Settings, error) {
	if err := s.Defaults.validate(); err != nil {
		return st.Get(), err
	}
	s.Defaults = s.Defaults.normalized()
	s = s.clean()
	if s.DownloadDir != "" {
		if fi, err := os.Stat(s.DownloadDir); err != nil || !fi.IsDir() {
			return st.Get(), errors.New("that download folder doesn't exist")
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.s = s
	return s, writeJSONAtomic(st.path, s)
}

// DownloadDir is where Download saves files.
func (st *SettingsStore) DownloadDir() string {
	if d := st.Get().DownloadDir; d != "" {
		return d
	}
	return st.paths.Downloads
}

func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Advanced settings aren't shown in the app. If reMarkable ever moves its
// servers, these can be changed in settings.json without a new version.
type Advanced struct {
	RMAuthHost   string `json:"rmAuthHost,omitempty"`
	RMUploadHost string `json:"rmUploadHost,omitempty"`
	RMUSBAddress string `json:"rmUsbAddress,omitempty"`
}

const settingsVersion = 3

func (s Settings) clean() Settings {
	s.Version = settingsVersion
	switch s.Theme {
	case "light", "dark", "eink", "classic", "y2k", "terminal":
	default:
		s.Theme = "system"
	}
	if s.Font != "hand" {
		s.Font = "normal"
	}
	if s.Motion != "smooth" {
		s.Motion = "ghost"
	}
	if s.SendTo != "usb" && s.SendTo != "mac" {
		s.SendTo = "cloud"
	}
	return s
}
