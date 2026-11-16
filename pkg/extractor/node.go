package extractor

import (
	"hash/fnv"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/hellenic-development/figma-extractor/pkg/figma"
)

type builder struct {
	src   Source
	opts  Options
	tally *tally
	stats Stats
	seen  map[string]bool // frames already added, by id
}

// node builds the layer n, which sits in parent (nil for a top-level frame)
// at the given depth below its frame. n is visible; hidden layers never
// reach here.
func (b *builder) node(n *figma.Node, parent *figma.Node, depth int) *Node {
	b.stats.Layers++
	out := &Node{ID: n.ID, Name: n.Name, Type: n.Type, Opacity: n.OpacityOr1()}

	b.geometry(out, n, parent)
	b.paint(out, n)

	if n.Type == "TEXT" {
		out.Text = b.text(n, out)
	}
	if n.Type == "INSTANCE" {
		out.Instance = b.instance(n)
	}
	if len(n.ExportSettings) > 0 {
		out.ExportFormat = strings.ToLower(n.ExportSettings[0].Format)
	}

	if isIcon(n) {
		out.Icon = true
		out.AssetKey = iconKey(n)
		out.Kind = KindIcon
		if math.Max(out.W, out.H) > 64 {
			out.Kind = KindGraphic
		}
		b.stats.Icons++
		// What is inside an icon is its paths, not design tokens: nothing
		// below this layer is walked.
		return out
	}

	out.Kind = kindOf(n, out)
	out.AssetKey = "n:" + n.ID
	b.tally.node(out)

	for _, child := range n.Children {
		if !child.IsVisible() {
			out.Hidden++
			b.stats.Hidden++
			continue
		}
		if b.opts.Depth > 0 && depth >= b.opts.Depth {
			out.Omitted++
			continue
		}
		out.Children = append(out.Children, b.node(child, n, depth+1))
	}
	return out
}

func kindOf(n *figma.Node, out *Node) Kind {
	switch n.Type {
	case "TEXT":
		return KindText
	case "INSTANCE":
		return KindInstance
	case "COMPONENT":
		return KindComponent
	case "COMPONENT_SET":
		return KindComponentSet
	case "SECTION":
		return KindSection
	}
	// A leaf that shows a picture is an image whatever shape holds it.
	if len(n.Children) == 0 && len(out.ImageRefs()) > 0 {
		return KindImage
	}
	switch n.Type {
	case "FRAME":
		return KindFrame
	case "GROUP":
		return KindGroup
	case "RECTANGLE":
		return KindRect
	case "ELLIPSE":
		return KindEllipse
	case "LINE":
		return KindLine
	}
	return Kind(strings.ToLower(strings.ReplaceAll(n.Type, "_", "-")))
}

func hasAutoLayout(n *figma.Node) bool {
	return n != nil && (n.LayoutMode == "HORIZONTAL" || n.LayoutMode == "VERTICAL" || n.LayoutMode == "GRID")
}

