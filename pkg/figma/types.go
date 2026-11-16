package figma

// The types in this file mirror what Figma's REST API sends. Two rules of
// that API shape every one of them:
//
//   - A property at its default is left out of the JSON. `visible` is true
//     and `opacity` is 1 unless the key is present, so both are pointers here
//     and are read through IsVisible and OpacityOr1. A plain bool or float
//     would read every visible fill as hidden and every opaque one as
//     transparent.
//   - A map value may be null: a node id that is not in the file, a render
//     that failed. The node map therefore holds pointers.

// FileResponse is the answer of GET /v1/files/:key.
type FileResponse struct {
	Name          string                  `json:"name"`
	LastModified  string                  `json:"lastModified"`
	ThumbnailURL  string                  `json:"thumbnailUrl"`
	Version       string                  `json:"version"`
	Role          string                  `json:"role"`
	EditorType    string                  `json:"editorType"`
	Document      Node                    `json:"document"`
	Components    map[string]Component    `json:"components"`
	ComponentSets map[string]ComponentSet `json:"componentSets"`
	Styles        map[string]Style        `json:"styles"`
	SchemaVersion int                     `json:"schemaVersion"`
}

// NodesResponse is the answer of GET /v1/files/:key/nodes. Nodes is keyed by
// the colon form of the id ("12:34") whatever form the request used, and
// holds nil for an id the file does not have.
type NodesResponse struct {
	Name         string               `json:"name"`
	LastModified string               `json:"lastModified"`
	ThumbnailURL string               `json:"thumbnailUrl"`
	Version      string               `json:"version"`
	Role         string               `json:"role"`
	EditorType   string               `json:"editorType"`
	Nodes        map[string]*NodeData `json:"nodes"`
}

// Missing returns the ids, in the order given, that the file does not have.
func (r *NodesResponse) Missing(ids []string) []string {
	var missing []string
	for _, id := range ids {
		if r.Nodes[id] == nil {
			missing = append(missing, id)
		}
	}
	return missing
}

// NodeData is one requested node with the components and styles its subtree
// refers to.
type NodeData struct {
	Document      Node                    `json:"document"`
	Components    map[string]Component    `json:"components"`
	ComponentSets map[string]ComponentSet `json:"componentSets"`
	Styles        map[string]Style        `json:"styles"`
}

