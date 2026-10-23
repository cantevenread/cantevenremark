package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Kind is the engine that turns a file into a PDF.
type Kind string

const (
	KindKeynote     Kind = "keynote"
	KindPages       Kind = "pages"
	KindNumbers     Kind = "numbers"
	KindImage       Kind = "image"
	KindPDF         Kind = "pdf"
	KindLibreOffice Kind = "libreoffice"
)

func (k Kind) Label() string {
	switch k {
	case KindKeynote:
		return "Keynote"
	case KindPages:
		return "Pages"
	case KindNumbers:
		return "Numbers"
	case KindImage:
		return "Image"
	case KindPDF:
		return "PDF"
	case KindLibreOffice:
		return "LibreOffice"
	}
	return string(k)
}

func (k Kind) bundleID() string {
	switch k {
	case KindKeynote:
		return "com.apple.iWork.Keynote"
	case KindPages:
		return "com.apple.iWork.Pages"
	case KindNumbers:
		return "com.apple.iWork.Numbers"
	}
	return ""
}

// Pre-processing steps done before the main engine runs.
const (
	preNone     = ""
	preTextutil = "textutil" // macOS textutil -> .docx, then Pages
	preAsText   = "astxt"    // copy as .txt (Markdown), then Pages
)

type plan struct {
	Kind Kind
	Pre  string
}

var extTable = map[string]plan{
	// Slides -> Keynote
	".pptx": {KindKeynote, preNone}, ".ppt": {KindKeynote, preNone}, ".pptm": {KindKeynote, preNone},
	".ppsx": {KindKeynote, preNone}, ".pps": {KindKeynote, preNone}, ".potx": {KindKeynote, preNone},
	".key": {KindKeynote, preNone},
	// Documents -> Pages
	".docx": {KindPages, preNone}, ".doc": {KindPages, preNone}, ".docm": {KindPages, preNone},
	".dotx": {KindPages, preNone}, ".pages": {KindPages, preNone}, ".rtf": {KindPages, preNone},
	".txt": {KindPages, preNone},
	".md":  {KindPages, preAsText}, ".markdown": {KindPages, preAsText},
	".html": {KindPages, preTextutil}, ".htm": {KindPages, preTextutil},
	".webarchive": {KindPages, preTextutil}, ".odt": {KindPages, preTextutil},
	// Spreadsheets -> Numbers
	".xlsx": {KindNumbers, preNone}, ".xls": {KindNumbers, preNone}, ".xlsm": {KindNumbers, preNone},
	".csv": {KindNumbers, preNone}, ".tsv": {KindNumbers, preNone}, ".numbers": {KindNumbers, preNone},
	// Images -> sips
	".png": {KindImage, preNone}, ".jpg": {KindImage, preNone}, ".jpeg": {KindImage, preNone},
	".heic": {KindImage, preNone}, ".heif": {KindImage, preNone}, ".tif": {KindImage, preNone},
	".tiff": {KindImage, preNone}, ".gif": {KindImage, preNone}, ".bmp": {KindImage, preNone},
	".webp": {KindImage, preNone},
	// Already PDF
	".pdf": {KindPDF, preNone},
	// Only LibreOffice can do these
	".odp": {KindLibreOffice, preNone}, ".ods": {KindLibreOffice, preNone}, ".odg": {KindLibreOffice, preNone},
}

// Avail records which converters exist on this Mac.
type Avail struct {
	Keynote     bool   `json:"keynote"`
	Pages       bool   `json:"pages"`
	Numbers     bool   `json:"numbers"`
	LibreOffice bool   `json:"libreoffice"`
	Images      bool   `json:"images"`
	SofficePath string `json:"-"`
}

func (a Avail) has(k Kind) bool {
	switch k {
	case KindKeynote:
		return a.Keynote
	case KindPages:
		return a.Pages
	case KindNumbers:
		return a.Numbers
	case KindLibreOffice:
		return a.LibreOffice
	case KindImage:
		return a.Images
	case KindPDF:
		return true
	}
	return false
}

func findApp(name string) string {
	home, _ := os.UserHomeDir()
	for _, dir := range []string{"/Applications", filepath.Join(home, "Applications"), "/System/Applications"} {
		p := filepath.Join(dir, name+".app")
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return ""
}

func detectAvail() Avail {
	a := Avail{
		Keynote: findApp("Keynote") != "",
		Pages:   findApp("Pages") != "",
		Numbers: findApp("Numbers") != "",
	}
	if _, err := os.Stat("/usr/bin/sips"); err == nil {
		a.Images = true
	}
	if lo := findApp("LibreOffice"); lo != "" {
		p := filepath.Join(lo, "Contents", "MacOS", "soffice")
		if _, err := os.Stat(p); err == nil {
			a.LibreOffice, a.SofficePath = true, p
		}
	}
	return a
}

var needsHint = map[Kind]string{
	KindKeynote:     "Keynote (free on the Mac App Store)",
	KindPages:       "Pages (free on the Mac App Store)",
	KindNumbers:     "Numbers (free on the Mac App Store)",
	KindLibreOffice: "LibreOffice (free at libreoffice.org)",
	KindImage:       "macOS sips",
}

// planFor picks the engine for a file, falling back to LibreOffice when the
// Apple app is missing.
func planFor(name string, a Avail) (plan, error) {
	ext := strings.ToLower(filepath.Ext(name))
	p, ok := extTable[ext]
	if !ok {
		if a.LibreOffice && ext != "" {
			return plan{KindLibreOffice, preNone}, nil
		}
		if ext == "" {
			return plan{}, fmt.Errorf("file has no extension, so its type is unknown")
		}
		return plan{}, fmt.Errorf("%s files aren't supported yet", ext)
	}
	if a.has(p.Kind) {
		return p, nil
	}
	if a.LibreOffice {
		return plan{KindLibreOffice, preNone}, nil
	}
	return plan{}, fmt.Errorf("%s files need %s", ext, needsHint[p.Kind])
}

func supportedExts() []string {
	out := make([]string, 0, len(extTable))
	for e := range extTable {
		out = append(out, e)
	}
	return out
}
