package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	figmaextractor "github.com/hellenic-development/figma-extractor"
)

// These tests read a real Figma file. They are skipped without a token and
// spend about five of Figma's most expensive requests when they run, so
// they are not part of `go test ./...` (the folder's name starts with an
// underscore).
//
// Run with:
//
//	FIGMA_TOKEN=<your-token> go test -v ./_examples/basic/
//
// The file is a copy of Figma's "Mobile Apps – Prototyping Kit". To run
// against your own, set FIGMA_URL to a link to one frame.
const (
	kitFile  = "https://www.figma.com/design/rrjFDZ1mXkjC147DGMDtFU/Mobile-Apps-%E2%80%93-Prototyping-Kit--Community-"
	kitFrame = kitFile + "?node-id=294-6186" // the small "Credits" frame of the About page
)

func token(t *testing.T) string {
	t.Helper()
	tok := os.Getenv("FIGMA_TOKEN")
	if tok == "" {
		t.Skip("FIGMA_TOKEN not set, skipping the live test")
	}
	return tok
}

func frameLink() string {
	if link := os.Getenv("FIGMA_URL"); link != "" {
		return link
	}
	return kitFrame
}

func context2min(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// TestOutline lists the pages and frames of the file: one request.
func TestOutline(t *testing.T) {
	link, _, _ := strings.Cut(frameLink(), "?")
	res, err := figmaextractor.Run(context2min(t), figmaextractor.Options{
		AccessToken: token(t), FileURL: link, Outline: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Requests.Tier1 != 1 {
		t.Errorf("the outline cost %d Tier 1 requests, want 1", res.Requests.Tier1)
	}
	if len(res.Outline.Pages) == 0 || !strings.Contains(res.Markdown, "## Page ") {
		t.Errorf("the outline lists no pages")
	}
	t.Logf("%s: %d pages, %d bytes of markdown", res.FileName, len(res.Outline.Pages), len(res.Markdown))
}

// TestFrame extracts one frame with its tree and its pictures.
func TestFrame(t *testing.T) {
	dir := t.TempDir()
	if keep := os.Getenv("FIGMA_OUT"); keep != "" {
		dir = keep // keep the result to look at
	}
	res, err := figmaextractor.Run(context2min(t), figmaextractor.Options{
		AccessToken:   token(t),
		FileURL:       frameLink(),
		ComponentTree: true,
		ExportImages:  true,
		ImageDir:      filepath.Join(dir, "figma-assets"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "design.md"), []byte(res.Markdown), 0o644); err != nil {
		t.Fatal(err)
	}

	d := res.Design
	t.Logf("%s: %d frame(s), %d layers, %d hidden skipped, %d icons collapsed", res.FileName, len(d.Frames), d.Stats.Layers, d.Stats.Hidden, d.Stats.Icons)
	t.Logf("tokens: %d named styles, %d colours, %d text styles; %d components", len(d.Tokens.Styles), len(d.Tokens.Colors), len(d.Tokens.Text), len(res.Design.Components))
	t.Logf("%d files in %s, requests %+v, %d warnings", len(res.Assets), res.AssetDir, res.Requests, len(res.Warnings))
	for _, w := range res.Warnings {
		t.Logf("warning: %s", w)
	}

	if len(d.Frames) == 0 || d.Stats.Layers == 0 {
		t.Fatalf("nothing was extracted")
	}
	// A frame with pictures is the nodes, then one render request per image
	// format in use. More than that means requests are being wasted.
	if res.Requests.Tier1 > 3 {
		t.Errorf("the frame cost %d Tier 1 requests, want at most 3", res.Requests.Tier1)
	}
	if len(d.Tokens.Colors) == 0 {
		t.Errorf("no colours were extracted: fills are being dropped")
	}
	if d.Frames[0].Screenshot == "" {
		t.Errorf("the frame has no screenshot")
	}
	for _, a := range res.Assets {
		if info, err := os.Stat(filepath.FromSlash(a.Path)); err != nil || info.Size() == 0 {
			t.Errorf("asset %s is missing or empty on disk", a.File)
		}
	}
}
