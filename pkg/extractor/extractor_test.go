package extractor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hellenic-development/figma-extractor/pkg/figma"
)

func loadCard(t *testing.T) *figma.NodesResponse {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "card.json"))
	if err != nil {
		t.Fatal(err)
	}
	var resp figma.NodesResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	return &resp
}

func buildCard(t *testing.T, opts Options) (*Design, *Node) {
	t.Helper()
	src, _ := SourceFromNodes(loadCard(t), []string{"1:2"})
	d := Build(src, opts)
	if len(d.Frames) != 1 {
		t.Fatalf("got %d frames, want the card", len(d.Frames))
	}
	return d, d.Frames[0]
}

func find(t *testing.T, root *Node, id string) *Node {
	t.Helper()
	var got *Node
	root.Walk(func(n *Node) {
		if n.ID == id {
			got = n
		}
	})
	if got == nil {
		t.Fatalf("layer %s is not in the tree", id)
	}
	return got
}

func styleToken(d *Design, name string) *StyleToken {
	for i := range d.Tokens.Styles {
		if d.Tokens.Styles[i].Name == name {
			return &d.Tokens.Styles[i]
		}
	}
	return nil
}

func TestVisibleFillsAreKeptAndHiddenOnesDropped(t *testing.T) {
	_, card := buildCard(t, Options{})

	if len(card.Fills) != 1 || card.Fills[0].CSS != "#FFFFFF" {
		t.Errorf("card fills = %+v, want the white fill Figma sent with no `visible` key", card.Fills)
	}
	if len(card.Strokes) != 1 || card.Strokes[0].CSS != "#E2E8F0" || card.StrokeWeight != 1 || card.StrokeAlign != "inside" {
		t.Errorf("card stroke = %+v weight %v align %q", card.Strokes, card.StrokeWeight, card.StrokeAlign)
	}

	button := find(t, card, "1:13")
	if len(button.Fills) != 1 || button.Fills[0].Kind != FillGradient {
		t.Fatalf("button fills = %+v, want only the gradient (the solid under it has visible:false)", button.Fills)
	}
	if want := "linear-gradient(90deg, #4F46E5 0%, #7C3AED 100%)"; button.Fills[0].CSS != want {
		t.Errorf("gradient = %q, want %q", button.Fills[0].CSS, want)
	}
}

func TestHiddenLayersAreCountedNotListed(t *testing.T) {
	d, card := buildCard(t, Options{})

	card.Walk(func(n *Node) {
		if n.Name == "Hidden spinner" || n.Name == "Spinner" {
			t.Errorf("hidden layer %q (%s) is in the tree", n.Name, n.ID)
		}
	})
	if body := find(t, card, "1:6"); body.Hidden != 1 {
		t.Errorf("body counts %d hidden children, want 1", body.Hidden)
	}
	if button := find(t, card, "1:13"); button.Hidden != 1 {
		t.Errorf("button counts %d hidden children, want 1", button.Hidden)
	}
	if d.Stats.Hidden != 2 {
		t.Errorf("stats count %d hidden layers, want 2", d.Stats.Hidden)
	}
	for _, c := range d.Tokens.Colors {
		if c.Value == "#EF4444" {
			t.Errorf("the hidden layers' red made it into the palette")
		}
	}
}

func TestColoursKeepTheirAlpha(t *testing.T) {
	_, card := buildCard(t, Options{})

	if want := []string{"0 8px 24px -4px #0F172A0D"}; !reflect.DeepEqual(card.Shadows, want) {
		t.Errorf("card shadow = %v, want %v (a 5%% shadow, not solid)", card.Shadows, want)
	}
	badge := find(t, card, "1:4")
	if badge.Fills[0].CSS != "#0F172ACC" {
		t.Errorf("badge fill = %q, want the paint's 0.8 opacity kept as CC", badge.Fills[0].CSS)
	}
	if badge.BackdropBlur != 8 {
		t.Errorf("badge backdrop blur = %v", badge.BackdropBlur)
	}
	// The frame's old backgroundColor (0,0,0,0) must not surface as black.
	if body := find(t, card, "1:6"); len(body.Fills) != 0 {
		t.Errorf("a frame with no fill reported %+v", body.Fills)
	}
}