// Component is the metadata of a component an instance in the tree points at.
// A variant carries the id of its set, and its own Name is then the variant
// string ("Type=Primary, Size=Large"), not the family name.
type Component struct {
	Key            string `json:"key"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	ComponentSetID string `json:"componentSetId"`
	Remote         bool   `json:"remote"`
}

// ComponentSet is a family of variants ("Button").
type ComponentSet struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Remote      bool   `json:"remote"`
}

// Style is a named style a node refers to through Node.Styles. StyleType is
// FILL, TEXT, EFFECT or GRID.
type Style struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	StyleType   string `json:"styleType"`
	Remote      bool   `json:"remote"`
}

// Node is one layer of the document tree.
type Node struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Visible  *bool   `json:"visible,omitempty"`
	Children []*Node `json:"children,omitempty"`

	// Geometry. Rotation is in radians, as the API sends it.
	AbsoluteBoundingBox  *Rectangle        `json:"absoluteBoundingBox,omitempty"`
	AbsoluteRenderBounds *Rectangle        `json:"absoluteRenderBounds,omitempty"`
	Constraints          *LayoutConstraint `json:"constraints,omitempty"`
	Rotation             float64           `json:"rotation,omitempty"`

	// Paint.
	Opacity                 *float64       `json:"opacity,omitempty"`
	BlendMode               string         `json:"blendMode,omitempty"`
	BackgroundColor         *Color         `json:"backgroundColor,omitempty"`
	Fills                   []Paint        `json:"fills,omitempty"`
	Strokes                 []Paint        `json:"strokes,omitempty"`
	StrokeWeight            float64        `json:"strokeWeight,omitempty"`
	StrokeAlign             string         `json:"strokeAlign,omitempty"`
	StrokeDashes            []float64      `json:"strokeDashes,omitempty"`
	IndividualStrokeWeights *StrokeWeights `json:"individualStrokeWeights,omitempty"`
	CornerRadius            float64        `json:"cornerRadius,omitempty"`
	RectangleCornerRadii    []float64      `json:"rectangleCornerRadii,omitempty"`
	Effects                 []Effect       `json:"effects,omitempty"`
	ClipsContent            bool           `json:"clipsContent,omitempty"`
	IsMask                  bool           `json:"isMask,omitempty"`

	// Text (TEXT nodes). CharacterStyleOverrides holds, per UTF-16 code unit
	// of Characters, a key into StyleOverrideTable; 0 and a missing tail mean
	// the node's own Style.
	Characters              string               `json:"characters,omitempty"`
	Style                   *TypeStyle           `json:"style,omitempty"`
	CharacterStyleOverrides []int                `json:"characterStyleOverrides,omitempty"`
	StyleOverrideTable      map[string]TypeStyle `json:"styleOverrideTable,omitempty"`

	// Auto layout, on the container.
	LayoutMode            string  `json:"layoutMode,omitempty"`
	LayoutWrap            string  `json:"layoutWrap,omitempty"`
	PrimaryAxisSizingMode string  `json:"primaryAxisSizingMode,omitempty"`
	CounterAxisSizingMode string  `json:"counterAxisSizingMode,omitempty"`
	PrimaryAxisAlignItems string  `json:"primaryAxisAlignItems,omitempty"`
	CounterAxisAlignItems string  `json:"counterAxisAlignItems,omitempty"`
	PaddingLeft           float64 `json:"paddingLeft,omitempty"`
	PaddingRight          float64 `json:"paddingRight,omitempty"`
	PaddingTop            float64 `json:"paddingTop,omitempty"`
	PaddingBottom         float64 `json:"paddingBottom,omitempty"`
	ItemSpacing           float64 `json:"itemSpacing,omitempty"`
	CounterAxisSpacing    float64 `json:"counterAxisSpacing,omitempty"`

	// Auto layout, on the child.
	LayoutPositioning      string  `json:"layoutPositioning,omitempty"`
	LayoutAlign            string  `json:"layoutAlign,omitempty"`
	LayoutGrow             float64 `json:"layoutGrow,omitempty"`
	LayoutSizingHorizontal string  `json:"layoutSizingHorizontal,omitempty"`
	LayoutSizingVertical   string  `json:"layoutSizingVertical,omitempty"`
	MinWidth               float64 `json:"minWidth,omitempty"`
	MaxWidth               float64 `json:"maxWidth,omitempty"`
	MinHeight              float64 `json:"minHeight,omitempty"`
	MaxHeight              float64 `json:"maxHeight,omitempty"`

	// Components and styles.
	ComponentID         string                       `json:"componentId,omitempty"`
	ComponentProperties map[string]ComponentProperty `json:"componentProperties,omitempty"`
	Overrides           []Override                   `json:"overrides,omitempty"`
	Styles              map[string]string            `json:"styles,omitempty"`

	ExportSettings []ExportSetting `json:"exportSettings,omitempty"`
}

// IsVisible reports whether the layer is shown. Figma leaves `visible` out
// when it is true.
func (n *Node) IsVisible() bool { return n.Visible == nil || *n.Visible }

// OpacityOr1 is the layer's opacity, 1 when Figma left the key out.
func (n *Node) OpacityOr1() float64 {
	if n.Opacity == nil {
		return 1
	}
	return *n.Opacity
}

// Color is an RGBA colour with every channel between 0 and 1.
type Color struct {
	R float64 `json:"r"`
	G float64 `json:"g"`
	B float64 `json:"b"`
	A float64 `json:"a"`
}

// Paint is one fill or stroke. Type is SOLID, GRADIENT_LINEAR,
// GRADIENT_RADIAL, GRADIENT_ANGULAR, GRADIENT_DIAMOND, IMAGE and a few rarer
// ones.
type Paint struct {
	Type      string   `json:"type"`
	Visible   *bool    `json:"visible,omitempty"`
	Opacity   *float64 `json:"opacity,omitempty"`
	Color     *Color   `json:"color,omitempty"`
	BlendMode string   `json:"blendMode,omitempty"`

	// Gradients. The handles are in the layer's own 0..1 box: start, end,
	// and a third that sets the width.
	GradientHandlePositions []Vector    `json:"gradientHandlePositions,omitempty"`
	GradientStops           []ColorStop `json:"gradientStops,omitempty"`

	// Images. ImageRef is the key into Client.ImageFills.
	ImageRef  string `json:"imageRef,omitempty"`
	ScaleMode string `json:"scaleMode,omitempty"`
}

// IsVisible reports whether the paint is drawn.
func (p *Paint) IsVisible() bool { return p.Visible == nil || *p.Visible }

// OpacityOr1 is the paint's own opacity, 1 when Figma left the key out. It
// multiplies with the alpha of the paint's colour.
func (p *Paint) OpacityOr1() float64 {
	if p.Opacity == nil {
		return 1
	}
	return *p.Opacity
}

// ColorStop is one stop of a gradient.
type ColorStop struct {
	Position float64 `json:"position"`
	Color    Color   `json:"color"`
}

// Effect is a shadow or a blur. Type is DROP_SHADOW, INNER_SHADOW,
// LAYER_BLUR or BACKGROUND_BLUR.
type Effect struct {
	Type      string  `json:"type"`
	Visible   *bool   `json:"visible,omitempty"`
	Radius    float64 `json:"radius,omitempty"`
	Color     *Color  `json:"color,omitempty"`
	Offset    *Vector `json:"offset,omitempty"`
	Spread    float64 `json:"spread,omitempty"`
	BlendMode string  `json:"blendMode,omitempty"`
}

// IsVisible reports whether the effect is on.
func (e *Effect) IsVisible() bool { return e.Visible == nil || *e.Visible }

// Vector is a 2D point or offset.
type Vector struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// StrokeWeights is a stroke that differs per side.
type StrokeWeights struct {
	Top    float64 `json:"top"`
	Right  float64 `json:"right"`
	Bottom float64 `json:"bottom"`
	Left   float64 `json:"left"`
}

// TypeStyle is the text styling of a TEXT node, or of one run inside it when
// it comes from Node.StyleOverrideTable (where only the changed keys are
// set, Fills among them when the run has its own colour).
type TypeStyle struct {
	FontFamily          string  `json:"fontFamily"`
	FontPostScriptName  string  `json:"fontPostScriptName"`
	FontStyle           string  `json:"fontStyle"`
	FontWeight          float64 `json:"fontWeight"`
	FontSize            float64 `json:"fontSize"`
	Italic              *bool   `json:"italic"`
	TextCase            string  `json:"textCase"`
	TextDecoration      string  `json:"textDecoration"`
	TextAlignHorizontal string  `json:"textAlignHorizontal"`
	TextAlignVertical   string  `json:"textAlignVertical"`
	LetterSpacing       float64 `json:"letterSpacing"`
	LineHeightPx        float64 `json:"lineHeightPx"`
	LineHeightPercent   float64 `json:"lineHeightPercent"`
	LineHeightUnit      string  `json:"lineHeightUnit"`
	ParagraphSpacing    float64 `json:"paragraphSpacing"`
	Fills               []Paint `json:"fills,omitempty"`
	Hyperlink           *struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"hyperlink,omitempty"`
}

