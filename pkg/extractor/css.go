package extractor

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/hellenic-development/figma-extractor/pkg/figma"
)

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// num prints a number the way CSS is written by hand: at most two decimals,
// no trailing zeros, no "-0".
func num(f float64) string {
	f = round2(f)
	if f == 0 {
		return "0"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// px is num with the unit, and a bare 0.
func px(f float64) string {
	if s := num(f); s != "0" {
		return s + "px"
	}
	return "0"
}

// colorCSS writes a colour as #RRGGBB, or #RRGGBBAA when it is not opaque.
// opacity is the paint's own and multiplies with the colour's alpha.
func colorCSS(c figma.Color, opacity float64) string {
	channel := func(v float64) int { return int(math.Round(math.Max(0, math.Min(1, v)) * 255)) }
	hex := fmt.Sprintf("#%02X%02X%02X", channel(c.R), channel(c.G), channel(c.B))
	if alpha := c.A * opacity; alpha < 0.999 {
		hex += fmt.Sprintf("%02X", channel(alpha))
	}
	return hex
}

// cssAngle is the CSS angle of the direction (dx, dy) on the canvas: CSS
// measures clockwise from "to top", and the canvas y axis points down.
func cssAngle(dx, dy float64) float64 {
	return math.Mod(math.Atan2(dx, -dy)*180/math.Pi+360, 360)
}

// gradientCSS writes a gradient paint as a CSS gradient.
//
// Figma places a gradient with handles in the layer's 0..1 box: where it
// starts, where it ends, and a third that sets the width. CSS places one by
// an angle and the box. The two agree only when the handles sit on the
// box's edges, so the handles are measured in pixels (a diagonal across a
// wide layer is not 45 degrees) and, for a linear gradient, projected onto
// the line CSS draws, which gives the stops their real positions when the
// designer's gradient covers only part of the layer.
func gradientCSS(p *figma.Paint, w, h float64) string {
	if w <= 0 || h <= 0 {
		w, h = 1, 1
	}
	handles := p.GradientHandlePositions
	at := func(i int) (float64, float64) { return handles[i].X * w, handles[i].Y * h }
	origin := func() string { return num(handles[0].X*100) + "% " + num(handles[0].Y*100) + "%" }

	switch p.Type {
	case "GRADIENT_LINEAR":
		angle, from, to := 180.0, 0.0, 1.0 // top to bottom, CSS's own default
		if len(handles) >= 2 {
			x0, y0 := at(0)
			x1, y1 := at(1)
			if dx, dy := x1-x0, y1-y0; dx != 0 || dy != 0 {
				angle = cssAngle(dx, dy)
				// CSS runs its gradient line through the centre of the box,
				// just long enough to reach the corners.
				sin, cos := math.Sin(angle*math.Pi/180), math.Cos(angle*math.Pi/180)
				if length := math.Abs(w*sin) + math.Abs(h*cos); length > 0 {
					onLine := func(x, y float64) float64 { return ((x-w/2)*sin-(y-h/2)*cos)/length + 0.5 }
					from, to = onLine(x0, y0), onLine(x1, y1)
				}
			}
		}
		return "linear-gradient(" + num(math.Round(angle)) + "deg, " + gradientStops(p, from, to) + ")"

	case "GRADIENT_ANGULAR":
		if len(handles) >= 2 {
			x0, y0 := at(0)
			x1, y1 := at(1)
			if dx, dy := x1-x0, y1-y0; dx != 0 || dy != 0 {
				return "conic-gradient(from " + num(math.Round(cssAngle(dx, dy))) + "deg at " + origin() + ", " + gradientStops(p, 0, 1) + ")"
			}
		}
		return "conic-gradient(" + gradientStops(p, 0, 1) + ")"

	default: // radial and diamond
		if len(handles) >= 3 {
			cx, cy := at(0)
			x1, y1 := at(1)
			x2, y2 := at(2)
			if rx, ry := math.Hypot(x1-cx, y1-cy), math.Hypot(x2-cx, y2-cy); rx > 0 && ry > 0 {
				return "radial-gradient(ellipse " + px(rx) + " " + px(ry) + " at " + origin() + ", " + gradientStops(p, 0, 1) + ")"
			}
		}
		return "radial-gradient(" + gradientStops(p, 0, 1) + ")"
	}
}

// gradientStops writes the colour stops, with each stop's 0..1 position
// mapped onto the stretch from..to of the CSS gradient line.
func gradientStops(p *figma.Paint, from, to float64) string {
	stops := make([]string, 0, len(p.GradientStops))
	for _, s := range p.GradientStops {
		stops = append(stops, colorCSS(s.Color, p.OpacityOr1())+" "+num((from+s.Position*(to-from))*100)+"%")
	}
	return strings.Join(stops, ", ")
}

// shadowCSS writes a shadow effect as one CSS box-shadow value.
func shadowCSS(e *figma.Effect) string {
	var parts []string
	if e.Type == "INNER_SHADOW" {
		parts = append(parts, "inset")
	}
	var x, y float64
	if e.Offset != nil {
		x, y = e.Offset.X, e.Offset.Y
	}
	parts = append(parts, px(x), px(y), px(e.Radius))
	if e.Spread != 0 {
		parts = append(parts, px(e.Spread))
	}
	color := figma.Color{A: 1}
	if e.Color != nil {
		color = *e.Color
	}
	return strings.Join(append(parts, colorCSS(color, 1)), " ")
}

// String writes a text style as "Inter 600 24/32 ls=-0.24 upper": family,
// weight, size over line height ("auto" for the font's own), then whatever
// else is set.
func (s TextStyle) String() string {
	if s.Family == "" && s.Size == 0 {
		return ""
	}
	lineHeight := "auto"
	if s.LineHeight > 0 {
		lineHeight = num(s.LineHeight)
	}
	parts := []string{s.Family, strconv.Itoa(s.Weight), num(s.Size) + "/" + lineHeight}
	if s.LetterSpacing != 0 {
		parts = append(parts, "ls="+num(s.LetterSpacing))
	}
	if s.Case != "" {
		parts = append(parts, s.Case)
	}
	if s.Italic {
		parts = append(parts, "italic")
	}
	if s.Decoration != "" {
		parts = append(parts, s.Decoration)
	}
	return strings.Join(parts, " ")
}