func TestNamedStylesBecomeTokens(t *testing.T) {
	d, card := buildCard(t, Options{})

	tests := []StyleToken{
		{Name: "Neutral/Ink 900", Type: "fill", Value: "#0F172A", Uses: 3},
		{Name: "Heading/H2", Type: "text", Value: "Inter 600 24/32 ls=-0.24", Uses: 1},
		{Name: "Elevation/Card", Type: "effect", Value: "0 8px 24px -4px #0F172A0D", Uses: 1},
	}
	for _, want := range tests {
		got := styleToken(d, want.Name)
		if got == nil || *got != want {
			t.Errorf("style %q = %+v, want %+v", want.Name, got, want)
		}
	}
	heading := find(t, card, "1:10")
	if heading.FillStyle != "Neutral/Ink 900" || heading.Text.StyleName != "Heading/H2" || card.EffectStyle != "Elevation/Card" {
		t.Errorf("style names on the layers: fill %q text %q effect %q", heading.FillStyle, heading.Text.StyleName, card.EffectStyle)
	}
}

func TestTokensAreRawValuesRankedByUse(t *testing.T) {
	d, _ := buildCard(t, Options{})

	if len(d.Tokens.Colors) == 0 || d.Tokens.Colors[0].Value != "#0F172A" || d.Tokens.Colors[0].Text != 3 {
		t.Errorf("most used colour = %+v, want #0F172A as the text colour of three layers", d.Tokens.Colors)
	}
	for i := 1; i < len(d.Tokens.Colors); i++ {
		if d.Tokens.Colors[i-1].Uses() < d.Tokens.Colors[i].Uses() {
			t.Errorf("colours are not ranked by use: %+v", d.Tokens.Colors)
		}
	}
	var spacing, radii []float64
	for _, s := range d.Tokens.Spacing {
		spacing = append(spacing, s.Value)
	}
	for _, r := range d.Tokens.Radii {
		radii = append(radii, r.Value)
	}
	if want := []float64{8, 12, 20, 24}; !reflect.DeepEqual(spacing, want) {
		t.Errorf("spacing = %v, want %v: the real values, smallest first", spacing, want)
	}
	if want := []float64{10, 16, 999}; !reflect.DeepEqual(radii, want) {
		t.Errorf("radii = %v, want %v", radii, want)
	}
	if len(d.Tokens.Gradients) != 1 {
		t.Errorf("gradients = %+v", d.Tokens.Gradients)
	}
	var families []string
	for _, f := range d.Tokens.Fonts {
		families = append(families, f.Value)
	}
	if want := []string{"Inter", "Space Grotesk"}; !reflect.DeepEqual(families, want) {
		t.Errorf("font families = %v, want %v: every family, most used first", families, want)
	}
}