// Rectangle is a box on the canvas, in canvas coordinates.
type Rectangle struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// LayoutConstraint says how a layer follows its parent when the parent is
// resized.
type LayoutConstraint struct {
	Vertical   string `json:"vertical"`
	Horizontal string `json:"horizontal"`
}

// ComponentProperty is one property an instance sets. Type is VARIANT,
// BOOLEAN, TEXT or INSTANCE_SWAP; Value is a string or a bool to match.
type ComponentProperty struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

// Override names the fields an instance changed on one of its layers.
type Override struct {
	ID               string   `json:"id"`
	OverriddenFields []string `json:"overriddenFields"`
}

// ExportSetting is an export the designer set up on a layer.
type ExportSetting struct {
	Suffix     string `json:"suffix"`
	Format     string `json:"format"`
	Constraint struct {
		Type  string  `json:"type"`
		Value float64 `json:"value"`
	} `json:"constraint"`
}

// ImageResponse is the answer of GET /v1/images/:key: a render URL per node
// id, empty for a node Figma could not render.
type ImageResponse struct {
	Err    *string           `json:"err"`
	Images map[string]string `json:"images"`
}

// FileImagesResponse is the answer of GET /v1/files/:key/images. The map
// sits under "meta", unlike the render endpoint's.
type FileImagesResponse struct {
	Error  bool `json:"error"`
	Status int  `json:"status"`
	Meta   struct {
		Images map[string]string `json:"images"`
	} `json:"meta"`
}