func (b *builder) geometry(out *Node, n, parent *figma.Node) {
	if box := n.AbsoluteBoundingBox; box != nil {
		out.W, out.H = box.Width, box.Height
		if parent != nil && parent.AbsoluteBoundingBox != nil {
			out.X = box.X - parent.AbsoluteBoundingBox.X
			out.Y = box.Y - parent.AbsoluteBoundingBox.Y
			out.Absolute = hasAutoLayout(parent) && n.LayoutPositioning == "ABSOLUTE"
			out.Placed = !hasAutoLayout(parent) || out.Absolute
		}
	}
	out.SizeH, out.SizeV = sizingOf(n.LayoutSizingHorizontal), sizingOf(n.LayoutSizingVertical)
	out.Rotation = round2(n.Rotation * 180 / math.Pi)
	out.Clip = n.ClipsContent
	out.Mask = n.IsMask
	if n.BlendMode != "" && n.BlendMode != "PASS_THROUGH" && n.BlendMode != "NORMAL" {
		out.BlendMode = strings.ToLower(strings.ReplaceAll(n.BlendMode, "_", "-"))
	}

	if !hasAutoLayout(n) {
		return
	}
	l := &Layout{
		Gap:      n.ItemSpacing,
		CrossGap: n.CounterAxisSpacing,
		Padding:  [4]float64{n.PaddingTop, n.PaddingRight, n.PaddingBottom, n.PaddingLeft},
		Wrap:     n.LayoutWrap == "WRAP",
	}
	switch n.LayoutMode {
	case "HORIZONTAL":
		l.Direction = "row"
	case "VERTICAL":
		l.Direction = "col"
	default:
		l.Direction = "grid"
	}
	switch n.PrimaryAxisAlignItems {
	case "CENTER":
		l.Justify = "center"
	case "MAX":
		l.Justify = "end"
	case "SPACE_BETWEEN":
		l.Justify = "between"
		l.Gap = 0 // the gap Figma reports here is whatever space was left over
	}
	switch n.CounterAxisAlignItems {
	case "CENTER":
		l.Align = "center"
	case "MAX":
		l.Align = "end"
	case "BASELINE":
		l.Align = "baseline"
	}
	out.Layout = l
}

func sizingOf(s string) Sizing {
	switch s {
	case "HUG":
		return SizingHug
	case "FILL":
		return SizingFill
	case "FIXED":
		return SizingFixed
	}
	return ""
}

func (b *builder) paint(out *Node, n *figma.Node) {
	out.Fills = fillsOf(n.Fills, out.W, out.H)
	out.Strokes = fillsOf(n.Strokes, out.W, out.H)
	out.FillStyle = b.styleName(n, "fill", "fills")
	out.StrokeStyle = b.styleName(n, "stroke", "strokes")
	out.EffectStyle = b.styleName(n, "effect")

	if len(out.Strokes) > 0 {
		out.StrokeWeight = n.StrokeWeight
		out.StrokeAlign = strings.ToLower(n.StrokeAlign)
		out.Dashed = len(n.StrokeDashes) > 0
		if w := n.IndividualStrokeWeights; w != nil {
			sides := [4]float64{w.Top, w.Right, w.Bottom, w.Left}
			if sides[0] != sides[1] || sides[1] != sides[2] || sides[2] != sides[3] {
				out.StrokeSides = &sides
			}
		}
	}

	if len(n.RectangleCornerRadii) == 4 {
		copy(out.Radius[:], n.RectangleCornerRadii)
	} else if n.CornerRadius > 0 {
		out.Radius = [4]float64{n.CornerRadius, n.CornerRadius, n.CornerRadius, n.CornerRadius}
	}

	for i := range n.Effects {
		e := &n.Effects[i]
		if !e.IsVisible() {
			continue
		}
		switch e.Type {
		case "DROP_SHADOW", "INNER_SHADOW":
			out.Shadows = append(out.Shadows, shadowCSS(e))
		case "LAYER_BLUR":
			out.LayerBlur = e.Radius
		case "BACKGROUND_BLUR":
			out.BackdropBlur = e.Radius
		}
	}
}

// fillsOf keeps the visible paints, top first. Figma lists paints bottom
// first; CSS lists backgrounds top first, and that is the order used here.
func fillsOf(paints []figma.Paint, w, h float64) []Fill {
	var fills []Fill
	for i := len(paints) - 1; i >= 0; i-- {
		p := &paints[i]
		if !p.IsVisible() || p.OpacityOr1() == 0 {
			continue
		}
		switch {
		case p.Type == "SOLID" && p.Color != nil:
			fills = append(fills, Fill{Kind: FillSolid, CSS: colorCSS(*p.Color, p.OpacityOr1())})
		case strings.HasPrefix(p.Type, "GRADIENT_") && len(p.GradientStops) > 0:
			fills = append(fills, Fill{Kind: FillGradient, CSS: gradientCSS(p, w, h)})
		case p.Type == "IMAGE" && p.ImageRef != "":
			fills = append(fills, Fill{Kind: FillImage, ImageRef: p.ImageRef, Fit: fitOf(p.ScaleMode)})
		}
	}
	return fills
}

