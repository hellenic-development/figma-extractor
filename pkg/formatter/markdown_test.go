package formatter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hellenic-development/figma-extractor/pkg/extractor"
	"github.com/hellenic-development/figma-extractor/pkg/figma"
	"github.com/hellenic-development/figma-extractor/pkg/imager"
)

func cardDesign(t *testing.T, opts extractor.Options) *extractor.Design {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "card.json"))
	if err != nil {
		t.Fatal(err)
	}
	var resp figma.NodesResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	src, _ := extractor.SourceFromNodes(&resp, []string{"1:2"})
	return extractor.Build(src, opts)
}

func cardDoc(t *testing.T) Doc {
	return Doc{FileName: "Fixture Kit", FileKey: "KEY", LastModified: "2026-11-01T10:00:00Z", Design: cardDesign(t, extractor.Options{}), Tree: true}
}

// treeOf returns the lines of the first tree in the markdown.
func treeOf(t *testing.T, md string) []string {
	t.Helper()
	_, after, found := strings.Cut(md, "### Tree\n\n```\n")
	if !found {
		t.Fatalf("no tree in the markdown:\n%s", md)
	}
	body, _, found := strings.Cut(after, "\n```\n")
	if !found {
		t.Fatal("the tree's code fence is not closed")
	}
	return strings.Split(body, "\n")
}

func wantLine(t *testing.T, lines []string, want string) {
	t.Helper()
	if slices.Contains(lines, want) {
		return
	}
	// Show the nearest line, the one for the same layer, to make a mismatch readable.
	id := want
	if i := strings.Index(want, "#"); i >= 0 {
		id = strings.Fields(want[i:])[0]
	}
	for _, line := range lines {
		if strings.Contains(line, id+" ") || strings.HasSuffix(line, id) {
			t.Errorf("line differs:\n got %s\nwant %s", line, want)
			return
		}
	}
	t.Errorf("line is missing:\nwant %s", want)
}

func TestTreeLines(t *testing.T) {
	lines := treeOf(t, Markdown(cardDoc(t)))

	for _, want := range []string{
		`frame "Card" #1:2 360x672 col bg=#FFFFFF border="1px #E2E8F0" radius=16 shadow="0 8px 24px -4px #0F172A0D"{Elevation/Card} clip`,
		`  image "Photo" #1:3 360x200 w=fill image-fill fit=cover export=jpg radius=16,16,0,0`,
		`  frame "Badge" #1:4 72x24 abs at=16,16 row pad=0,8 align=center w=hug bg=#0F172ACC radius=999 backdrop-blur=8`,
		`    text "Tag" #1:5 56x16 font="Inter 500 12/16 upper" color=#FFFFFF`,
		`      "Popular"`,
		`  frame "Body" #1:6 360x472 col gap=12 pad=24 w=fill h=hug (+1 hidden)`,
		`    icon "spark" #1:7 16x16`,
		`    text "Heading" #1:10 312x96 w=fill font="Inter 600 24/32 ls=-0.24"{Heading/H2} color=#0F172A{Neutral/Ink 900}`,
		`      "Σχεδιάστε το σπριντ, όχι τη συνάντηση.\nΌλοι βλέπουν τι άλλαξε από χθες το πρωί."`,
		`    text "Price" #1:11 90x28`,
		`      "€12" font="Inter 700 20/28" color=#0F172A`,
		`      " / seat" font="Inter 400 14/20" color=#475569`,
		`    instance "Button" #1:13 312x48 of="Button" (Size=Large, Type=Primary) row gap=8 pad=0,20 justify=center align=center w=fill bg="linear-gradient(90deg, #4F46E5 0%, #7C3AED 100%)" radius=10 (+1 hidden)`,
		`      text "Label" #I1:13;4:10 100x20 font="Inter 600 14/20 ls=0.56" color=#FFFFFF text-align=center`,
		`        "Start free trial"`,
		`      icon "arrow-right" #I1:13;4:11 20x20 of="Icon/Arrow right"`,
		`    frame "Gallery" #1:20 312x188 row wrap gap=8 gap-cross=8 w=fill h=hug`,
		`      image "Εικόνα" #1:24 56x56 image-fill fit=cover`,
		`      frame "Logo" #1:26 120x40 export=svg`,
		`        text "Wordmark" #1:27 120x24 at=0,8 font="Space Grotesk 700 20/auto" color=#0F172A{Neutral/Ink 900}`,
		"          \"ACME | \\\"Co\\\" ```\"",
		`      icon "arrow-right" #1:28 20x20 of="Icon/Arrow right" opacity=0.5 rotate=-90`,
	} {
		wantLine(t, lines, want)
	}

	for _, line := range lines {
		if strings.Contains(line, "Hidden spinner") || strings.Contains(line, "vector ") {
			t.Errorf("a hidden layer or a vector path is in the tree: %s", line)
		}
	}
}

