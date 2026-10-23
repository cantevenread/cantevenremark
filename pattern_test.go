package main

import (
	"path/filepath"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// The writing-space pattern must survive pdfcpu's writer (regression: pointer refs were dropped).
func TestWritingSpaceKeepsPattern(t *testing.T) {
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in.pdf"), filepath.Join(dir, "out.pdf")
	makePDF(t, in, 2, 960, 540)
	if err := addWritingSpace(in, out, "remarkable", "grid"); err != nil {
		t.Fatal(err)
	}
	ctx, err := api.ReadContextFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for p := 1; p <= 2; p++ {
		d, _, _, _ := ctx.XRefTable.PageDict(p, false)
		c, _ := d.Find("Contents")
		arr, ok := c.(types.Array)
		if !ok || len(arr) != 3 {
			t.Fatalf("page %d contents %v", p, c)
		}
	}
	if fileSize(out) < 1500 {
		t.Fatalf("pattern missing, size %d", fileSize(out))
	}
}