func fitOf(scaleMode string) string {
	switch scaleMode {
	case "FIT":
		return "contain"
	case "TILE":
		return "tile"
	case "STRETCH":
		return "stretch"
	default: // FILL, and the cropped modes that also cover the box
		return "cover"
	}
}

// styleName is the name of the Figma style the layer uses for one of its
// aspects, looked up under each key the API has used for it.
func (b *builder) styleName(n *figma.Node, keys ...string) string {
	for _, key := range keys {
		if id := n.Styles[key]; id != "" {
			if style, ok := b.src.Styles[id]; ok && style.Name != "" {
				return style.Name
			}
		}
	}
	return ""
}

func textStyleOf(s *figma.TypeStyle) TextStyle {
	if s == nil {
		return TextStyle{}
	}
	ts := TextStyle{
		Family:        s.FontFamily,
		Weight:        int(math.Round(s.FontWeight)),
		Size:          round2(s.FontSize),
		LetterSpacing: round2(s.LetterSpacing),
		Italic:        s.Italic != nil && *s.Italic,
	}
	// "INTRINSIC_%" is Figma's Auto line height: the pixel value it reports
	// is the font's own, not a choice the designer made.
	if !strings.HasPrefix(s.LineHeightUnit, "INTRINSIC") {
		ts.LineHeight = round2(s.LineHeightPx)
	}
	switch s.TextCase {
	case "UPPER":
		ts.Case = "upper"
	case "LOWER":
		ts.Case = "lower"
	case "TITLE":
		ts.Case = "title"
	case "SMALL_CAPS", "SMALL_CAPS_FORCED":
		ts.Case = "small-caps"
	}
	switch s.TextDecoration {
	case "UNDERLINE":
		ts.Decoration = "underline"
	case "STRIKETHROUGH":
		ts.Decoration = "strike"
	}
	return ts
}

// overrideStyle lays a run's changed keys over the layer's base style. The
// override table holds only what differs, so a missing key means
// "unchanged". A run can also switch a style off, and says so with a value
// of its own ("NONE", "ORIGINAL", italic false), which is why case,
// decoration and italic are decided by whether the key is there and not by
// what it maps to.
func overrideStyle(base TextStyle, o *figma.TypeStyle) TextStyle {
	changed := textStyleOf(o)
	if changed.Family != "" {
		base.Family = changed.Family
	}
	if changed.Weight != 0 {
		base.Weight = changed.Weight
	}
	if changed.Size != 0 {
		base.Size = changed.Size
	}
	if o.LineHeightUnit != "" || o.LineHeightPx != 0 {
		base.LineHeight = changed.LineHeight
	}
	if changed.LetterSpacing != 0 {
		base.LetterSpacing = changed.LetterSpacing
	}
	if o.TextCase != "" {
		base.Case = changed.Case
	}
	if o.TextDecoration != "" {
		base.Decoration = changed.Decoration
	}
	if o.Italic != nil {
		base.Italic = *o.Italic
	}
	return base
}

func (b *builder) text(n *figma.Node, out *Node) *Text {
	t := &Text{
		Content:   n.Characters,
		Style:     textStyleOf(n.Style),
		StyleName: b.styleName(n, "text"),
	}
	if len(out.Fills) > 0 && out.Fills[0].Kind != FillImage {
		t.Color = out.Fills[0].CSS
	}
	if n.Style != nil {
		switch n.Style.TextAlignHorizontal {
		case "CENTER":
			t.Align = "center"
		case "RIGHT":
			t.Align = "right"
		case "JUSTIFIED":
			t.Align = "justified"
		}
		if n.Style.Hyperlink != nil {
			t.Link = n.Style.Hyperlink.URL
		}
	}
	t.Runs = textRuns(n, t)
	return t
}

