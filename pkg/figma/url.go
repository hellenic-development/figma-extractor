package figma

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var (
	// ErrNotFigmaURL is returned for a link that is not a Figma file link.
	ErrNotFigmaURL = errors.New("not a Figma file link: expected https://www.figma.com/design/<key>/<name>")
	// ErrCommunityURL is returned for a Community link. The API cannot read
	// a Community file; a copy in the person's own drafts has a normal link.
	ErrCommunityURL = errors.New("this is a Figma Community link, which the API cannot read: open it in Figma, choose \"Open in Figma\" to copy it to your drafts, and use the copy's link")
)

// fileTypes are the first path segments a file link can start with. They all
// carry the file key as the next segment.
var fileTypes = map[string]bool{
	"file": true, "design": true, "proto": true, "board": true,
	"slides": true, "site": true, "buzz": true, "make": true,
}

var (
	fileKeyPattern  = regexp.MustCompile(`^[A-Za-z0-9]+$`)
	nodeListPattern = regexp.MustCompile(`^[0-9A-Za-z:;,\- ]+$`)
)

// Ref is what a Figma link points at.
type Ref struct {
	// FileKey is the key the API takes. For a branch link it is the branch's
	// own key.
	FileKey string
	// FileType is the link's first path segment: design, file, proto, board
	// and so on.
	FileType string
	// NodeIDs are the nodes the link selects, in colon form, without
	// duplicates. Empty for a link to the whole file.
	NodeIDs []string
}

// ParseURL reads a Figma link: the file key and the nodes it selects.
//
// It accepts the /file, /design, /proto, /board, /slides, /site, /buzz and
// /make forms on figma.com and its subdomains, a branch link (whose branch
// key is the file key the API wants), and an embed link that wraps another
// one. Node ids are taken from `node-id` in any of the forms Figma has
// written it (12:34, 12-34, 12%3A34), then from `starting-point-node-id` on
// a prototype link, then from the older `#12:34` and `/nodes/12:34` forms.
func ParseURL(raw string) (Ref, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Ref{}, ErrNotFigmaURL
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Ref{}, ErrNotFigmaURL
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Ref{}, ErrNotFigmaURL
	}
	host := strings.ToLower(u.Hostname())
	if host != "figma.com" && !strings.HasSuffix(host, ".figma.com") {
		return Ref{}, ErrNotFigmaURL
	}

	segments := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	if len(segments) == 0 {
		return Ref{}, ErrNotFigmaURL
	}
	// https://www.figma.com/embed?embed_host=share&url=<the real link>
	if segments[0] == "embed" {
		if inner := u.Query().Get("url"); inner != "" {
			return ParseURL(inner)
		}
		return Ref{}, ErrNotFigmaURL
	}
	if segments[0] == "community" {
		return Ref{}, ErrCommunityURL
	}
	if !fileTypes[segments[0]] || len(segments) < 2 {
		return Ref{}, ErrNotFigmaURL
	}

	ref := Ref{FileType: segments[0], FileKey: segments[1]}
	// /design/<key>/branch/<branchKey>/<name>
	if len(segments) >= 4 && segments[2] == "branch" {
		ref.FileKey = segments[3]
	}
	if !fileKeyPattern.MatchString(ref.FileKey) {
		return Ref{}, ErrNotFigmaURL
	}

	query := u.Query()
	switch {
	case query.Get("node-id") != "":
		ref.NodeIDs = splitNodeIDs(query.Get("node-id"))
	case query.Get("starting-point-node-id") != "":
		ref.NodeIDs = splitNodeIDs(query.Get("starting-point-node-id"))
	case u.Fragment != "" && nodeListPattern.MatchString(u.Fragment):
		ref.NodeIDs = splitNodeIDs(u.Fragment)
	default:
		for i, s := range segments {
			if s == "nodes" && i+1 < len(segments) {
				ref.NodeIDs = splitNodeIDs(segments[i+1])
				break
			}
		}
	}
	return ref, nil
}

// NormalizeNodeID turns any form of a node id into the colon form the API
// answers with: "12-34" and "12%3A34" both become "12:34", and an instance
// path "I12-34;5-6" becomes "I12:34;5:6". Figma accepts the dash form in a
// request but keys its answer by the colon form, so an id that is not
// normalised first is reported missing when the answer is read.
func NormalizeNodeID(id string) string {
	id = strings.TrimSpace(id)
	if unescaped, err := url.PathUnescape(id); err == nil {
		id = unescaped
	}
	return strings.ReplaceAll(id, "-", ":")
}

// NormalizeNodeIDs normalises each id, drops the empty ones and removes
// duplicates, keeping the first of each.
func NormalizeNodeIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = NormalizeNodeID(id); id != "" {
			out = append(out, id)
		}
	}
	return deduplicateNodeIDs(out)
}

func splitNodeIDs(list string) []string {
	return NormalizeNodeIDs(strings.Split(list, ","))
}

// deduplicateNodeIDs removes duplicate node IDs while preserving order.
func deduplicateNodeIDs(nodeIDs []string) []string {
	seen := make(map[string]bool, len(nodeIDs))
	result := make([]string, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		if !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	return result
}

// ExtractFileKey returns the file key of a Figma link.
func ExtractFileKey(figmaURL string) (string, error) {
	ref, err := ParseURL(figmaURL)
	if err != nil {
		return "", err
	}
	return ref.FileKey, nil
}

// ExtractNodeIDs returns the node ids a Figma link selects, in colon form.
// A link to a whole file returns an empty slice and no error.
func ExtractNodeIDs(figmaURL string) ([]string, error) {
	ref, err := ParseURL(figmaURL)
	if err != nil {
		return nil, err
	}
	if ref.NodeIDs == nil {
		return []string{}, nil
	}
	return ref.NodeIDs, nil
}
