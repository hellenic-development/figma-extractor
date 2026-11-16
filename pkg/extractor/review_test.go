package extractor

import (
	"reflect"
	"testing"

	"github.com/hellenic-development/figma-extractor/pkg/figma"
)

func solid(r, g, b float64) figma.Paint {
	return figma.Paint{Type: "SOLID", Color: &figma.Color{R: r, G: g, B: b, A: 1}}
}

// Two instances of one icon component, white inside a dark button and black
// inside a light one. Nothing is overridden on the icon instance itself (the
// parent component set the colour), so only what is painted tells them apart.
func TestRecolouredIconsDoNotShareAFile(t *testing.T) {
	icon := func(id string, fill figma.Paint) *figma.Node {
		return &figma.Node{
			ID: id, Type: "INSTANCE", ComponentID: "6:1",
			AbsoluteBoundingBox: &figma.Rectangle{Width: 20, Height: 20},
			Children:            []*figma.Node{{ID: id + ";v", Type: "VECTOR", Fills: []figma.Paint{fill}}},
		}
	}
	white, alsoWhite, black := icon("1:1", solid(1, 1, 1)), icon("1:2", solid(1, 1, 1)), icon("1:3", solid(0, 0, 0))

	if iconKey(white) == iconKey(black) {
		t.Errorf("a white and a black instance of one icon share a file (%q)", iconKey(white))
	}
	if iconKey(white) != iconKey(alsoWhite) {
		t.Errorf("two identical instances got different keys: %q and %q", iconKey(white), iconKey(alsoWhite))
	}
}

// A run can turn a style OFF: the layer is underlined capitals and one
// stretch of it is neither. The override table says so with "NONE",
// "ORIGINAL" and italic:false, not by leaving the key out.
func TestARunCanSwitchAStyleOff(t *testing.T) {
	yes, no := true, false
	n := &figma.Node{
		ID: "1:1", Type: "TEXT", Characters: "LOUD quiet",
		Style:                   &figma.TypeStyle{FontFamily: "Inter", FontWeight: 400, FontSize: 16, LineHeightPx: 24, TextCase: "UPPER", TextDecoration: "UNDERLINE", Italic: &yes},
		CharacterStyleOverrides: []int{0, 0, 0, 0, 0, 1, 1, 1, 1, 1},
		StyleOverrideTable:      map[string]figma.TypeStyle{"1": {TextCase: "ORIGINAL", TextDecoration: "NONE", Italic: &no}},
	}
	d := Build(Source{Roots: []*figma.Node{n}}, Options{})
	runs := d.Frames[0].Text.Runs
	if len(runs) != 2 {
		t.Fatalf("runs = %+v, want the styled stretch and the plain one", runs)
	}
	if got := runs[0].Style.String(); got != "Inter 400 16/24 upper italic underline" {
		t.Errorf("first run = %q", got)
	}
	if got := runs[1].Style.String(); got != "Inter 400 16/24" {
		t.Errorf("second run = %q, want case, italic and underline switched off", got)
	}
}

func TestGradientsFollowTheirHandles(t *testing.T) {
	stops := []figma.ColorStop{
		{Position: 0, Color: figma.Color{R: 1, A: 1}},
		{Position: 1, Color: figma.Color{B: 1, A: 1}},
	}
	tests := []struct {
		name    string
		kind    string
		handles []figma.Vector
		want    string
	}{
		// Edge to edge: the stops are where CSS puts them anyway.
		{"left to right", "GRADIENT_LINEAR", []figma.Vector{{X: 0, Y: 0.5}, {X: 1, Y: 0.5}, {X: 0, Y: 1}}, "linear-gradient(90deg, #FF0000 0%, #0000FF 100%)"},
		{"top to bottom", "GRADIENT_LINEAR", []figma.Vector{{X: 0.5, Y: 0}, {X: 0.5, Y: 1}, {X: 0, Y: 0}}, "linear-gradient(180deg, #FF0000 0%, #0000FF 100%)"},
		// The designer's gradient covers the middle half of the box only.
		{"middle half", "GRADIENT_LINEAR", []figma.Vector{{X: 0.25, Y: 0.5}, {X: 0.75, Y: 0.5}, {X: 0.25, Y: 1}}, "linear-gradient(90deg, #FF0000 25%, #0000FF 75%)"},
		// Corner to corner of a 200x100 box is not 135 degrees.
		{"diagonal of a wide box", "GRADIENT_LINEAR", []figma.Vector{{X: 0, Y: 0}, {X: 1, Y: 1}, {X: 0, Y: 1}}, "linear-gradient(117deg, #FF0000 0%, #0000FF 100%)"},
		{"radial to the sides", "GRADIENT_RADIAL", []figma.Vector{{X: 0.5, Y: 0.5}, {X: 1, Y: 0.5}, {X: 0.5, Y: 1}}, "radial-gradient(ellipse 100px 50px at 50% 50%, #FF0000 0%, #0000FF 100%)"},
		{"radial off centre", "GRADIENT_RADIAL", []figma.Vector{{X: 0.25, Y: 0.5}, {X: 0.5, Y: 0.5}, {X: 0.25, Y: 1}}, "radial-gradient(ellipse 50px 50px at 25% 50%, #FF0000 0%, #0000FF 100%)"},
		{"angular", "GRADIENT_ANGULAR", []figma.Vector{{X: 0.5, Y: 0.5}, {X: 1, Y: 0.5}, {X: 0.5, Y: 1}}, "conic-gradient(from 90deg at 50% 50%, #FF0000 0%, #0000FF 100%)"},
		{"no handles", "GRADIENT_LINEAR", nil, "linear-gradient(180deg, #FF0000 0%, #0000FF 100%)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &figma.Paint{Type: tt.kind, GradientHandlePositions: tt.handles, GradientStops: stops}
			if got := gradientCSS(p, 200, 100); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

// A page and one of the frames on it, both asked for: the frame is one frame.
func TestOverlappingRootsGiveEachFrameOnce(t *testing.T) {
	page := pagesFixture().Children[0]
	landing := page.Children[0]
	d := Build(Source{Roots: []*figma.Node{page, landing}}, Options{})

	var got []string
	for _, f := range d.Frames {
		got = append(got, f.Name)
	}
	if want := []string{"Landing", "Sign up", "Sign in"}; !reflect.DeepEqual(got, want) {
		t.Errorf("frames = %v, want %v", got, want)
	}
}