// textRuns splits a text layer into the stretches that differ in style,
// colour or link. Figma indexes the overrides by UTF-16 code unit, so the
// text is cut in that encoding and never in the middle of a character.
func textRuns(n *figma.Node, t *Text) []Run {
	mixed := false
	for _, id := range n.CharacterStyleOverrides {
		if id != 0 {
			mixed = true
			break
		}
	}
	if !mixed {
		return nil
	}

	units := utf16.Encode([]rune(n.Characters))
	overrideAt := func(i int) int {
		if i < len(n.CharacterStyleOverrides) {
			return n.CharacterStyleOverrides[i]
		}
		return 0
	}

	var runs []Run
	for start := 0; start < len(units); {
		id := overrideAt(start)
		end := start + 1
		for end < len(units) && overrideAt(end) == id {
			end++
		}
		// Never split a surrogate pair between two runs.
		for end < len(units) && utf16.IsSurrogate(rune(units[end])) && units[end] >= 0xDC00 {
			end++
		}
		run := Run{Content: string(utf16.Decode(units[start:end])), Style: t.Style, Color: t.Color, Link: t.Link}
		if override, ok := n.StyleOverrideTable[strconv.Itoa(id)]; ok && id != 0 {
			run.Style = overrideStyle(t.Style, &override)
			if fills := fillsOf(override.Fills, 0, 0); len(fills) > 0 && fills[0].Kind != FillImage {
				run.Color = fills[0].CSS
			}
			if override.Hyperlink != nil {
				run.Link = override.Hyperlink.URL
			}
		}
		// Join with the run before when nothing visible changed between them.
		if last := len(runs) - 1; last >= 0 && runs[last].Style == run.Style && runs[last].Color == run.Color && runs[last].Link == run.Link {
			runs[last].Content += run.Content
		} else {
			runs = append(runs, run)
		}
		start = end
	}
	if len(runs) < 2 {
		return nil
	}
	return runs
}

func (b *builder) instance(n *figma.Node) *Instance {
	inst := &Instance{}
	if comp, ok := b.src.Components[n.ComponentID]; ok {
		inst.Component, inst.Description = comp.Name, comp.Description
		if set, ok := b.src.ComponentSets[comp.ComponentSetID]; ok {
			inst.Component = set.Name
			if set.Description != "" {
				inst.Description = set.Description
			}
		}
	}
	for name, prop := range n.ComponentProperties {
		p := Prop{Name: propName(name), Type: strings.ToLower(strings.ReplaceAll(prop.Type, "_", "-"))}
		switch v := prop.Value.(type) {
		case string:
			p.Value = v
			if prop.Type == "INSTANCE_SWAP" {
				// The value is a component id; only its name helps a reader.
				p.Value = b.componentName(v)
				if p.Value == "" {
					continue
				}
			}
		case bool:
			p.Value = strconv.FormatBool(v)
		default:
			continue
		}
		inst.Props = append(inst.Props, p)
	}
	sort.Slice(inst.Props, func(i, j int) bool { return inst.Props[i].Name < inst.Props[j].Name })
	return inst
}

func (b *builder) componentName(id string) string {
	comp, ok := b.src.Components[id]
	if !ok {
		return ""
	}
	if set, ok := b.src.ComponentSets[comp.ComponentSetID]; ok {
		return set.Name
	}
	return comp.Name
}

// propName drops the "#12:34" id Figma appends to a non-variant property.
func propName(name string) string {
	if i := strings.LastIndexByte(name, '#'); i > 0 {
		return name[:i]
	}
	return name
}

