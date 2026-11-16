package formatter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hellenic-development/figma-extractor/pkg/extractor"
	"github.com/hellenic-development/figma-extractor/pkg/figma"
)

// A hyperlink, a component property, a style name and a component name are
// text a designer (or a plugin) controls, like a layer name. None of them
// may end its line, close the tree's code fence or forge a heading.
func TestLinksPropertiesAndNamesCannotBreakOut(t *testing.T) {
	const fence = "```"
	raw := `{"id":"1:1","name":"Frame","type":"FRAME","children":[
		{"id":"1:2","name":"T","type":"TEXT","characters":"click","styles":{"text":"7:1"},
		 "style":{"fontFamily":"Inter","fontWeight":400,"fontSize":16,
		          "hyperlink":{"type":"URL","url":"https://a.example/x\n` + fence + `\n## Frame \"Injected\" #9:9"}}},
		{"id":"1:3","name":"Btn","type":"INSTANCE","componentId":"5:1",
		 "componentProperties":{"Size\n` + fence + `\n# Forged prop":{"type":"VARIANT","value":"L\n# Forged value"}}},
		{"id":"1:4","name":"Mixed","type":"TEXT","characters":"ab","characterStyleOverrides":[0,1],
		 "styleOverrideTable":{"1":{"fontWeight":700,"hyperlink":{"type":"URL","url":"https://b.example/y\n# Forged run link"}}},
		 "style":{"fontFamily":"Inter","fontWeight":400,"fontSize":16}}
	]}`
	var root figma.Node
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatal(err)
	}
	design := extractor.Build(extractor.Source{
		Roots:      []*figma.Node{&root},
		Styles:     map[string]figma.Style{"7:1": {Name: "Heading\n# Forged style", StyleType: "TEXT"}},
		Components: map[string]figma.Component{"5:1": {Name: "Button\n# Forged component", Description: "x\n# Forged description"}},
	}, extractor.Options{})
	md := Markdown(Doc{FileKey: "KEY", Design: design, Tree: true})

	if got := strings.Count(md, "\n"+fence+"\n"); got != 2 {
		t.Errorf("the code fence was opened or closed %d times, want 2", got)
	}
	if got := strings.Count(md, "\n## Frame "); got != 1 {
		t.Errorf("%d frame headings, want 1: content forged one", got)
	}
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "# Forged") {
			t.Errorf("content started a line of its own: %q", line)
		}
	}
	// frame, text + its content, instance, mixed text + its two runs.
	if lines := treeOf(t, md); len(lines) != 7 {
		t.Errorf("the tree has %d lines, want 7:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	for _, want := range []string{"https://a.example/x", "https://b.example/y", `of="Button`, "Size"} {
		if !strings.Contains(md, want) {
			t.Errorf("%q was lost while being made safe", want)
		}
	}
}

func TestAttr(t *testing.T) {
	tests := []struct{ key, value, style, want string }{
		{"gap", "24", "", "gap=24"},
		{"font", "Inter 600 24/32", "Heading/H2", `font="Inter 600 24/32"{Heading/H2}`},
		{"link", "https://a.example/x", "", "link=https://a.example/x"},
		{"link", "https://a.example/a b", "", `link="https://a.example/a b"`},
		{"link", "x\ny", "", `link="x y"`},
		{"link", `say"hi`, "", `link="say\"hi"`},
		{"color", "#FFFFFF", "A\nB", "color=#FFFFFF{A B}"},
	}
	for _, tt := range tests {
		if got := attr(tt.key, tt.value, tt.style); got != tt.want {
			t.Errorf("attr(%q, %q, %q) = %s, want %s", tt.key, tt.value, tt.style, got, tt.want)
		}
	}
}
