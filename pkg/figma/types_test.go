package figma

import (
	"encoding/json"
	"reflect"
	"testing"
)

// findNode walks to the first node with the given id.
func findNode(n *Node, id string) *Node {
	if n.ID == id {
		return n
	}
	for _, child := range n.Children {
		if got := findNode(child, id); got != nil {
			return got
		}
	}
	return nil
}

func decodeCard(t *testing.T) *NodeData {
	t.Helper()
	var resp NodesResponse
	if err := json.Unmarshal(fixture(t, "card.json"), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Nodes["1:2"] == nil {
		t.Fatal("the card is missing from the fixture")
	}
	return resp.Nodes["1:2"]
}

// Figma leaves `visible` and `opacity` out at their defaults. Reading them as
// plain values dropped every visible fill in the file.
func TestOmittedVisibleAndOpacityMeanTheDefault(t *testing.T) {
	card := decodeCard(t)

	fill := card.Document.Fills[0]
	if !fill.IsVisible() || fill.OpacityOr1() != 1 {
		t.Errorf("a fill with no `visible` and no `opacity` key read as visible=%v opacity=%v", fill.IsVisible(), fill.OpacityOr1())
	}
	if !card.Document.IsVisible() || card.Document.OpacityOr1() != 1 {
		t.Errorf("a node with no `visible` key read as hidden")
	}

	button := findNode(&card.Document, "1:13")
	if button.Fills[0].IsVisible() {
		t.Errorf("a fill with visible:false read as visible")
	}
	if !button.Fills[1].IsVisible() || len(button.Fills[1].GradientStops) != 2 {
		t.Errorf("the gradient fill did not decode: %+v", button.Fills[1])
	}

	if findNode(&card.Document, "1:12").IsVisible() {
		t.Errorf("a node with visible:false read as visible")
	}
	if badge := findNode(&card.Document, "1:4"); badge.Fills[0].OpacityOr1() == 1 {
		t.Errorf("a fill with opacity 0.8 read as opaque")
	}
	if arrow := findNode(&card.Document, "1:28"); arrow.OpacityOr1() != 0.5 || arrow.Rotation == 0 {
		t.Errorf("node opacity %v rotation %v", arrow.OpacityOr1(), arrow.Rotation)
	}
	if !card.Document.Effects[0].IsVisible() {
		t.Errorf("the shadow read as off")
	}
}

func TestStylesComponentsAndInstancesDecode(t *testing.T) {
	card := decodeCard(t)

	if got := card.Styles["7:2"]; got.Name != "Heading/H2" || got.StyleType != "TEXT" {
		t.Errorf("style 7:2 = %+v (the file endpoints write styleType, not style_type)", got)
	}
	if got := card.Components["5:1"]; got.ComponentSetID != "5:0" {
		t.Errorf("component 5:1 = %+v", got)
	}
	if got := card.ComponentSets["5:0"]; got.Name != "Button" {
		t.Errorf("component set 5:0 = %+v", got)
	}

	heading := findNode(&card.Document, "1:10")
	if !reflect.DeepEqual(heading.Styles, map[string]string{"text": "7:2", "fill": "7:1"}) {
		t.Errorf("heading styles = %v", heading.Styles)
	}
	if heading.Style.LetterSpacing != -0.24 || heading.Style.FontPostScriptName != "Inter-SemiBold" {
		t.Errorf("heading style = %+v", heading.Style)
	}
	// fontPostScriptName is null on some nodes.
	if tag := findNode(&card.Document, "1:5"); tag.Style.FontPostScriptName != "" || tag.Style.TextCase != "UPPER" {
		t.Errorf("tag style = %+v", tag.Style)
	}

	price := findNode(&card.Document, "1:11")
	if len(price.CharacterStyleOverrides) != 10 || price.StyleOverrideTable["1"].FontSize != 14 || len(price.StyleOverrideTable["1"].Fills) != 1 {
		t.Errorf("price overrides = %v %+v", price.CharacterStyleOverrides, price.StyleOverrideTable)
	}

	button := findNode(&card.Document, "1:13")
	if button.ComponentID != "5:1" || button.ComponentProperties["Type"].Value != "Primary" || button.ComponentProperties["Show icon#4:12"].Value != true {
		t.Errorf("button = %q %+v", button.ComponentID, button.ComponentProperties)
	}
	if button.PrimaryAxisAlignItems != "CENTER" || button.LayoutSizingHorizontal != "FILL" {
		t.Errorf("button layout = %q %q", button.PrimaryAxisAlignItems, button.LayoutSizingHorizontal)
	}

	photo := findNode(&card.Document, "1:3")
	if photo.Fills[0].ImageRef != "ref_photo" || photo.ExportSettings[0].Format != "JPG" || len(photo.RectangleCornerRadii) != 4 {
		t.Errorf("photo = %+v", photo)
	}
	if badge := findNode(&card.Document, "1:4"); badge.LayoutPositioning != "ABSOLUTE" {
		t.Errorf("badge positioning = %q", badge.LayoutPositioning)
	}
	// absoluteRenderBounds is null on a masked vector.
	if shape := findNode(&card.Document, "I1:7;126:526"); shape.AbsoluteRenderBounds != nil || !shape.IsMask {
		t.Errorf("shape = %+v", shape)
	}
}

func TestImageFillsAnswerDecodesFromMeta(t *testing.T) {
	var resp FileImagesResponse
	body := `{"error":false,"status":200,"meta":{"images":{"ref_photo":"https://example.invalid/a"}},"i18n":null}`
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Meta.Images) != 1 {
		t.Errorf("decoded %d refs, want 1", len(resp.Meta.Images))
	}
}
