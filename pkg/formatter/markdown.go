// Package formatter writes a Design as markdown for a coding model to build
// from.
//
// The document is laid out for that reader. Tokens come first and are few.
// Each frame then has a section that stands on its own (its link, size,
// screenshot, files and tree), so a result that was saved to a file and is
// read back in ranges still makes sense from the middle. A value is written
// inline wherever it is used; only the names the designer gave in Figma are
// added beside it. Nothing in the output depends on a clock or on map order:
// the same design gives the same text.
package formatter

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/hellenic-development/figma-extractor/pkg/extractor"
	"github.com/hellenic-development/figma-extractor/pkg/imager"
)

// Doc is what Markdown writes.
type Doc struct {
	FileName     string
	FileKey      string
	LastModified string
	Design       *extractor.Design
	// Assets are the files that were saved, and AssetDir the folder they
	// are in. The folder is written once per frame; the layers refer to
	// their files by name.
	Assets   []imager.Asset
	AssetDir string
	Warnings []string
	// Tree includes each frame's layer tree. Without it the document holds
	// the tokens, the components and the list of frames.
	Tree bool
}

// OutlineDoc is what OutlineMarkdown writes.
type OutlineDoc struct {
	FileName     string
	FileKey      string
	LastModified string
	Outline      *extractor.Outline
	Warnings     []string
}

const legend = "" +
	"Each line is one layer: `kind \"name\" #id WIDTHxHEIGHT`, then its properties. Indentation is nesting. Lengths are in px.\n\n" +
	"- `row`, `col`: auto layout, which maps to flexbox. `gap`, `pad` (one value is all sides, two are vertical then horizontal, four are top, right, bottom, left), `justify` along the direction, `align` across it, `wrap`.\n" +
	"- `w=fill`, `w=hug`, `h=fill`, `h=hug`: how the layer sizes itself in its parent's layout. Without it the layer is fixed at the size shown. Text hugs its content unless it says `fill`.\n" +
	"- `at=x,y`: offset from the parent's top-left corner, given when the parent has no auto layout. `abs` marks a layer taken out of its parent's auto layout.\n" +
	"- `bg` (containers), `fill` (shapes) and `color` (text) are `#RRGGBB`, or `#RRGGBBAA` when not opaque. Several fills are listed top first.\n" +
	"- `{Name}` after a value is the Figma style it comes from. Define each one once, as a CSS variable or a utility class, and reuse it.\n" +
	"- `font=\"family weight size/line-height\"`, then `ls=` for letter spacing and `upper`, `italic`, `underline` when set. The quoted line under a text layer is its content; several quoted lines are runs, each with its own style. A text layer has no name when the name only repeats its content.\n" +
	"- `icon` and `graphic` are vector art collapsed into one layer: use the file in `src`, do not redraw it. `image` is a photo; `src` is the file as it was uploaded and `fit` says how it fills the box. Every `src` is a file name inside the frame's files folder.\n" +
	"- `instance ... of=\"Component\" (Variant=Value)`: an instance of a reusable component. Build the component once.\n" +
	"- `(+N hidden)`: hidden layers that were left out. A line starting with `...` says how many children the depth limit cut and which node id to ask for.\n"

