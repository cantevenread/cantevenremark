package main

import (
	"bytes"
	"fmt"
	"os"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func init() {
	api.DisableConfigDir() // don't create a pdfcpu folder in the user's Library
}

func pdfConf() *model.Configuration {
	c := model.NewDefaultConfiguration()
	c.ValidationMode = model.ValidationRelaxed
	return c
}

// postProcess applies the batch's PDF options to in and writes out.
// Steps run in order: writing space → page numbers → compression.
func postProcess(in, out string, o Options) error {
	cur := in
	tmpFiles := []string{}
	defer func() {
		for _, f := range tmpFiles {
			os.Remove(f)
		}
	}()
	next := func() string {
		p := fmt.Sprintf("%s.step%d.pdf", out, len(tmpFiles))
		tmpFiles = append(tmpFiles, p)
		return p
	}

	if o.NoteSpace != "none" {
		dst := next()
		if err := addWritingSpace(cur, dst, o.NoteSpace, o.Paper); err != nil {
			return fmt.Errorf("adding writing space: %w", err)
		}
		cur = dst
	}
	if o.PageNumbers {
		dst := next()
		desc := "fontname:Helvetica, points:9, position:bc, offset:0 14, scalefactor:1 abs, rotation:0, fillcolor:#8a8a8a"
		if err := api.AddTextWatermarksFile(cur, dst, nil, true, "%p / %P", desc, pdfConf()); err != nil {
			return fmt.Errorf("adding page numbers: %w", err)
		}
		cur = dst
	}
	if o.Compress {
		dst := next()
		if err := api.OptimizeFile(cur, dst, pdfConf()); err != nil {
			return fmt.Errorf("shrinking: %w", err)
		}
		// Only keep the optimised copy if it is actually smaller.
		if a, b := fileSize(cur), fileSize(dst); b > 0 && (a == 0 || b < a) {
			cur = dst
		}
	}
	if cur == in {
		return copyFile(in, out)
	}
	return moveFile(cur, out)
}

func fileSize(p string) int64 {
	if fi, err := os.Stat(p); err == nil {
		return fi.Size()
	}
	return 0
}

// writingArea returns the new page box and the empty area added to it.
// mode: bottom (space under the page), right (space beside it) or
// remarkable (grow the page to the reMarkable's 3:4 portrait screen).
func writingArea(box types.Rectangle, mode string) (newBox, area types.Rectangle, ok bool) {
	w, h := box.Width(), box.Height()
	if w <= 0 || h <= 0 {
		return box, area, false
	}
	bottom := func(extra float64) (types.Rectangle, types.Rectangle, bool) {
		nb := box
		nb.LL.Y -= extra
		return nb, types.Rectangle{LL: types.Point{X: box.LL.X, Y: nb.LL.Y}, UR: types.Point{X: box.UR.X, Y: box.LL.Y}}, true
	}
	right := func(extra float64) (types.Rectangle, types.Rectangle, bool) {
		nb := box
		nb.UR.X += extra
		return nb, types.Rectangle{LL: types.Point{X: box.UR.X, Y: box.LL.Y}, UR: types.Point{X: nb.UR.X, Y: box.UR.Y}}, true
	}
	switch mode {
	case "bottom":
		return bottom(h * 0.8)
	case "right":
		return right(w * 0.6)
	case "remarkable":
		const ratio = 3.0 / 4.0 // width / height of the reMarkable screen held upright
		if w/h > ratio+0.01 {
			return bottom(w/ratio - h)
		}
		if w/h < ratio-0.01 {
			return right(h*ratio - w)
		}
	}
	return box, area, false
}

// paperContent draws the writing area: white fill plus lines, a grid or dots.
func paperContent(area types.Rectangle, paper string, pageW, pageH float64) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "q\n1 g %.2f %.2f %.2f %.2f re f\n", area.LL.X, area.LL.Y, area.Width(), area.Height())
	// Spacing scales with the page so a 16:9 slide and a letter page look alike.
	unit := pageW
	if pageH < unit {
		unit = pageH
	}
	pad := unit * 0.045
	x0, y0, x1, y1 := area.LL.X+pad, area.LL.Y+pad, area.UR.X-pad, area.UR.Y-pad
	if x1-x0 < 10 || y1-y0 < 10 {
		b.WriteString("Q\n")
		return b.Bytes()
	}
	switch paper {
	case "lined":
		step := unit * 0.045
		fmt.Fprintf(&b, "0.80 G %.2f w\n", unit*0.0012)
		for y := y1; y >= y0; y -= step {
			fmt.Fprintf(&b, "%.2f %.2f m %.2f %.2f l\n", x0, y, x1, y)
		}
		b.WriteString("S\n")
	case "grid":
		step := unit * 0.03
		fmt.Fprintf(&b, "0.86 G %.2f w\n", unit*0.001)
		for y := y1; y >= y0-0.01; y -= step {
			fmt.Fprintf(&b, "%.2f %.2f m %.2f %.2f l\n", x0, y, x1, y)
		}
		for x := x0; x <= x1+0.01; x += step {
			fmt.Fprintf(&b, "%.2f %.2f m %.2f %.2f l\n", x, y0, x, y1)
		}
		b.WriteString("S\n")
	case "dots":
		step := unit * 0.035
		fmt.Fprintf(&b, "0.70 G 1 J %.2f w\n", unit*0.004)
		for y := y1; y >= y0; y -= step {
			for x := x0; x <= x1; x += step {
				fmt.Fprintf(&b, "%.2f %.2f m %.2f %.2f l\n", x, y, x, y)
			}
		}
		b.WriteString("S\n")
	}
	b.WriteString("Q\n")
	return b.Bytes()
}

