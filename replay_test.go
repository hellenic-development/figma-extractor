package figmaextractor

import (
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hellenic-development/figma-extractor/pkg/figma"
)

// TestReplay runs the whole pipeline against answers recorded from a real
// file, without touching Figma. It is how a change to the output is judged
// on a real design at no cost in requests.
//
// Record once (three requests) into a folder:
//
//	curl -H "X-Figma-Token: $FIGMA_TOKEN" "https://api.figma.com/v1/files/KEY/nodes?ids=1:2" -o nodes.json
//	curl -H "X-Figma-Token: $FIGMA_TOKEN" "https://api.figma.com/v1/files/KEY?depth=3"      -o outline.json
//	curl -H "X-Figma-Token: $FIGMA_TOKEN" "https://api.figma.com/v1/files/KEY/images"       -o fills.json
//
// then run:
//
//	FIGMA_REPLAY_DIR=<folder> FIGMA_REPLAY_OUT=<folder> go test -run TestReplay -v .
//
// The node ids are read from the recording. Renders and uploads are served
// as small placeholder pictures; the markdown lands in FIGMA_REPLAY_OUT.
func TestReplay(t *testing.T) {
	dir := os.Getenv("FIGMA_REPLAY_DIR")
	if dir == "" {
		t.Skip("FIGMA_REPLAY_DIR not set")
	}
	out := os.Getenv("FIGMA_REPLAY_OUT")
	if out == "" {
		out = t.TempDir()
	}
	read := func(name string) []byte {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	nodesJSON, outlineJSON := read("nodes.json"), read("outline.json")

	var recorded figma.NodesResponse
	if err := json.Unmarshal(nodesJSON, &recorded); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for id := range recorded.Nodes {
		ids = append(ids, id)
	}
	var fills figma.FileImagesResponse
	if err := json.Unmarshal(read("fills.json"), &fills); err != nil {
		t.Fatal(err)
	}

	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/files/KEY/nodes", func(w http.ResponseWriter, r *http.Request) { w.Write(nodesJSON) })
	mux.HandleFunc("/files/KEY", func(w http.ResponseWriter, r *http.Request) { w.Write(outlineJSON) })
	mux.HandleFunc("/files/KEY/images", func(w http.ResponseWriter, r *http.Request) {
		local := map[string]string{}
		for ref := range fills.Meta.Images {
			local[ref] = srv.URL + "/upload/" + ref
		}
		json.NewEncoder(w).Encode(map[string]any{"error": false, "status": 200, "meta": map[string]any{"images": local}})
	})
	mux.HandleFunc("/images/KEY", func(w http.ResponseWriter, r *http.Request) {
		images := map[string]string{}
		for id := range strings.SplitSeq(r.URL.Query().Get("ids"), ",") {
			images[id] = srv.URL + "/render/" + r.URL.Query().Get("format") + "/" + url.PathEscape(id)
		}
		json.NewEncoder(w).Encode(map[string]any{"err": nil, "images": images})
	})
	mux.HandleFunc("/upload/", func(w http.ResponseWriter, r *http.Request) {
		png.Encode(w, image.NewRGBA(image.Rect(0, 0, 8, 8)))
	})
	mux.HandleFunc("/render/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/render/svg/") {
			w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8"/>`))
			return
		}
		png.Encode(w, image.NewRGBA(image.Rect(0, 0, 8, 8)))
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	run := func(name string, opts Options) *Result {
		opts.Client = figma.NewClient("replay", figma.WithBaseURL(srv.URL))
		opts.FileURL = "https://www.figma.com/design/KEY/Replay"
		res, err := Run(context.Background(), opts)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		path := filepath.Join(out, name+".md")
		if err := os.WriteFile(path, []byte(res.Markdown), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d bytes of markdown, %d warnings, requests %+v -> %s", name, len(res.Markdown), len(res.Warnings), res.Requests, path)
		return res
	}

	run("outline", Options{Outline: true})
	run("tokens", Options{NodeIDs: ids})
	full := run("design", Options{NodeIDs: ids, ComponentTree: true, ExportImages: true, ImageDir: filepath.Join(out, "figma-assets")})
	run("design-depth-3", Options{NodeIDs: ids, ComponentTree: true, Depth: 3})

	d := full.Design
	t.Logf("frames %d, layers %d, hidden %d, icons %d, assets %d", len(d.Frames), d.Stats.Layers, d.Stats.Hidden, d.Stats.Icons, len(full.Assets))
	t.Logf("named styles %d, colours %d, text styles %d, components %d", len(d.Tokens.Styles), len(d.Tokens.Colors), len(d.Tokens.Text), len(d.Components))

	again := run("design-again", Options{NodeIDs: ids, ComponentTree: true, ExportImages: true, ImageDir: filepath.Join(out, "figma-assets")})
	if again.Markdown != full.Markdown {
		t.Errorf("two runs over the same recording gave different markdown")
	}
}
