package extractor

import "github.com/hellenic-development/figma-extractor/pkg/figma"

// Outline is the map of a file: its pages and what sits at the top of each.
// It is what to read before asking for a tree, so the tree asked for is one
// frame and not a whole page.
type Outline struct {
	Pages []OutlinePage
}

// OutlinePage is one page. A layer asked for on its own is listed under a
// page with no ID.
type OutlinePage struct {
	ID    string
	Name  string
	Items []OutlineItem
}

// OutlineItem is one layer in the outline.
type OutlineItem struct {
	ID   string
	Name string
	Type string
	W, H float64
	// Children is how many visible layers sit directly inside, or -1 when
	// the response did not go deep enough to tell.
	Children int
	// Items lists what is inside a section, or inside a layer that was
	// asked for by id.
	Items []OutlineItem
}

// BuildOutline outlines the given roots. depth is how many levels below each
// root the response holds (the `depth` the request asked for), 0 for all of
// them; a container sitting on the last level has children nobody fetched.
func BuildOutline(roots []*figma.Node, depth int) *Outline {
	o := &Outline{}
	var selection []OutlineItem
	for _, root := range roots {
		if !root.IsVisible() {
			continue
		}
		switch root.Type {
		case "DOCUMENT":
			for _, page := range root.Children {
				if page.IsVisible() {
					o.Pages = append(o.Pages, outlinePage(page, 1, depth))
				}
			}
		case "CANVAS":
			o.Pages = append(o.Pages, outlinePage(root, 0, depth))
		default:
			selection = append(selection, outlineItem(root, 0, depth, true))
		}
	}
	if len(selection) > 0 {
		o.Pages = append(o.Pages, OutlinePage{Items: selection})
	}
	return o
}

func outlinePage(page *figma.Node, level, depth int) OutlinePage {
	p := OutlinePage{ID: page.ID, Name: page.Name}
	for _, child := range page.Children {
		if child.IsVisible() {
			p.Items = append(p.Items, outlineItem(child, level+1, depth, child.Type == "SECTION"))
		}
	}
	return p
}

func outlineItem(n *figma.Node, level, depth int, expand bool) OutlineItem {
	item := OutlineItem{ID: n.ID, Name: n.Name, Type: n.Type}
	if box := n.AbsoluteBoundingBox; box != nil {
		item.W, item.H = box.Width, box.Height
	}
	fetched := depth == 0 || level < depth
	if !fetched && isContainer(n.Type) {
		item.Children = -1
		return item
	}
	for _, child := range n.Children {
		if !child.IsVisible() {
			continue
		}
		item.Children++
		if expand {
			item.Items = append(item.Items, outlineItem(child, level+1, depth, false))
		}
	}
	return item
}

func isContainer(nodeType string) bool {
	switch nodeType {
	case "FRAME", "GROUP", "SECTION", "COMPONENT", "COMPONENT_SET", "INSTANCE", "BOOLEAN_OPERATION", "CANVAS", "DOCUMENT":
		return true
	}
	return false
}