func addWritingSpace(in, out, mode, paper string) error {
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	ctx, err := api.ReadValidateAndOptimize(f, pdfConf())
	if err != nil {
		return err
	}
	xr := ctx.XRefTable
	newStream := func(content []byte) (*types.IndirectRef, error) {
		sd, err := xr.NewStreamDictForBuf(content)
		if err != nil {
			return nil, err
		}
		if err := sd.Encode(); err != nil {
			return nil, err
		}
		return xr.IndRefForNewObject(*sd)
	}

	for p := 1; p <= ctx.PageCount; p++ {
		d, _, inh, err := xr.PageDict(p, false)
		if err != nil {
			return err
		}
		box := pageBox(xr, d, "CropBox", inh.CropBox)
		if box == nil {
			box = pageBox(xr, d, "MediaBox", inh.MediaBox)
		}
		if box == nil {
			continue
		}
		rot := inh.Rotate
		if v, ok := d.Find("Rotate"); ok {
			if o, err := xr.Dereference(v); err == nil {
				if i, ok := o.(types.Integer); ok {
					rot = i.Value()
				}
			}
		}
		if ((rot%360)+360)%180 != 0 {
			continue // sideways pages: leave untouched
		}
		nb, area, ok := writingArea(*box, mode)
		if !ok {
			continue
		}
		d.Update("MediaBox", nb.Array())
		for _, k := range []string{"CropBox", "TrimBox", "BleedBox", "ArtBox"} {
			d.Delete(k)
		}

		pre, err := newStream([]byte("q\n"))
		if err != nil {
			return err
		}
		post, err := newStream(append([]byte("Q\n"), paperContent(area, paper, box.Width(), box.Height())...))
		if err != nil {
			return err
		}
		contents := types.Array{*pre} // pdfcpu wants IndirectRef values, not pointers
		if c, ok := d.Find("Contents"); ok {
			if o, err := xr.Dereference(c); err == nil {
				if arr, isArr := o.(types.Array); isArr {
					contents = append(contents, arr...)
				} else {
					contents = append(contents, c)
				}
			} else {
				contents = append(contents, c)
			}
		}
		contents = append(contents, *post)
		d.Update("Contents", contents)
	}
	return api.WriteContextFile(ctx, out)
}

func pageBox(xr *model.XRefTable, d types.Dict, key string, inherited *types.Rectangle) *types.Rectangle {
	if v, ok := d.Find(key); ok {
		if o, err := xr.Dereference(v); err == nil {
			if arr, ok := o.(types.Array); ok {
				if r := types.RectForArray(arr); r != nil {
					return r
				}
			}
		}
	}
	return inherited
}

// mergePDFs joins finished PDFs into one file.
func mergePDFs(files []string, out string) error {
	if len(files) == 0 {
		return fmt.Errorf("nothing to combine")
	}
	return api.MergeCreateFile(files, out, false, pdfConf())
}

// pageCount returns the number of pages, or 0 if unknown.
func pageCount(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	n, err := api.PageCount(f, pdfConf())
	if err != nil {
		return 0
	}
	return n
}