// Markdown writes the design.
func Markdown(doc Doc) string {
	var b strings.Builder
	d := doc.Design

	fmt.Fprintf(&b, "# Figma design: %s\n\n", oneLine(doc.FileName))
	b.WriteString(summaryLine(doc.FileKey, doc.LastModified, d))
	writeWarnings(&b, doc.Warnings)

	if doc.Tree && len(d.Frames) > 0 {
		b.WriteString("## Reading the trees\n\n" + legend + "\n")
	}
	writeTokens(&b, &d.Tokens)
	writeComponents(&b, d.Components)

	owner := frameOfLayer(d)
	for _, frame := range d.Frames {
		writeFrame(&b, doc, frame, owner)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func summaryLine(fileKey, lastModified string, d *extractor.Design) string {
	parts := []string{"File `" + fileKey + "`"}
	if lastModified != "" {
		parts = append(parts, "last modified "+lastModified)
	}
	parts = append(parts, plural(len(d.Frames), "frame"), plural(d.Stats.Layers, "layer"))
	if d.Stats.Hidden > 0 {
		parts = append(parts, plural(d.Stats.Hidden, "hidden layer")+" skipped")
	}
	if d.Stats.Icons > 0 {
		parts = append(parts, plural(d.Stats.Icons, "icon")+" collapsed")
	}
	return strings.Join(parts, " · ") + "\n\n"
}

func writeWarnings(b *strings.Builder, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	b.WriteString("## Warnings\n\n")
	for _, w := range warnings {
		b.WriteString("- " + oneLine(w) + "\n")
	}
	b.WriteString("\n")
}

func writeTokens(b *strings.Builder, t *extractor.Tokens) {
	if len(t.Styles) > 0 {
		b.WriteString("## Styles named in Figma\n\n| Style | Type | Value | Uses |\n|---|---|---|---|\n")
		for _, s := range t.Styles {
			fmt.Fprintf(b, "| %s | %s | `%s` | %d |\n", cell(s.Name), s.Type, cell(s.Value), s.Uses)
		}
		b.WriteString("\n")
	}
	if len(t.Colors) > 0 {
		b.WriteString("## Colours, most used first\n\n| Colour | As fill | As text | As stroke |\n|---|---|---|---|\n")
		for _, c := range t.Colors {
			fmt.Fprintf(b, "| `%s` | %d | %d | %d |\n", c.Value, c.Fill, c.Text, c.Stroke)
		}
		b.WriteString("\n")
	}
	if len(t.Text) > 0 {
		b.WriteString("## Typography, most used first\n\n")
		if len(t.Fonts) > 0 {
			b.WriteString("Font families: " + countList(t.Fonts) + "\n\n")
		}
		b.WriteString("| Text style (family weight size/line-height) | Uses |\n|---|---|\n")
		for _, s := range t.Text {
			fmt.Fprintf(b, "| `%s` | %d |\n", cell(s.Value), s.Uses)
		}
		b.WriteString("\n")
	}
	if len(t.Spacing) > 0 || len(t.Radii) > 0 {
		b.WriteString("## Spacing and radii\n\n")
		if len(t.Spacing) > 0 {
			b.WriteString("- Gaps and padding (px, with how often each appears): " + numberList(t.Spacing) + "\n")
		}
		if len(t.Radii) > 0 {
			b.WriteString("- Corner radii (px): " + numberList(t.Radii) + "\n")
		}
		b.WriteString("\n")
	}
	if len(t.Shadows) > 0 {
		b.WriteString("## Shadows\n\n")
		for _, s := range t.Shadows {
			fmt.Fprintf(b, "- `%s` (%d)\n", s.Value, s.Uses)
		}
		b.WriteString("\n")
	}
	if len(t.Gradients) > 0 {
		b.WriteString("## Gradients\n\n")
		for _, g := range t.Gradients {
			fmt.Fprintf(b, "- `%s` (%d)\n", g.Value, g.Uses)
		}
		b.WriteString("\n")
	}
}

func writeComponents(b *strings.Builder, components []extractor.ComponentUse) {
	if len(components) == 0 {
		return
	}
	b.WriteString("## Components used\n\n| Component | Uses | Variants seen | Description |\n|---|---|---|---|\n")
	for _, c := range components {
		fmt.Fprintf(b, "| %s | %d | %s | %s |\n", cell(c.Name), c.Uses, cell(strings.Join(c.Variants, "; ")), cell(c.Description))
	}
	b.WriteString("\n")
}

// frameOfLayer maps every layer id to the frame it sits in, so a file can be
// listed under the frame that uses it.
func frameOfLayer(d *extractor.Design) map[string]string {
	owner := map[string]string{}
	for _, frame := range d.Frames {
		frame.Walk(func(n *extractor.Node) {
			if _, taken := owner[n.ID]; !taken {
				owner[n.ID] = frame.ID
			}
		})
	}
	return owner
}

func writeFrame(b *strings.Builder, doc Doc, frame *extractor.Node, owner map[string]string) {
	fmt.Fprintf(b, "## Frame %s #%s\n\n", quote(frame.Name), frame.ID)

	if frame.Page != "" {
		where := "- Page: " + oneLine(frame.Page)
		if frame.Section != "" {
			where += ", section " + oneLine(frame.Section)
		}
		b.WriteString(where + "\n")
	}
	b.WriteString("- Link: " + FrameLink(doc.FileKey, frame.ID) + "\n")
	if size := sizeOf(frame); size != "" {
		b.WriteString("- Size: " + size + "\n")
	}

	// The files this frame uses. Icons are named on their own lines in the
	// tree, so only their count is given here; the photos and exported
	// layers get a table, because their pixel size is worth knowing.
	counts := map[imager.AssetKind]int{}
	var rows []string
	for _, a := range doc.Assets {
		if len(a.NodeIDs) == 0 || owner[a.NodeIDs[0]] != frame.ID {
			continue
		}
		counts[a.Kind]++
		if a.Kind != imager.AssetImage && a.Kind != imager.AssetExport {
			continue
		}
		pixels := "vector"
		if a.Width > 0 {
			pixels = fmt.Sprintf("%dx%d", a.Width, a.Height)
		}
		layers := cell(a.Name)
		if len(a.NodeIDs) > 1 {
			layers += fmt.Sprintf(" (and %d more)", len(a.NodeIDs)-1)
		}
		rows = append(rows, fmt.Sprintf("| `%s` | %s | %s | %s |", a.File, a.Kind, layers, pixels))
	}
	if doc.AssetDir != "" && len(counts) > 0 {
		var kinds []string
		for _, k := range []imager.AssetKind{imager.AssetScreenshot, imager.AssetSection, imager.AssetImage, imager.AssetIcon, imager.AssetExport} {
			if counts[k] > 0 {
				kinds = append(kinds, plural(counts[k], assetNoun[k][0], assetNoun[k][1]))
			}
		}
		fmt.Fprintf(b, "- Files folder: `%s` (%s)\n", doc.AssetDir, strings.Join(kinds, ", "))
	}
	if frame.Screenshot != "" {
		fmt.Fprintf(b, "- Screenshot: `%s`%s\n", frame.Screenshot, pixelsOf(doc.Assets, frame.Screenshot))
	}
	if len(frame.SectionShots) > 0 {
		b.WriteString("- Screenshots of its parts, top to bottom: `" + strings.Join(frame.SectionShots, "`, `") + "`\n")
	}
	b.WriteString("\n")

	if len(rows) > 0 {
		b.WriteString("### Photos and exported layers\n\n| File | Kind | Layer | Pixels |\n|---|---|---|---|\n" + strings.Join(rows, "\n") + "\n\n")
	}

	if doc.Tree {
		b.WriteString("### Tree\n\n```\n")
		writeNode(b, frame, 0)
		b.WriteString("```\n\n")
	}
}

var assetNoun = map[imager.AssetKind][2]string{
	imager.AssetScreenshot: {"screenshot", "screenshots"},
	imager.AssetSection:    {"part screenshot", "part screenshots"},
	imager.AssetImage:      {"photo", "photos"},
	imager.AssetIcon:       {"icon", "icons"},
	imager.AssetExport:     {"exported layer", "exported layers"},
}

// FrameLink is the link that opens a layer in Figma.
func FrameLink(fileKey, nodeID string) string {
	return "https://www.figma.com/design/" + fileKey + "?node-id=" + strings.ReplaceAll(nodeID, ":", "-")
}

func pixelsOf(assets []imager.Asset, file string) string {
	for _, a := range assets {
		if a.File == file && a.Width > 0 {
			return fmt.Sprintf(" (%dx%d px)", a.Width, a.Height)
		}
	}
	return ""
}

func sizeOf(n *extractor.Node) string {
	if n.W == 0 && n.H == 0 {
		return ""
	}
	return num(n.W) + "x" + num(n.H)
}

// nameRepeatsContent reports whether a text layer's name is only its own
// content, which is how Figma names a text layer nobody renamed.
func nameRepeatsContent(name, content string) bool {
	name = strings.Join(strings.Fields(name), " ")
	return name == "" || strings.HasPrefix(strings.Join(strings.Fields(content), " "), name)
}

// writeNode writes one layer on one line, its text under it, then its
// children.
func writeNode(b *strings.Builder, n *extractor.Node, depth int) {
	indent := strings.Repeat("  ", depth)
	parts := []string{string(n.Kind)}
	if n.Text == nil || !nameRepeatsContent(n.Name, n.Text.Content) {
		parts = append(parts, quote(n.Name))
	}
	parts = append(parts, "#"+n.ID)
	if size := sizeOf(n); size != "" {
		parts = append(parts, size)
	}
	parts = append(parts, props(n)...)
	b.WriteString(indent + strings.Join(parts, " ") + "\n")

	if t := n.Text; t != nil {
		if len(t.Runs) == 0 {
			b.WriteString(indent + "  " + quote(t.Content) + "\n")
		}
		for _, run := range t.Runs {
			line := quote(run.Content)
			if s := run.Style.String(); s != "" {
				line += " " + attr("font", s, "")
			}
			if run.Color != "" {
				line += " color=" + run.Color
			}
			if run.Link != "" {
				line += " " + attr("link", run.Link, "")
			}
			b.WriteString(indent + "  " + line + "\n")
		}
	}
	for _, child := range n.Children {
		writeNode(b, child, depth+1)
	}
	if n.Omitted > 0 {
		fmt.Fprintf(b, "%s  ... %s not shown (depth limit). Ask for node %s to see them.\n", indent, plural(n.Omitted, "more child", "more children"), n.ID)
	}
}

// attr writes key=value and adds the Figma style the value comes from in
// braces. The value is kept on its line, and quoted when it holds a space or
// a quote: a link or a style name is text a designer controls, and must not
// be able to end the line it is written on.
func attr(key, value, style string) string {
	if value = oneLine(value); strings.ContainsAny(value, " \t\"") {
		value = quote(value)
	}
	out := key + "=" + value
	if style != "" {
		out += "{" + oneLine(style) + "}"
	}
	return out
}

func isContainerKind(k extractor.Kind) bool {
	switch k {
	case extractor.KindFrame, extractor.KindGroup, extractor.KindSection, extractor.KindComponent, extractor.KindComponentSet, extractor.KindInstance:
		return true
	}
	return false
}

func props(n *extractor.Node) []string {
	var p []string

	// Only the variant properties are written: a boolean or a text property
	// shows up in the tree itself, as a layer that is there or a text that
	// says something.
	if inst := n.Instance; inst != nil && inst.Component != "" {
		of := "of=" + quote(inst.Component)
		var variants []string
		for _, prop := range inst.Props {
			if prop.Type == "variant" {
				variants = append(variants, oneLine(prop.Name)+"="+oneLine(prop.Value))
			}
		}
		if len(variants) > 0 {
			of += " (" + strings.Join(variants, ", ") + ")"
		}
		p = append(p, of)
	}

	if n.Absolute {
		p = append(p, "abs")
	}
	if n.Placed {
		p = append(p, "at="+num(n.X)+","+num(n.Y))
	}

	if l := n.Layout; l != nil && !n.Icon {
		p = append(p, l.Direction)
		if l.Wrap {
			p = append(p, "wrap")
		}
		if l.Gap != 0 {
			p = append(p, "gap="+num(l.Gap))
		}
		if l.Wrap && l.CrossGap != 0 {
			p = append(p, "gap-cross="+num(l.CrossGap))
		}
		if pad := padding(l.Padding); pad != "" {
			p = append(p, "pad="+pad)
		}
		if l.Justify != "" {
			p = append(p, "justify="+l.Justify)
		}
		if l.Align != "" {
			p = append(p, "align="+l.Align)
		}
	}
	// Text hugs its content by default, so only `fill` says something.
	if n.SizeH == extractor.SizingFill || (n.SizeH == extractor.SizingHug && n.Text == nil) {
		p = append(p, "w="+string(n.SizeH))
	}
	if n.SizeV == extractor.SizingFill || (n.SizeV == extractor.SizingHug && n.Text == nil) {
		p = append(p, "h="+string(n.SizeV))
	}

	// A collapsed icon is its file. What is painted inside it is in the
	// SVG, and a render already has the layer's own rotation and opacity.
	if n.Icon {
		switch {
		case n.Src != "":
			p = append(p, attr("src", n.Src, ""))
		default:
			if n.Opacity < 0.999 {
				p = append(p, "opacity="+num(n.Opacity))
			}
			if n.Rotation != 0 {
				p = append(p, "rotate="+num(n.Rotation))
			}
		}
		return p
	}

	p = append(p, paintProps(n)...)
	if n.Src != "" {
		p = append(p, attr("src", n.Src, ""))
	} else if n.ExportFormat != "" {
		p = append(p, "export="+n.ExportFormat)
	}

	if r := radius(n.Radius); r != "" {
		p = append(p, "radius="+r)
	}
	if len(n.Shadows) > 0 {
		p = append(p, attr("shadow", strings.Join(n.Shadows, ", "), n.EffectStyle))
	}
	if n.LayerBlur > 0 {
		p = append(p, "blur="+num(n.LayerBlur))
	}
	if n.BackdropBlur > 0 {
		p = append(p, "backdrop-blur="+num(n.BackdropBlur))
	}
	if n.Opacity < 0.999 {
		p = append(p, "opacity="+num(n.Opacity))
	}
	if n.Rotation != 0 {
		p = append(p, "rotate="+num(n.Rotation))
	}
	if n.BlendMode != "" {
		p = append(p, "blend="+n.BlendMode)
	}
	if n.Clip {
		p = append(p, "clip")
	}
	if n.Mask {
		p = append(p, "mask")
	}

	if t := n.Text; t != nil {
		if len(t.Runs) == 0 {
			if s := t.Style.String(); s != "" {
				p = append(p, attr("font", s, t.StyleName))
			}
			if t.Color != "" {
				p = append(p, attr("color", t.Color, n.FillStyle))
			}
			if t.Link != "" {
				p = append(p, attr("link", t.Link, ""))
			}
		} else if t.StyleName != "" {
			p = append(p, "font={"+oneLine(t.StyleName)+"}")
		}
		if t.Align != "" {
			p = append(p, "text-align="+t.Align)
		}
	}

	if n.Hidden > 0 {
		p = append(p, fmt.Sprintf("(+%d hidden)", n.Hidden))
	}
	return p
}

// paintProps writes the fills and the stroke. A text layer's fill is its
// text colour and is written with its font instead.
func paintProps(n *extractor.Node) []string {
	var p []string
	if n.Text == nil {
		key := "fill"
		if isContainerKind(n.Kind) {
			key = "bg"
		}
		var colours []string
		for _, f := range n.Fills {
			switch f.Kind {
			case extractor.FillImage:
				if n.Src == "" {
					p = append(p, "image-fill")
				}
				p = append(p, "fit="+f.Fit)
			default:
				colours = append(colours, f.CSS)
			}
		}
		if len(colours) > 0 {
			p = append(p, attr(key, strings.Join(colours, ", "), n.FillStyle))
		}
	}
	if len(n.Strokes) > 0 && n.Strokes[0].Kind != extractor.FillImage {
		weight := px(n.StrokeWeight)
		if n.StrokeSides != nil {
			weight = px(n.StrokeSides[0]) + " " + px(n.StrokeSides[1]) + " " + px(n.StrokeSides[2]) + " " + px(n.StrokeSides[3])
		}
		value := weight + " " + n.Strokes[0].CSS
		if n.Dashed {
			value += " dashed"
		}
		if n.StrokeAlign == "outside" || n.StrokeAlign == "center" {
			value += " " + n.StrokeAlign
		}
		p = append(p, attr("border", value, n.StrokeStyle))
	}
	return p
}

func padding(p [4]float64) string {
	switch {
	case p == [4]float64{}:
		return ""
	case p[0] == p[1] && p[1] == p[2] && p[2] == p[3]:
		return num(p[0])
	case p[0] == p[2] && p[1] == p[3]:
		return num(p[0]) + "," + num(p[1])
	default:
		return num(p[0]) + "," + num(p[1]) + "," + num(p[2]) + "," + num(p[3])
	}
}

func radius(r [4]float64) string {
	switch {
	case r == [4]float64{}:
		return ""
	case r[0] == r[1] && r[1] == r[2] && r[2] == r[3]:
		return num(r[0])
	default:
		return num(r[0]) + "," + num(r[1]) + "," + num(r[2]) + "," + num(r[3])
	}
}

// OutlineMarkdown writes the map of a file.
func OutlineMarkdown(doc OutlineDoc) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Figma file outline: %s\n\n", oneLine(doc.FileName))
	parts := []string{"File `" + doc.FileKey + "`"}
	if doc.LastModified != "" {
		parts = append(parts, "last modified "+doc.LastModified)
	}
	b.WriteString(strings.Join(parts, " · ") + "\n\n")
	writeWarnings(&b, doc.Warnings)
	b.WriteString("Each line is `type \"name\" #id`, its size and how many layers sit directly inside. Ask for a frame's design by its id or its link.\n\n")

	for _, page := range doc.Outline.Pages {
		if page.ID == "" {
			b.WriteString("## Selection\n\n")
		} else {
			fmt.Fprintf(&b, "## Page %s #%s\n\n", quote(page.Name), page.ID)
		}
		if len(page.Items) == 0 {
			b.WriteString("Nothing visible on this page.\n\n")
			continue
		}
		for _, item := range page.Items {
			writeOutlineItem(&b, doc.FileKey, item, 0)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func writeOutlineItem(b *strings.Builder, fileKey string, item extractor.OutlineItem, depth int) {
	line := strings.Repeat("  ", depth) + "- " + strings.ToLower(strings.ReplaceAll(item.Type, "_", "-")) + " " + quote(item.Name) + " #" + item.ID
	if item.W != 0 || item.H != 0 {
		line += " · " + num(item.W) + "x" + num(item.H)
	}
	if item.Children > 0 {
		line += " · " + plural(item.Children, "child", "children")
	}
	if depth == 0 {
		line += " · " + FrameLink(fileKey, item.ID)
	}
	b.WriteString(line + "\n")
	for _, child := range item.Items {
		writeOutlineItem(b, fileKey, child, depth+1)
	}
}

// isLineBreak reports whether r ends a line in some reader: the ASCII ones,
// NEL, and the Unicode line and paragraph separators Figma uses for a soft
// break inside a text layer.
func isLineBreak(r rune) bool {
	switch r {
	case '\n', '\v', '\f', 0x0085, 0x2028, 0x2029:
		return true
	}
	return false
}

// quote writes a layer name or text content as one double-quoted line. Line
// breaks of every kind become "\n", so content can never start a line of its
// own and pass for a layer or close the code fence around the tree.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case isLineBreak(r):
			b.WriteString(`\n`)
		case r == '\r':
			// dropped: it only ever sits beside a \n
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// oneLine keeps a value from breaking the line it is written on.
func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == '\r' || isLineBreak(r) }), " ")
}

// cell writes a value into a markdown table cell.
func cell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(oneLine(s), "|", `\|`), "`", "'")
}

func num(f float64) string {
	f = math.Round(f*100) / 100
	if f == 0 {
		return "0"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func px(f float64) string {
	if s := num(f); s != "0" {
		return s + "px"
	}
	return "0"
}

// plural writes "1 frame" or "3 frames". A second form is the plural when
// adding an s is not it.
func plural(n int, one string, many ...string) string {
	if n == 1 {
		return "1 " + one
	}
	if len(many) > 0 {
		return strconv.Itoa(n) + " " + many[0]
	}
	return strconv.Itoa(n) + " " + one + "s"
}

func countList(items []extractor.CountToken) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("%s (%d)", it.Value, it.Uses))
	}
	return strings.Join(parts, ", ")
}

func numberList(items []extractor.NumberToken) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("%s (%d)", num(it.Value), it.Uses))
	}
	return strings.Join(parts, ", ")
}