func TestTextIsWholeWithItsRuns(t *testing.T) {
	_, card := buildCard(t, Options{})

	heading := find(t, card, "1:10").Text
	if want := "Σχεδιάστε το σπριντ, όχι τη συνάντηση.\u2028Όλοι βλέπουν τι άλλαξε από χθες το πρωί."; heading.Content != want {
		t.Errorf("heading text = %q", heading.Content)
	}
	if heading.Style.String() != "Inter 600 24/32 ls=-0.24" || heading.Color != "#0F172A" {
		t.Errorf("heading style %q colour %q", heading.Style.String(), heading.Color)
	}

	price := find(t, card, "1:11").Text
	want := []Run{
		{Content: "€12", Style: TextStyle{Family: "Inter", Weight: 700, Size: 20, LineHeight: 28}, Color: "#0F172A"},
		{Content: " / seat", Style: TextStyle{Family: "Inter", Weight: 400, Size: 14, LineHeight: 20}, Color: "#475569"},
	}
	if !reflect.DeepEqual(price.Runs, want) {
		t.Errorf("price runs = %+v\nwant %+v", price.Runs, want)
	}

	if tag := find(t, card, "1:5").Text; tag.Style.String() != "Inter 500 12/16 upper" {
		t.Errorf("tag style = %q", tag.Style.String())
	}
	if label := find(t, card, "I1:13;4:10").Text; label.Align != "center" || label.Runs != nil {
		t.Errorf("label align %q runs %v", label.Align, label.Runs)
	}
	// Figma's Auto line height reports the font's own pixels; it is not a
	// value the designer chose.
	if mark := find(t, card, "1:27").Text; mark.Style.String() != "Space Grotesk 700 20/auto" {
		t.Errorf("wordmark style = %q", mark.Style.String())
	}
}

func TestRunsNeverSplitACharacter(t *testing.T) {
	// "a😀b": the emoji is two UTF-16 units, each with its own override id.
	n := &figma.Node{
		ID: "1:1", Type: "TEXT", Characters: "a😀b",
		Style:                   &figma.TypeStyle{FontFamily: "Inter", FontWeight: 400, FontSize: 16, LineHeightPx: 24},
		CharacterStyleOverrides: []int{0, 1, 2, 0},
		StyleOverrideTable: map[string]figma.TypeStyle{
			"1": {FontWeight: 700},
			"2": {FontWeight: 500},
		},
	}
	d := Build(Source{Roots: []*figma.Node{n}}, Options{})
	var joined string
	for _, run := range d.Frames[0].Text.Runs {
		if !strings.ContainsAny(run.Content, "a😀b") || strings.ContainsRune(run.Content, '�') {
			t.Errorf("run %q holds a broken character", run.Content)
		}
		joined += run.Content
	}
	if joined != "a😀b" {
		t.Errorf("runs join to %q, want the text back", joined)
	}
}

func TestLayoutSizingAndPosition(t *testing.T) {
	_, card := buildCard(t, Options{})

	if card.Layout == nil || card.Layout.Direction != "col" || !card.Clip || card.Placed {
		t.Errorf("card layout %+v clip %v placed %v", card.Layout, card.Clip, card.Placed)
	}
	if card.Radius != [4]float64{16, 16, 16, 16} {
		t.Errorf("card radius = %v", card.Radius)
	}

	photo := find(t, card, "1:3")
	if photo.Kind != KindImage || photo.Fills[0].Fit != "cover" || photo.Radius != [4]float64{16, 16, 0, 0} {
		t.Errorf("photo kind %q fills %+v radius %v", photo.Kind, photo.Fills, photo.Radius)
	}
	if photo.SizeH != SizingFill || photo.SizeV != SizingFixed || photo.Placed || photo.ExportFormat != "jpg" {
		t.Errorf("photo sizing %q/%q placed %v export %q", photo.SizeH, photo.SizeV, photo.Placed, photo.ExportFormat)
	}

	// Taken out of the auto layout: it needs its offset.
	badge := find(t, card, "1:4")
	if !badge.Absolute || !badge.Placed || badge.X != 16 || badge.Y != 16 {
		t.Errorf("badge absolute %v placed %v at %v,%v", badge.Absolute, badge.Placed, badge.X, badge.Y)
	}
	if l := badge.Layout; l == nil || l.Direction != "row" || l.Align != "center" || l.Padding != [4]float64{0, 8, 0, 8} {
		t.Errorf("badge layout = %+v", badge.Layout)
	}

	body := find(t, card, "1:6")
	if l := body.Layout; l == nil || l.Gap != 12 || l.Padding != [4]float64{24, 24, 24, 24} {
		t.Errorf("body layout = %+v", body.Layout)
	}

	button := find(t, card, "1:13")
	if l := button.Layout; l == nil || l.Justify != "center" || l.Align != "center" || l.Gap != 8 {
		t.Errorf("button layout = %+v", button.Layout)
	}

	gallery := find(t, card, "1:20")
	if l := gallery.Layout; l == nil || !l.Wrap || l.CrossGap != 8 {
		t.Errorf("gallery layout = %+v", gallery.Layout)
	}

	// A child of a frame with no auto layout is placed by its offset.
	if mark := find(t, card, "1:27"); !mark.Placed || mark.Absolute || mark.X != 0 || mark.Y != 8 {
		t.Errorf("wordmark placed %v absolute %v at %v,%v", mark.Placed, mark.Absolute, mark.X, mark.Y)
	}
	if logo := find(t, card, "1:26"); logo.Kind != KindFrame || logo.Icon || logo.ExportFormat != "svg" {
		t.Errorf("logo kind %q icon %v export %q: it holds text, so it is not an icon", logo.Kind, logo.Icon, logo.ExportFormat)
	}
}

