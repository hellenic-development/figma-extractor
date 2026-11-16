// Package extractor turns a decoded Figma response into a Design: the frames
// as a tree a coding model can build from, and the design tokens the frames
// actually use. It makes no requests and touches no files, so the same
// response always gives the same Design.
package extractor

import (
	"maps"
	"slices"

	"github.com/hellenic-development/figma-extractor/pkg/figma"
)

// Source is what the extractor reads: the nodes asked for, and the name maps
// their subtrees refer to.
type Source struct {
	Roots         []*figma.Node
	Styles        map[string]figma.Style
	Components    map[string]figma.Component
	ComponentSets map[string]figma.ComponentSet
}

// SourceFromNodes gathers the nodes of a nodes response in the order of ids,
// merging the name maps each one came with. It also returns the ids the
// response does not hold.
//
// An id is looked for among the nodes that were requested first, then inside
// their subtrees. The second case is for a caller that fetched a page and
// wants some of the frames on it: the answer already holds them, so they
// need no request of their own.
func SourceFromNodes(resp *figma.NodesResponse, ids []string) (Source, []string) {
	src := Source{
		Styles:        map[string]figma.Style{},
		Components:    map[string]figma.Component{},
		ComponentSets: map[string]figma.ComponentSet{},
	}
	var missing []string
	var inside map[string]nestedNode // built on the first id that is not a top-level key
	for _, id := range figma.NormalizeNodeIDs(ids) {
		data := resp.Nodes[id]
		root := (*figma.Node)(nil)
		if data != nil {
			root = &data.Document
		} else {
			if inside == nil {
				inside = indexNested(resp)
			}
			if hit, ok := inside[id]; ok {
				root, data = hit.node, hit.data
			}
		}
		if root == nil {
			missing = append(missing, id)
			continue
		}
		src.Roots = append(src.Roots, root)
		maps.Copy(src.Styles, data.Styles)
		maps.Copy(src.Components, data.Components)
		maps.Copy(src.ComponentSets, data.ComponentSets)
	}
	return src, missing
}

// nestedNode is a node found inside a requested node's subtree, with the
// name maps of the answer it came in.
type nestedNode struct {
	node *figma.Node
	data *figma.NodeData
}

// indexNested maps every node below the requested ones to where it is. The
// requested nodes are walked in id order so that an id present under two of
// them (a page and one of its frames, both asked for) always resolves to the
// same one.
func indexNested(resp *figma.NodesResponse) map[string]nestedNode {
	index := map[string]nestedNode{}
	for _, key := range slices.Sorted(maps.Keys(resp.Nodes)) {
		data := resp.Nodes[key]
		if data == nil {
			continue
		}
		var walk func(n *figma.Node)
		walk = func(n *figma.Node) {
			for _, child := range n.Children {
				if _, taken := index[child.ID]; !taken {
					index[child.ID] = nestedNode{node: child, data: data}
				}
				walk(child)
			}
		}
		walk(&data.Document)
	}
	return index
}

// SourceFromFile is the source for a whole file.
func SourceFromFile(resp *figma.FileResponse) Source {
	return Source{
		Roots:         []*figma.Node{&resp.Document},
		Styles:        resp.Styles,
		Components:    resp.Components,
		ComponentSets: resp.ComponentSets,
	}
}

// Options shapes the extraction.
type Options struct {
	// Depth is how many levels below each frame the tree keeps. A layer at
	// the limit reports how many children it has instead of listing them.
	// 0 keeps everything.
	Depth int
}

// Design is the result of an extraction.
type Design struct {
	// Frames are the top-level frames: a page given as a root is expanded
	// into the frames on it.
	Frames     []*Node
	Tokens     Tokens
	Components []ComponentUse
	Stats      Stats
}

// Stats counts what the extraction walked.
type Stats struct {
	Layers int // visible layers kept in the trees
	Hidden int // hidden layers skipped, with everything under them
	Icons  int // vector groups collapsed to one line
}

// Kind is what a layer is to someone building it, which is not always its
// Figma type: a rectangle with a photo in it is an image, a frame of vector
// paths is an icon.
type Kind string

const (
	KindFrame        Kind = "frame"
	KindGroup        Kind = "group"
	KindSection      Kind = "section"
	KindComponent    Kind = "component"
	KindComponentSet Kind = "component-set"
	KindInstance     Kind = "instance"
	KindText         Kind = "text"
	KindImage        Kind = "image"
	KindIcon         Kind = "icon"
	KindGraphic      Kind = "graphic"
	KindRect         Kind = "rect"
	KindEllipse      Kind = "ellipse"
	KindLine         Kind = "line"
)

// Sizing is how a layer sizes itself along one axis inside auto layout.
type Sizing string

const (
	SizingFixed Sizing = "fixed"
	SizingHug   Sizing = "hug"
	SizingFill  Sizing = "fill"
)

// FillKind is the kind of a paint.
type FillKind string

const (
	FillSolid    FillKind = "solid"
	FillGradient FillKind = "gradient"
	FillImage    FillKind = "image"
)

// Fill is one visible paint of a layer.
type Fill struct {
	Kind     FillKind
	CSS      string // a colour or a gradient; empty for an image
	ImageRef string // the image's key in Figma, for an image
	Fit      string // cover, contain, tile or stretch, for an image
}