// isIcon reports whether a layer is a vector graphic that should be exported
// as one SVG instead of being listed layer by layer: every leaf is a shape,
// at least one is a true vector path, and there is no text and no picture.
// An auto-layout box that is drawn itself or arranges several children is a
// control or a row (an icon button, a toolbar), not an icon, and neither is
// anything that holds two or more frames, instances or components (a sheet
// of icons): those are left as trees so each icon inside keeps its own line
// and file. Groups do not count, because paths are grouped inside one icon.
func isIcon(n *figma.Node) bool {
	switch n.Type {
	case "VECTOR", "STAR", "REGULAR_POLYGON", "BOOLEAN_OPERATION":
		return !hasImagePaint(n)
	case "FRAME", "GROUP", "INSTANCE", "COMPONENT":
	default:
		return false
	}
	vector, shapes := false, 0
	var walk func(n *figma.Node) bool
	walk = func(n *figma.Node) bool {
		if hasImagePaint(n) {
			return false
		}
		switch n.Type {
		case "VECTOR", "STAR", "REGULAR_POLYGON", "BOOLEAN_OPERATION":
			vector = true
			shapes++
			return true
		case "RECTANGLE", "ELLIPSE", "LINE":
			shapes++
			return true
		case "FRAME", "GROUP", "INSTANCE", "COMPONENT":
			visible, boxes := 0, 0
			for _, child := range n.Children {
				if !child.IsVisible() {
					continue
				}
				visible++
				if child.Type == "FRAME" || child.Type == "INSTANCE" || child.Type == "COMPONENT" {
					boxes++
				}
			}
			// A box that lays several things out, or that is drawn itself,
			// is a piece of interface. So is anything holding two or more
			// boxes: a row or a sheet of icons is not one picture.
			if hasAutoLayout(n) && (visible > 1 || hasVisiblePaint(n.Fills) || hasVisiblePaint(n.Strokes)) {
				return false
			}
			if boxes > 1 {
				return false
			}
			for _, child := range n.Children {
				if child.IsVisible() && !walk(child) {
					return false
				}
			}
			return true
		default:
			return false // text, sections, anything unknown
		}
	}
	return walk(n) && vector && shapes > 0
}

func hasVisiblePaint(paints []figma.Paint) bool {
	for i := range paints {
		if paints[i].IsVisible() && paints[i].OpacityOr1() > 0 {
			return true
		}
	}
	return false
}

func hasImagePaint(n *figma.Node) bool {
	for i := range n.Fills {
		if n.Fills[i].Type == "IMAGE" && n.Fills[i].IsVisible() {
			return true
		}
	}
	return false
}

// iconKey names what an icon looks like, so layers that would export to the
// same picture share one file. Instances of one component are the same
// picture when the instance changed nothing but its size; the size, the
// rotation and the opacity are part of the key because a render bakes them
// in. So is what is painted inside: a parent component can recolour the
// icons it holds (white in a dark button, black in a light one) without the
// icon instance listing an override of its own.
func iconKey(n *figma.Node) string {
	if n.Type != "INSTANCE" || n.ComponentID == "" {
		return "n:" + n.ID
	}
	for _, o := range n.Overrides {
		for _, field := range o.OverriddenFields {
			if field != "width" && field != "height" {
				return "n:" + n.ID
			}
		}
	}
	var w, h float64
	if box := n.AbsoluteBoundingBox; box != nil {
		w, h = box.Width, box.Height
	}
	return "c:" + n.ComponentID + "@" + num(w) + "x" + num(h) + "r" + num(n.Rotation*180/math.Pi) + "o" + num(n.OpacityOr1()) + "p" + paintSignature(n)
}

// paintSignature is a short hash of every visible fill and stroke in a
// layer's subtree, in document order.
func paintSignature(n *figma.Node) string {
	h := fnv.New32a()
	var walk func(n *figma.Node)
	walk = func(n *figma.Node) {
		for _, f := range fillsOf(n.Fills, 0, 0) {
			h.Write([]byte("f" + f.CSS + f.ImageRef + ";"))
		}
		for _, f := range fillsOf(n.Strokes, 0, 0) {
			h.Write([]byte("s" + f.CSS + ";"))
		}
		for _, child := range n.Children {
			if child.IsVisible() {
				walk(child)
			}
		}
	}
	walk(n)
	return strconv.FormatUint(uint64(h.Sum32()), 36)
}
