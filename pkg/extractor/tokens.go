package extractor

import (
	"sort"
	"strings"
)

// Tokens are the design values the frames use. Nothing is guessed from layer
// names and nothing is renamed onto a made-up scale: Styles are the names the
// designer gave in Figma, and every other list is the raw value with how
// often it appears, most used first.
type Tokens struct {
	Styles    []StyleToken
	Colors    []ColorToken
	Text      []CountToken // text styles, written as TextStyle.String
	Fonts     []CountToken // font families
	Spacing   []NumberToken
	Radii     []NumberToken
	Shadows   []CountToken
	Gradients []CountToken
}

// StyleToken is a named Figma style with the value it has in these frames.
type StyleToken struct {
	Name  string
	Type  string // fill, stroke, text or effect
	Value string
	Uses  int
}

// ColorToken is a solid colour and where it is used.
type ColorToken struct {
	Value  string
	Fill   int
	Text   int
	Stroke int
}

// Uses is how many layers use the colour in any role.
func (c ColorToken) Uses() int { return c.Fill + c.Text + c.Stroke }

// CountToken is a value and how many layers use it.
type CountToken struct {
	Value string
	Uses  int
}

// NumberToken is a length in pixels and how many times it appears.
type NumberToken struct {
	Value float64
	Uses  int
}

// ComponentUse is a component the frames hold instances of.
type ComponentUse struct {
	Name        string
	Description string
	Uses        int
	Variants    []string // the distinct variant settings seen, "Type=Primary, Size=Large"
}

type tally struct {
	styles     map[string]*StyleToken
	colors     map[string]*ColorToken
	text       map[string]int
	fonts      map[string]int
	spacing    map[float64]int
	radii      map[float64]int
	shadows    map[string]int
	gradients  map[string]int
	components map[string]*componentTally
}

type componentTally struct {
	description string
	uses        int
	variants    map[string]bool
}

func newTally() *tally {
	return &tally{
		styles:     map[string]*StyleToken{},
		colors:     map[string]*ColorToken{},
		text:       map[string]int{},
		fonts:      map[string]int{},
		spacing:    map[float64]int{},
		radii:      map[float64]int{},
		shadows:    map[string]int{},
		gradients:  map[string]int{},
		components: map[string]*componentTally{},
	}
}

func (t *tally) color(value string) *ColorToken {
	c := t.colors[value]
	if c == nil {
		c = &ColorToken{Value: value}
		t.colors[value] = c
	}
	return c
}

func (t *tally) style(kind, name, value string) {
	if name == "" || value == "" {
		return
	}
	key := kind + "\x00" + name
	s := t.styles[key]
	if s == nil {
		s = &StyleToken{Name: name, Type: kind, Value: value}
		t.styles[key] = s
	}
	s.Uses++
}

// node counts what one layer uses.
func (t *tally) node(n *Node) {
	for _, f := range n.Fills {
		switch f.Kind {
		case FillSolid:
			if n.Text != nil {
				t.color(f.CSS).Text++
			} else {
				t.color(f.CSS).Fill++
			}
		case FillGradient:
			t.gradients[f.CSS]++
		}
	}
	for _, f := range n.Strokes {
		if f.Kind == FillSolid {
			t.color(f.CSS).Stroke++
		}
	}
	if len(n.Fills) > 0 && n.Fills[0].Kind != FillImage {
		t.style("fill", n.FillStyle, n.Fills[0].CSS)
	}
	if len(n.Strokes) > 0 && n.Strokes[0].Kind != FillImage {
		t.style("stroke", n.StrokeStyle, n.Strokes[0].CSS)
	}
	for _, shadow := range n.Shadows {
		t.shadows[shadow]++
	}
	t.style("effect", n.EffectStyle, strings.Join(n.Shadows, ", "))

	if text := n.Text; text != nil {
		if s := text.Style.String(); s != "" {
			t.text[s]++
			t.fonts[text.Style.Family]++
			t.style("text", text.StyleName, s)
		}
		for _, run := range text.Runs {
			if run.Style != text.Style {
				if s := run.Style.String(); s != "" {
					t.text[s]++
				}
			}
			if run.Color != "" && run.Color != text.Color {
				t.color(run.Color).Text++
			}
		}
	}

	if l := n.Layout; l != nil {
		for _, v := range []float64{l.Gap, l.CrossGap, l.Padding[0], l.Padding[1], l.Padding[2], l.Padding[3]} {
			if v > 0 {
				t.spacing[round2(v)]++
			}
		}
	}
	seen := map[float64]bool{}
	for _, r := range n.Radius {
		if r = round2(r); r > 0 && !seen[r] {
			seen[r] = true
			t.radii[r]++
		}
	}

	if inst := n.Instance; inst != nil && inst.Component != "" {
		c := t.components[inst.Component]
		if c == nil {
			c = &componentTally{description: inst.Description, variants: map[string]bool{}}
			t.components[inst.Component] = c
		}
		c.uses++
		var variant []string
		for _, p := range inst.Props {
			if p.Type == "variant" {
				variant = append(variant, p.Name+"="+p.Value)
			}
		}
		if len(variant) > 0 {
			c.variants[strings.Join(variant, ", ")] = true
		}
	}
}

// styleOrder puts the style types in the order a stylesheet is written in.
var styleOrder = map[string]int{"fill": 0, "stroke": 1, "text": 2, "effect": 3}

func (t *tally) tokens() Tokens {
	var out Tokens

	for _, s := range t.styles {
		out.Styles = append(out.Styles, *s)
	}
	sort.Slice(out.Styles, func(i, j int) bool {
		a, b := out.Styles[i], out.Styles[j]
		if styleOrder[a.Type] != styleOrder[b.Type] {
			return styleOrder[a.Type] < styleOrder[b.Type]
		}
		return a.Name < b.Name
	})

	for _, c := range t.colors {
		out.Colors = append(out.Colors, *c)
	}
	sort.Slice(out.Colors, func(i, j int) bool {
		a, b := out.Colors[i], out.Colors[j]
		if a.Uses() != b.Uses() {
			return a.Uses() > b.Uses()
		}
		return a.Value < b.Value
	})

	out.Text = counted(t.text)
	out.Fonts = counted(t.fonts)
	out.Shadows = counted(t.shadows)
	out.Gradients = counted(t.gradients)
	out.Spacing = numbered(t.spacing)
	out.Radii = numbered(t.radii)
	return out
}

func (t *tally) componentUses() []ComponentUse {
	out := make([]ComponentUse, 0, len(t.components))
	for name, c := range t.components {
		use := ComponentUse{Name: name, Description: c.description, Uses: c.uses}
		for v := range c.variants {
			use.Variants = append(use.Variants, v)
		}
		sort.Strings(use.Variants)
		out = append(out, use)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Uses != out[j].Uses {
			return out[i].Uses > out[j].Uses
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// counted sorts by use, most used first, then by value so equal counts keep
// one order from run to run.
func counted(m map[string]int) []CountToken {
	out := make([]CountToken, 0, len(m))
	for value, uses := range m {
		if value != "" {
			out = append(out, CountToken{Value: value, Uses: uses})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Uses != out[j].Uses {
			return out[i].Uses > out[j].Uses
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// numbered sorts by value, smallest first: a spacing or radius list reads as
// a scale.
func numbered(m map[float64]int) []NumberToken {
	out := make([]NumberToken, 0, len(m))
	for value, uses := range m {
		out = append(out, NumberToken{Value: value, Uses: uses})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	return out
}