func TestIconsCollapseToOneLine(t *testing.T) {
	d, card := buildCard(t, Options{})

	spark := find(t, card, "1:7")
	if !spark.Icon || spark.Kind != KindIcon || len(spark.Children) != 0 {
		t.Errorf("spark icon %v kind %q with %d children, want one collapsed line", spark.Icon, spark.Kind, len(spark.Children))
	}
	card.Walk(func(n *Node) {
		if n.Type == "VECTOR" {
			t.Errorf("a vector path (%s) is listed on its own", n.ID)
		}
	})
	if d.Stats.Icons != 3 {
		t.Errorf("stats count %d icons, want 3", d.Stats.Icons)
	}
	// Brand/600 is used only inside the spark icon: an icon's inside is its
	// paths, not a design token.
	if styleToken(d, "Brand/600") != nil {
		t.Errorf("a style used only inside an icon became a token")
	}

	// The arrow in the button and the rotated, half-transparent one are the
	// same component but not the same picture.
	inButton, rotated := find(t, card, "I1:13;4:11"), find(t, card, "1:28")
	if inButton.AssetKey == rotated.AssetKey {
		t.Errorf("a rotated instance shares a file with an upright one (%q)", inButton.AssetKey)
	}
	if inButton.Instance == nil || inButton.Instance.Component != "Icon/Arrow right" {
		t.Errorf("icon instance = %+v", inButton.Instance)
	}
}

func TestUnchangedInstancesOfOneIconShareAnAsset(t *testing.T) {
	icon := func(id string, overrides []figma.Override) *figma.Node {
		return &figma.Node{
			ID: id, Type: "INSTANCE", ComponentID: "6:1", Overrides: overrides,
			AbsoluteBoundingBox: &figma.Rectangle{Width: 20, Height: 20},
			Children:            []*figma.Node{{ID: id + ";v", Type: "VECTOR"}},
		}
	}
	a := icon("1:1", nil)
	b := icon("1:2", []figma.Override{{ID: "1:2", OverriddenFields: []string{"width", "height"}}})
	recoloured := icon("1:3", []figma.Override{{ID: "1:3;v", OverriddenFields: []string{"fills"}}})

	if iconKey(a) != iconKey(b) {
		t.Errorf("two unchanged instances got different keys: %q and %q", iconKey(a), iconKey(b))
	}
	if iconKey(a) == iconKey(recoloured) {
		t.Errorf("a recoloured instance shares the key of an unchanged one")
	}
}

