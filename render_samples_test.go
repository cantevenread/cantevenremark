package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRenderSamples(t *testing.T) {
	out := os.Getenv("RENDER_OUT")
	if out == "" {
		t.Skip()
	}
	in := filepath.Join(out, "in.pdf")
	makePDF(t, in, 1, 960, 540)
	for _, c := range []struct{ space, paper string }{{"remarkable", "lined"}, {"remarkable", "grid"}, {"right", "dots"}, {"bottom", "blank"}} {
		o := defaultOptions()
		o.NoteSpace, o.Paper, o.PageNumbers = c.space, c.paper, true
		if err := postProcess(in, filepath.Join(out, c.space+"-"+c.paper+".pdf"), o); err != nil {
			t.Fatal(err)
		}
	}
}