// Layout is a container's auto layout.
type Layout struct {
	Direction string     // row, col or grid
	Wrap      bool       // children wrap onto more lines
	Gap       float64    // between children along the direction
	CrossGap  float64    // between lines when wrapping
	Padding   [4]float64 // top, right, bottom, left
	Justify   string     // start, center, end or between, along the direction
	Align     string     // start, center, end or baseline, across it
}

// TextStyle is the typography of a text layer or of one run inside it.
type TextStyle struct {
	Family        string
	Weight        int
	Size          float64
	LineHeight    float64 // pixels; 0 means the font's own
	LetterSpacing float64 // pixels
	Case          string  // upper, lower, title or small-caps
	Italic        bool
	Decoration    string // underline or strike
}

// Run is a stretch of a text layer with its own style or colour.
type Run struct {
	Content string
	Style   TextStyle
	Color   string
	Link    string
}

// Text is the content and typography of a text layer.
type Text struct {
	Content   string
	Style     TextStyle
	StyleName string // the Figma text style applied, if any
	Color     string
	Align     string // center, right or justified; empty for left
	Link      string
	// Runs is set only when the layer mixes styles, colours or links. The
	// runs then cover Content end to end.
	Runs []Run
}

// Prop is one property an instance sets on its component.
type Prop struct {
	Name  string
	Type  string // variant, boolean, text or instance-swap
	Value string
}

// Instance says which component a layer is an instance of.
type Instance struct {
	Component   string // the set's name for a variant, else the component's
	Description string
	Props       []Prop
}

// Node is one visible layer.
type Node struct {
	ID   string
	Name string
	Type string // Figma's own type
	Kind Kind

	// Page and Section name where a top-level frame sits.
	Page    string
	Section string

	// Geometry. X and Y are the offset from the parent's top-left and mean
	// something only when Placed: the parent has no auto layout, or the
	// layer is taken out of it (Absolute).
	W, H     float64
	X, Y     float64
	Placed   bool
	Absolute bool
	SizeH    Sizing
	SizeV    Sizing
	Rotation float64 // degrees, as Figma reports it

	Layout *Layout

	Fills        []Fill // top first, as CSS lists backgrounds
	FillStyle    string // the Figma style the fills come from, if any
	Strokes      []Fill
	StrokeStyle  string
	StrokeWeight float64
	StrokeSides  *[4]float64 // top, right, bottom, left, when they differ
	StrokeAlign  string      // inside, center or outside
	Dashed       bool
	Radius       [4]float64 // top-left, top-right, bottom-right, bottom-left
	Opacity      float64    // 1 is opaque
	BlendMode    string     // set only when it is not the normal one

	Shadows      []string // CSS box-shadow values
	EffectStyle  string
	LayerBlur    float64
	BackdropBlur float64

	Clip bool // children are clipped to the box
	Mask bool // the layer masks the siblings after it

	Text     *Text
	Instance *Instance

	// Icon is true for a vector graphic collapsed into this one line. Its
	// paths are not listed: it is meant to be exported as one SVG. Layers
	// that are the same component with nothing changed share an AssetKey,
	// so one file serves them all.
	Icon     bool
	AssetKey string
	// ExportFormat is svg, png, jpg or pdf when the designer set the layer
	// up for export.
	ExportFormat string

	Hidden   int // hidden children skipped
	Omitted  int // visible children cut by Options.Depth
	Children []*Node

	// Filled in by the imager, as file names inside the asset folder.
	Src          string   // the layer's own file: a photo, an icon, an export
	Screenshot   string   // a render of the whole frame
	SectionShots []string // renders of a tall frame's direct children
}

// ImageRefs lists the image keys of the layer's visible image fills.
func (n *Node) ImageRefs() []string {
	var refs []string
	for _, f := range n.Fills {
		if f.Kind == FillImage && f.ImageRef != "" {
			refs = append(refs, f.ImageRef)
		}
	}
	return refs
}

// Walk calls fn for n and every layer under it, parents first.
func (n *Node) Walk(fn func(*Node)) {
	fn(n)
	for _, child := range n.Children {
		child.Walk(fn)
	}
}

// Build extracts a Design from a source.
func Build(src Source, opts Options) *Design {
	b := &builder{src: src, opts: opts, tally: newTally(), seen: map[string]bool{}}
	d := &Design{}
	for _, root := range src.Roots {
		b.frames(d, root, "", "")
	}
	d.Tokens = b.tally.tokens()
	d.Components = b.tally.componentUses()
	d.Stats = b.stats
	return d
}

// frames adds root to the design as a frame, or, when it is a document, a
// page or a section, the frames inside it.
func (b *builder) frames(d *Design, root *figma.Node, page, section string) {
	if !root.IsVisible() {
		b.stats.Hidden++
		return
	}
	switch root.Type {
	case "DOCUMENT":
		for _, child := range root.Children {
			b.frames(d, child, "", "")
		}
	case "CANVAS":
		for _, child := range root.Children {
			b.frames(d, child, root.Name, "")
		}
	case "SECTION":
		for _, child := range root.Children {
			b.frames(d, child, page, root.Name)
		}
	default:
		// A page and one of its frames, both asked for, is that frame once.
		if b.seen[root.ID] {
			return
		}
		b.seen[root.ID] = true
		frame := b.node(root, nil, 0)
		frame.Page, frame.Section = page, section
		d.Frames = append(d.Frames, frame)
	}
}