func TestAStyledAutoLayoutBoxIsNotAnIcon(t *testing.T) {
	white := figma.Paint{Type: "SOLID", Color: &figma.Color{R: 1, G: 1, B: 1, A: 1}}
	iconButton := &figma.Node{
		ID: "1:1", Type: "FRAME", LayoutMode: "HORIZONTAL", Fills: []figma.Paint{white},
		Children: []*figma.Node{{ID: "1:2", Type: "FRAME", Children: []*figma.Node{{ID: "1:3", Type: "VECTOR"}}}},
	}
	if isIcon(iconButton) {
		t.Errorf("an icon button with its own background collapsed into an icon")
	}
	if !isIcon(iconButton.Children[0]) {
		t.Errorf("the icon inside it did not collapse")
	}
	twoRects := &figma.Node{ID: "2:1", Type: "GROUP", Children: []*figma.Node{{ID: "2:2", Type: "RECTANGLE"}, {ID: "2:3", Type: "RECTANGLE"}}}
	if isIcon(twoRects) {
		t.Errorf("a group of plain rectangles, with no vector path, collapsed into an icon")
	}
}

func TestInstancesNameTheirComponent(t *testing.T) {
	d, card := buildCard(t, Options{})

	inst := find(t, card, "1:13").Instance
	if inst == nil || inst.Component != "Button" || inst.Description != "The main action on a screen." {
		t.Fatalf("button instance = %+v", inst)
	}
	want := []Prop{
		{Name: "Icon", Type: "instance-swap", Value: "Icon/Arrow right"},
		{Name: "Label", Type: "text", Value: "Start free trial"},
		{Name: "Show icon", Type: "boolean", Value: "true"},
		{Name: "Size", Type: "variant", Value: "Large"},
		{Name: "Type", Type: "variant", Value: "Primary"},
	}
	if !reflect.DeepEqual(inst.Props, want) {
		t.Errorf("props = %+v\nwant %+v", inst.Props, want)
	}

	wantUse := ComponentUse{Name: "Button", Description: "The main action on a screen.", Uses: 1, Variants: []string{"Size=Large, Type=Primary"}}
	if len(d.Components) != 1 || !reflect.DeepEqual(d.Components[0], wantUse) {
		t.Errorf("components = %+v\nwant %+v", d.Components, wantUse)
	}
	// An instance of a component the response does not describe still builds.
	if spark := find(t, card, "1:7"); spark.Instance == nil || spark.Instance.Component != "" {
		t.Errorf("spark instance = %+v", spark.Instance)
	}
}

func TestDepthCutsTheTreeAndSaysSo(t *testing.T) {
	_, card := buildCard(t, Options{Depth: 1})

	body := find(t, card, "1:6")
	if len(body.Children) != 0 || body.Omitted != 5 || body.Hidden != 1 {
		t.Errorf("body at the depth limit: %d children listed, %d omitted, %d hidden; want 0, 5, 1", len(body.Children), body.Omitted, body.Hidden)
	}
	if len(card.Children) != 3 {
		t.Errorf("card lists %d children, want its 3 direct ones", len(card.Children))
	}
}

func TestSameInputSameDesign(t *testing.T) {
	first, _ := buildCard(t, Options{})
	for i := range 30 {
		if again, _ := buildCard(t, Options{}); !reflect.DeepEqual(first, again) {
			t.Fatalf("build %d differs from the first", i+2)
		}
	}
}

func TestMissingNodesAreReported(t *testing.T) {
	src, missing := SourceFromNodes(loadCard(t), []string{"1-2", "9:999", "8:8"})
	if len(src.Roots) != 1 || !reflect.DeepEqual(missing, []string{"9:999", "8:8"}) {
		t.Errorf("%d roots, missing %v; want the card and the two absent ids", len(src.Roots), missing)
	}
}