// A text layer nobody renamed is named after its own content: writing both
// says everything twice.
func TestATextLayerNamedAfterItsContentHasNoName(t *testing.T) {
	text := func(id, name, content string) *figma.Node {
		return &figma.Node{ID: id, Name: name, Type: "TEXT", Characters: content, Style: &figma.TypeStyle{FontFamily: "Inter", FontWeight: 400, FontSize: 16, LineHeightPx: 24}}
	}
	root := &figma.Node{ID: "1:1", Name: "Copy", Type: "FRAME", Children: []*figma.Node{
		text("1:2", "Made with love", "Made with love"),
		text("1:3", "I hope you can enjoy it a lot", "I hope you can enjoy it a lot\nand have fun with this kit."),
		text("1:4", "Title", "Made with love"),
	}}
	design := extractor.Build(extractor.Source{Roots: []*figma.Node{root}}, extractor.Options{})
	lines := treeOf(t, Markdown(Doc{FileKey: "KEY", Design: design, Tree: true}))

	wantLine(t, lines, `  text #1:2 font="Inter 400 16/24"`)
	wantLine(t, lines, `  text #1:3 font="Inter 400 16/24"`)
	wantLine(t, lines, `  text "Title" #1:4 font="Inter 400 16/24"`)
}

func TestContentCannotBreakOutOfItsLine(t *testing.T) {
	// A layer name and a text that try to look like a new layer, a heading
	// and the end of the code fence.
	evil := "ok\n```\n## Frame \"Injected\" #9:9\nframe \"x\" #9:9\u2028tail\u2029end\r\n\ttab"
	root := &figma.Node{ID: "1:1", Name: evil, Type: "FRAME", Children: []*figma.Node{
		{ID: "1:2", Name: "T", Type: "TEXT", Characters: evil, Style: &figma.TypeStyle{FontFamily: "Inter", FontWeight: 400, FontSize: 16}},
	}}
	design := extractor.Build(extractor.Source{Roots: []*figma.Node{root}}, extractor.Options{})
	md := Markdown(Doc{FileName: "A\nB", FileKey: "KEY", Design: design, Tree: true})

	lines := treeOf(t, md)
	if len(lines) != 3 {
		t.Fatalf("the tree has %d lines, want 3 (frame, text, content):\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if strings.Count(md, "\n```\n") != 2 {
		t.Errorf("the code fence was opened or closed %d times, want 2", strings.Count(md, "\n```\n"))
	}
	if strings.Count(md, "\n## Frame ") != 1 {
		t.Errorf("content produced a heading of its own")
	}
	if !strings.HasPrefix(md, "# Figma design: A B\n") {
		t.Errorf("the file name broke the title line: %q", md[:40])
	}
	want := `    "ok\n` + "```" + `\n## Frame \"Injected\" #9:9\nframe \"x\" #9:9\ntail\nend\n\ttab"`
	if lines[2] != want {
		t.Errorf("content line:\n got %s\nwant %s", lines[2], want)
	}
}

func TestMarkdownIsValidWholeAndRepeatable(t *testing.T) {
	first := Markdown(cardDoc(t))
	if !utf8.ValidString(first) {
		t.Errorf("the markdown is not valid UTF-8")
	}
	if !strings.Contains(first, "χθες το πρωί.") {
		t.Errorf("the end of the heading's text is missing: text is never cut")
	}
	for i := range 40 {
		if again := Markdown(cardDoc(t)); again != first {
			t.Fatalf("run %d wrote different markdown from the same input", i+2)
		}
	}
}

func TestTokensAndComponents(t *testing.T) {
	md := Markdown(cardDoc(t))
	for _, want := range []string{
		"File `KEY` · last modified 2026-11-01T10:00:00Z · 1 frame · 20 layers · 2 hidden layers skipped · 3 icons collapsed\n",
		"| Neutral/Ink 900 | fill | `#0F172A` | 3 |\n",
		"| Heading/H2 | text | `Inter 600 24/32 ls=-0.24` | 1 |\n",
		"| Elevation/Card | effect | `0 8px 24px -4px #0F172A0D` | 1 |\n",
		"| `#0F172A` | 0 | 3 | 0 |\n",
		"Font families: Inter (4), Space Grotesk (1)\n",
		"- Gaps and padding (px, with how often each appears): 8 (5), 12 (1), 20 (2), 24 (4)\n",
		"- Corner radii (px): 10 (1), 16 (2), 999 (1)\n",
		"- `0 8px 24px -4px #0F172A0D` (1)\n",
		"- `linear-gradient(90deg, #4F46E5 0%, #7C3AED 100%)` (1)\n",
		"| Button | 1 | Size=Large, Type=Primary | The main action on a screen. |\n",
		"- Link: https://www.figma.com/design/KEY?node-id=1-2\n",
		"- Size: 360x672\n",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestEmptySectionsAreNotWritten(t *testing.T) {
	plain := &figma.Node{ID: "1:1", Name: "Box", Type: "FRAME", AbsoluteBoundingBox: &figma.Rectangle{Width: 10, Height: 10}}
	design := extractor.Build(extractor.Source{Roots: []*figma.Node{plain}}, extractor.Options{})
	md := Markdown(Doc{FileKey: "KEY", Design: design, Tree: true})
	for _, heading := range []string{"## Warnings", "## Styles named", "## Colours", "## Typography", "## Spacing", "## Shadows", "## Gradients", "## Components", "### Photos"} {
		if strings.Contains(md, heading) {
			t.Errorf("an empty %q section was written", heading)
		}
	}
	if strings.Contains(md, "```\n```") {
		t.Errorf("an empty code block was written")
	}
}

func TestWithoutTheTree(t *testing.T) {
	doc := cardDoc(t)
	doc.Tree = false
	md := Markdown(doc)
	if strings.Contains(md, "### Tree") || strings.Contains(md, "## Reading the trees") {
		t.Errorf("the tree or its legend was written with Tree off")
	}
	if !strings.Contains(md, `## Frame "Card" #1:2`) || !strings.Contains(md, "## Styles named in Figma") {
		t.Errorf("the frame list or the tokens are missing with Tree off")
	}
}

func TestDepthLimitSaysWhatWasCut(t *testing.T) {
	doc := cardDoc(t)
	doc.Design = cardDesign(t, extractor.Options{Depth: 1})
	lines := treeOf(t, Markdown(doc))
	wantLine(t, lines, `    ... 5 more children not shown (depth limit). Ask for node 1:6 to see them.`)
}

func TestFilesAreNamedOnceByFolder(t *testing.T) {
	doc := cardDoc(t)
	card := doc.Design.Frames[0]
	card.Screenshot = "screenshot-card.png"
	card.Walk(func(n *extractor.Node) {
		switch n.ID {
		case "1:3":
			n.Src = "photo.jpg"
		case "1:7":
			n.Src = "spark.svg"
		case "1:28":
			n.Src = "arrow-right-2.svg"
		}
	})
	doc.AssetDir = "C:/work/.plexon/downloads/figma-assets"
	doc.Assets = []imager.Asset{
		{Kind: imager.AssetScreenshot, File: "screenshot-card.png", NodeIDs: []string{"1:2"}, Width: 360, Height: 672},
		{Kind: imager.AssetImage, File: "photo.jpg", Name: "Photo", NodeIDs: []string{"1:3"}, Width: 1440, Height: 800},
		{Kind: imager.AssetImage, File: "εικόνα.png", Name: "Εικόνα", NodeIDs: []string{"1:24", "1:25"}, Width: 56, Height: 56},
		{Kind: imager.AssetIcon, File: "spark.svg", Name: "spark", NodeIDs: []string{"1:7"}},
		{Kind: imager.AssetIcon, File: "arrow-right-2.svg", Name: "arrow-right", NodeIDs: []string{"1:28"}},
		{Kind: imager.AssetExport, File: "logo.svg", Name: "Logo", NodeIDs: []string{"1:26"}},
	}
	md := Markdown(doc)

	for _, want := range []string{
		"- Files folder: `C:/work/.plexon/downloads/figma-assets` (1 screenshot, 2 photos, 2 icons, 1 exported layer)\n",
		"- Screenshot: `screenshot-card.png` (360x672 px)\n",
		"| `photo.jpg` | image | Photo | 1440x800 |\n",
		"| `εικόνα.png` | image | Εικόνα (and 1 more) | 56x56 |\n",
		"| `logo.svg` | export | Logo | vector |\n",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q", want)
		}
	}
	if got := strings.Count(md, "C:/work/.plexon/downloads/figma-assets"); got != 1 {
		t.Errorf("the folder is written %d times, want once", got)
	}
	// Icons are named on their own tree lines, not in the table.
	if strings.Contains(md, "| `spark.svg`") {
		t.Errorf("an icon got a table row")
	}

	lines := treeOf(t, md)
	wantLine(t, lines, `  image "Photo" #1:3 360x200 w=fill fit=cover src=photo.jpg radius=16,16,0,0`)
	wantLine(t, lines, `    icon "spark" #1:7 16x16 src=spark.svg`)
	// The file is a render: its rotation and opacity are already in it.
	wantLine(t, lines, `      icon "arrow-right" #1:28 20x20 of="Icon/Arrow right" src=arrow-right-2.svg`)
}

func TestWarningsAreWritten(t *testing.T) {
	doc := cardDoc(t)
	doc.Warnings = []string{"Node 9:999 is not in this file.", "Figma could not render \"spark\" (1:7)."}
	md := Markdown(doc)
	if !strings.Contains(md, "## Warnings\n\n- Node 9:999 is not in this file.\n- Figma could not render \"spark\" (1:7).\n") {
		t.Errorf("warnings section is missing or wrong:\n%s", md[:400])
	}
}

func TestOutlineMarkdown(t *testing.T) {
	o := &extractor.Outline{Pages: []extractor.OutlinePage{
		{ID: "0:1", Name: "Home", Items: []extractor.OutlineItem{
			{ID: "1:1", Name: "Landing", Type: "FRAME", W: 1440, H: 900, Children: 2},
			{ID: "1:2", Name: "Flows", Type: "SECTION", Children: 1, Items: []extractor.OutlineItem{
				{ID: "1:3", Name: "Sign up", Type: "FRAME", W: 390, H: 844, Children: -1},
			}},
			{ID: "1:9", Name: "Button", Type: "COMPONENT_SET", W: 200, H: 100, Children: 1},
		}},
		{ID: "0:2", Name: "Archive"},
	}}
	md := OutlineMarkdown(OutlineDoc{FileName: "Site", FileKey: "KEY", LastModified: "2026-11-01T10:00:00Z", Outline: o})

	for _, want := range []string{
		"# Figma file outline: Site\n\nFile `KEY` · last modified 2026-11-01T10:00:00Z\n",
		"## Page \"Home\" #0:1\n\n",
		"- frame \"Landing\" #1:1 · 1440x900 · 2 children · https://www.figma.com/design/KEY?node-id=1-1\n",
		"- section \"Flows\" #1:2 · 1 child · https://www.figma.com/design/KEY?node-id=1-2\n",
		"  - frame \"Sign up\" #1:3 · 390x844\n",
		"- component-set \"Button\" #1:9 · 200x100 · 1 child ·",
		"## Page \"Archive\" #0:2\n\nNothing visible on this page.\n",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
}

func TestQuote(t *testing.T) {
	tests := map[string]string{
		`plain`:           `"plain"`,
		`say "hi"`:        `"say \"hi\""`,
		`back\slash`:      `"back\\slash"`,
		"two\nlines":      `"two\nlines"`,
		"crlf\r\nline":    `"crlf\nline"`,
		"soft\u2028break": `"soft\nbreak"`,
		"para\u2029break": `"para\nbreak"`,
		"bell\u0007":      `"bell\u0007"`,
		"Ελληνικά 日本語 🌈":  `"Ελληνικά 日本語 🌈"`,
	}
	for in, want := range tests {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
	}
}