// A caller that fetched a page can ask for frames on it without another
// request: the answer already holds them.
func TestNodesInsideAFetchedNodeAreFound(t *testing.T) {
	resp := loadCard(t)
	src, missing := SourceFromNodes(resp, []string{"1-6", "I1:13;4:10", "8:8"})
	if len(src.Roots) != 2 || src.Roots[0].Name != "Body" || src.Roots[1].Name != "Label" {
		t.Fatalf("roots = %d, want the Body frame and the Label text found inside the card", len(src.Roots))
	}
	if !reflect.DeepEqual(missing, []string{"8:8"}) {
		t.Errorf("missing = %v, want only the id that is nowhere in the answer", missing)
	}
	// The styles and components of the answer it sits in come along.
	d := Build(src, Options{})
	if len(d.Frames) != 2 || styleToken(d, "Heading/H2") == nil || len(d.Components) != 1 {
		t.Errorf("%d frames, Heading/H2 %v, %d components: the nested frame lost its name maps", len(d.Frames), styleToken(d, "Heading/H2"), len(d.Components))
	}
	// A nested frame is a frame of its own: it is not placed in a parent.
	if d.Frames[0].Placed || d.Frames[0].Name != "Body" {
		t.Errorf("nested frame %q placed=%v", d.Frames[0].Name, d.Frames[0].Placed)
	}
}

func pagesFixture() *figma.Node {
	hidden := false
	frame := func(id, name string, children ...*figma.Node) *figma.Node {
		return &figma.Node{ID: id, Name: name, Type: "FRAME", Children: children, AbsoluteBoundingBox: &figma.Rectangle{Width: 1440, Height: 900}}
	}
	return &figma.Node{ID: "0:0", Type: "DOCUMENT", Children: []*figma.Node{
		{ID: "0:1", Name: "Home", Type: "CANVAS", Children: []*figma.Node{
			frame("1:1", "Landing", frame("1:10", "Hero"), frame("1:11", "Footer")),
			{ID: "1:2", Name: "Flows", Type: "SECTION", Children: []*figma.Node{frame("1:3", "Sign up", frame("1:30", "Form")), frame("1:4", "Sign in")}},
			{ID: "1:5", Name: "Scratch", Type: "FRAME", Visible: &hidden},
		}},
		{ID: "0:2", Name: "Archive", Type: "CANVAS"},
	}}
}

func TestAPageExpandsIntoItsFrames(t *testing.T) {
	d := Build(Source{Roots: []*figma.Node{pagesFixture()}}, Options{})

	var got []string
	for _, f := range d.Frames {
		got = append(got, f.Page+"/"+f.Section+"/"+f.Name)
	}
	want := []string{"Home//Landing", "Home/Flows/Sign up", "Home/Flows/Sign in"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("frames = %v, want %v", got, want)
	}
}

func TestOutline(t *testing.T) {
	// As a file fetched at depth 3: pages, their top layers, and one level
	// more. "Form" (level 4) would not be in such a response.
	o := BuildOutline([]*figma.Node{pagesFixture()}, 3)

	if len(o.Pages) != 2 || o.Pages[0].Name != "Home" || o.Pages[1].Name != "Archive" {
		t.Fatalf("pages = %+v", o.Pages)
	}
	items := o.Pages[0].Items
	if len(items) != 2 {
		t.Fatalf("Home lists %d items, want the frame and the section (the hidden frame is skipped)", len(items))
	}
	if landing := items[0]; landing.Name != "Landing" || landing.Children != 2 || landing.W != 1440 || len(landing.Items) != 0 {
		t.Errorf("landing = %+v", landing)
	}
	flows := items[1]
	if flows.Type != "SECTION" || flows.Children != 2 || len(flows.Items) != 2 {
		t.Fatalf("flows = %+v, want the section opened into its frames", flows)
	}
	if flows.Items[0].Name != "Sign up" || flows.Items[0].Children != -1 {
		t.Errorf("sign up = %+v: its children sit below what was fetched, so the count is unknown", flows.Items[0])
	}

	// A frame asked for by id is opened one level.
	sel := BuildOutline([]*figma.Node{pagesFixture().Children[0].Children[0]}, 2)
	if len(sel.Pages) != 1 || sel.Pages[0].ID != "" || len(sel.Pages[0].Items) != 1 || len(sel.Pages[0].Items[0].Items) != 2 {
		t.Errorf("selection outline = %+v", sel.Pages)
	}
}
