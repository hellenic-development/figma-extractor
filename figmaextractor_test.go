package figmaextractor

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hellenic-development/figma-extractor/pkg/figma"
)

const cardLink = "https://www.figma.com/design/KEY/Fixture-Kit?node-id=1-2&t=abc"

// fileJSON is a small whole file: two pages, one with a frame and a section.
const fileJSON = `{
  "name": "Fixture Site", "lastModified": "2026-11-02T08:00:00Z", "version": "7",
  "document": {"id": "0:0", "name": "Document", "type": "DOCUMENT", "children": [
    {"id": "0:1", "name": "Home", "type": "CANVAS", "children": [
      {"id": "3:1", "name": "Landing", "type": "FRAME", "absoluteBoundingBox": {"x": 0, "y": 0, "width": 1440, "height": 900},
       "fills": [{"type": "SOLID", "color": {"r": 1, "g": 1, "b": 1, "a": 1}}],
       "children": [{"id": "3:2", "name": "Hero", "type": "FRAME", "absoluteBoundingBox": {"x": 0, "y": 0, "width": 1440, "height": 600}}]},
      {"id": "3:5", "name": "Flows", "type": "SECTION", "children": [
        {"id": "3:6", "name": "Sign up", "type": "FRAME", "absoluteBoundingBox": {"x": 2000, "y": 0, "width": 390, "height": 844}}]}
    ]},
    {"id": "0:2", "name": "Archive", "type": "CANVAS", "children": []}
  ]},
  "components": {}, "componentSets": {}, "styles": {}
}`

// fake is Figma's API and its storage host, as far as Run uses them.
type fake struct {
	t   *testing.T
	srv *httptest.Server

	mu   sync.Mutex
	hits []string // "path?query" of every API request, in order

	nodesStatus  int  // answer the nodes endpoint with this status instead
	rendersBreak bool // every render request fails
	nodesHang    bool // the nodes endpoint never answers
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/files/KEY/nodes", func(w http.ResponseWriter, r *http.Request) {
		f.hit(r)
		if f.nodesHang {
			<-r.Context().Done()
			return
		}
		if f.nodesStatus != 0 {
			w.WriteHeader(f.nodesStatus)
			w.Write([]byte(`{"status":403,"err":"Invalid token"}`))
			return
		}
		raw, err := os.ReadFile(filepath.Join("testdata", "card.json"))
		if err != nil {
			t.Error(err)
		}
		w.Write(raw)
	})
	mux.HandleFunc("/files/KEY", func(w http.ResponseWriter, r *http.Request) {
		f.hit(r)
		w.Write([]byte(fileJSON))
	})
	mux.HandleFunc("/files/KEY/images", func(w http.ResponseWriter, r *http.Request) {
		f.hit(r)
		images := map[string]string{}
		for _, ref := range []string{"ref_photo", "ref_a", "ref_b", "ref_c", "ref_shared"} {
			images[ref] = f.srv.URL + "/upload/" + ref
		}
		json.NewEncoder(w).Encode(map[string]any{"error": false, "status": 200, "meta": map[string]any{"images": images}})
	})
	mux.HandleFunc("/images/KEY", func(w http.ResponseWriter, r *http.Request) {
		f.hit(r)
		if f.rendersBreak {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"status":400,"err":"Render timeout, try requesting fewer or smaller images"}`))
			return
		}
		images := map[string]string{}
		for id := range strings.SplitSeq(r.URL.Query().Get("ids"), ",") {
			images[id] = f.srv.URL + "/render/" + r.URL.Query().Get("format") + "/" + url.PathEscape(id)
		}
		json.NewEncoder(w).Encode(map[string]any{"err": nil, "images": images})
	})
	mux.HandleFunc("/upload/", func(w http.ResponseWriter, r *http.Request) {
		png.Encode(w, image.NewRGBA(image.Rect(0, 0, 40, 30)))
	})
	mux.HandleFunc("/render/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/render/svg/") {
			w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"/>`))
			return
		}
		png.Encode(w, image.NewRGBA(image.Rect(0, 0, 4, 3)))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) hit(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q, _ := url.QueryUnescape(r.URL.RawQuery)
	f.hits = append(f.hits, r.URL.Path+"?"+q)
}

func (f *fake) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.hits...)
}

func (f *fake) client() *figma.Client {
	return figma.NewClient("test-token", figma.WithBaseURL(f.srv.URL))
}

func TestAFrameWithPicturesCostsThreeTierOneRequests(t *testing.T) {
	f := newFake(t)
	dir := filepath.Join(t.TempDir(), "figma-assets")

	res, err := Run(context.Background(), Options{
		Client: f.client(), FileURL: cardLink,
		ComponentTree: true, ExportImages: true, ImageDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The nodes once, then one render request per format. The whole file is
	// never fetched: the nodes answer already carries the file's name.
	want := []string{
		"/files/KEY/nodes?ids=1:2",
		"/files/KEY/images?",
		"/images/KEY?format=png&ids=1:2&scale=1",
		"/images/KEY?format=svg&ids=1:7,I1:13;4:11,1:26,1:28",
	}
	if got := f.log(); !reflect.DeepEqual(got, want) {
		t.Errorf("requests:\n got %v\nwant %v", got, want)
	}
	if res.Requests != (figma.RequestStats{Tier1: 3, Tier2: 1}) {
		t.Errorf("Requests = %+v, want 3 Tier 1 and 1 Tier 2", res.Requests)
	}

	if res.FileName != "Fixture Kit" || res.FileKey != "KEY" || res.LastModified != "2026-11-01T10:00:00Z" || !reflect.DeepEqual(res.NodeIDs, []string{"1:2"}) {
		t.Errorf("result header: %q %q %q %v", res.FileName, res.FileKey, res.LastModified, res.NodeIDs)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings: %v", res.Warnings)
	}
	if len(res.Assets) != 10 || res.AssetDir != filepath.ToSlash(dir) {
		t.Errorf("%d assets in %q, want 10 in the image folder", len(res.Assets), res.AssetDir)
	}
	for _, want := range []string{
		"# Figma design: Fixture Kit\n",
		"- Files folder: `" + filepath.ToSlash(dir) + "` (1 screenshot, 5 photos, 3 icons, 1 exported layer)\n",
		"- Screenshot: `screenshot-card.png` (4x3 px)\n",
		`frame "Card" #1:2 360x672 col bg=#FFFFFF`,
		`  image "Photo" #1:3 360x200 w=fill fit=cover src=photo.png radius=16,16,0,0`,
		`    icon "spark" #1:7 16x16 src=spark.svg`,
		`      frame "Logo" #1:26 120x40 src=logo.svg`,
	} {
		if !strings.Contains(res.Markdown, want) {
			t.Errorf("markdown is missing %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "screenshot-card.png")); err != nil {
		t.Errorf("the screenshot is not on disk: %v", err)
	}
}

func TestExplicitIDsInDashFormWork(t *testing.T) {
	f := newFake(t)
	res, err := Run(context.Background(), Options{
		Client: f.client(), FileURL: "https://www.figma.com/design/KEY/Fixture-Kit", NodeIDs: []string{" 1-2 "},
	})
	if err != nil {
		t.Fatalf("an id copied out of a link in dash form was refused: %v", err)
	}
	if !reflect.DeepEqual(res.NodeIDs, []string{"1:2"}) || len(res.Design.Frames) != 1 {
		t.Errorf("node ids %v, %d frames", res.NodeIDs, len(res.Design.Frames))
	}
	if got := f.log(); len(got) != 1 || got[0] != "/files/KEY/nodes?ids=1:2" {
		t.Errorf("requests %v", got)
	}
}

func TestAMissingNodeIsAWarningUnlessNothingIsLeft(t *testing.T) {
	f := newFake(t)
	res, err := Run(context.Background(), Options{Client: f.client(), FileURL: cardLink, NodeIDs: []string{"1:2", "9:999"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Node 9:999 is not in this file."}; !reflect.DeepEqual(res.Warnings, want) {
		t.Errorf("warnings = %v, want %v", res.Warnings, want)
	}
	if !strings.Contains(res.Markdown, "## Warnings\n\n- Node 9:999 is not in this file.\n") {
		t.Errorf("the warning is not in the markdown")
	}

	_, err = Run(context.Background(), Options{Client: f.client(), FileURL: cardLink, NodeIDs: []string{"9:999"}})
	if !errors.Is(err, ErrNodesNotFound) || !strings.Contains(err.Error(), "9:999") {
		t.Errorf("err = %v, want ErrNodesNotFound naming the node", err)
	}
}

func TestFailedRendersDoNotCostTheDesign(t *testing.T) {
	f := newFake(t)
	f.rendersBreak = true
	res, err := Run(context.Background(), Options{
		Client: f.client(), FileURL: cardLink, ComponentTree: true, ExportImages: true, ImageDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("failed renders failed the whole extraction: %v", err)
	}
	if len(res.Warnings) == 0 {
		t.Errorf("no warning for the failed renders")
	}
	if !strings.Contains(res.Markdown, `frame "Card" #1:2`) || !strings.Contains(res.Markdown, "## Warnings") {
		t.Errorf("the tree or the warnings are missing from the markdown")
	}
	// The photos are uploads, not renders: they still arrive.
	if !strings.Contains(res.Markdown, "src=photo.png") {
		t.Errorf("the photos were lost with the renders")
	}
}

func TestOutlineIsOneCheapRequest(t *testing.T) {
	f := newFake(t)
	res, err := Run(context.Background(), Options{Client: f.client(), FileURL: "https://www.figma.com/design/KEY/Fixture-Site", Outline: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.log(); len(got) != 1 || got[0] != "/files/KEY?depth=3" {
		t.Errorf("requests %v, want the file once at depth 3", got)
	}
	if res.Outline == nil || res.Design != nil || len(res.Outline.Pages) != 2 {
		t.Fatalf("outline = %+v, design = %v", res.Outline, res.Design)
	}
	for _, want := range []string{
		"# Figma file outline: Fixture Site\n",
		"## Page \"Home\" #0:1\n",
		"- frame \"Landing\" #3:1 · 1440x900 · 1 child · https://www.figma.com/design/KEY?node-id=3-1\n",
		"- section \"Flows\" #3:5 · 1 child ·",
		"  - frame \"Sign up\" #3:6 · 390x844\n",
		"## Page \"Archive\" #0:2\n\nNothing visible on this page.\n",
	} {
		if !strings.Contains(res.Markdown, want) {
			t.Errorf("outline markdown is missing %q:\n%s", want, res.Markdown)
		}
	}

	// The outline of one node asks for that node two levels deep.
	f2 := newFake(t)
	if _, err := Run(context.Background(), Options{Client: f2.client(), FileURL: cardLink, Outline: true}); err != nil {
		t.Fatal(err)
	}
	if got := f2.log(); len(got) != 1 || got[0] != "/files/KEY/nodes?depth=2&ids=1:2" {
		t.Errorf("requests %v, want the node once at depth 2", got)
	}
}

func TestAWholeFileExpandsIntoItsFrames(t *testing.T) {
	f := newFake(t)
	res, err := Run(context.Background(), Options{Client: f.client(), FileURL: "https://www.figma.com/design/KEY/Fixture-Site", ComponentTree: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.log(); len(got) != 1 || got[0] != "/files/KEY?" {
		t.Errorf("requests %v", got)
	}
	var frames []string
	for _, frame := range res.Design.Frames {
		frames = append(frames, frame.Page+"/"+frame.Section+"/"+frame.Name)
	}
	if want := []string{"Home//Landing", "Home/Flows/Sign up"}; !reflect.DeepEqual(frames, want) {
		t.Errorf("frames = %v, want %v", frames, want)
	}
	if !strings.Contains(res.Markdown, "- Page: Home, section Flows\n") {
		t.Errorf("the page and section of a frame are missing from the markdown")
	}
}

func TestDepthIsAskedOfFigmaToo(t *testing.T) {
	f := newFake(t)
	res, err := Run(context.Background(), Options{Client: f.client(), FileURL: cardLink, ComponentTree: true, Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.log(); len(got) != 1 || got[0] != "/files/KEY/nodes?depth=6&ids=1:2" {
		t.Errorf("requests %v, want the nodes asked for with a depth", got)
	}
	if !strings.Contains(res.Markdown, "... 5 more children not shown (depth limit). Ask for node 1:6 to see them.") {
		t.Errorf("the cut is not announced in the markdown")
	}
}

func TestStopEndsTheRun(t *testing.T) {
	f := newFake(t)
	f.nodesHang = true
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(80*time.Millisecond, cancel)
	start := time.Now()
	_, err := Run(ctx, Options{Client: f.client(), FileURL: cardLink})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("the run outlived its context")
	}
}

func TestBadInputCostsNoRequest(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want error
	}{
		{"scale out of range", Options{FileURL: cardLink, ImageScales: []float64{9}}, ErrInvalidOption},
		{"unknown format", Options{FileURL: cardLink, ImageFormat: "webp"}, ErrInvalidOption},
		{"negative depth", Options{FileURL: cardLink, Depth: -1}, ErrInvalidOption},
		{"not a figma link", Options{FileURL: "https://example.com/design/KEY/x"}, figma.ErrNotFigmaURL},
		{"community link", Options{FileURL: "https://www.figma.com/community/file/123/kit"}, figma.ErrCommunityURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			tt.opts.Client = f.client()
			_, err := Run(context.Background(), tt.opts)
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
			if got := f.log(); len(got) != 0 {
				t.Errorf("requests were sent for input that could not work: %v", got)
			}
		})
	}
}

func TestARefusedTokenIsTellable(t *testing.T) {
	f := newFake(t)
	f.nodesStatus = http.StatusForbidden
	_, err := Run(context.Background(), Options{Client: f.client(), FileURL: cardLink})
	if figma.KindOf(err) != figma.KindTokenRejected {
		t.Errorf("err = %v, want a rejected-token error a caller can test for", err)
	}
}

func TestFromNodesGivesWhatRunGives(t *testing.T) {
	f := newFake(t)
	viaRun, err := Run(context.Background(), Options{Client: f.client(), FileURL: cardLink, ComponentTree: true})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join("testdata", "card.json"))
	if err != nil {
		t.Fatal(err)
	}
	var resp figma.NodesResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	viaNodes, err := FromNodes(&resp, "KEY", []string{"1-2"}, Options{ComponentTree: true})
	if err != nil {
		t.Fatal(err)
	}
	if viaNodes.Markdown != viaRun.Markdown {
		t.Errorf("FromNodes and Run wrote different markdown for the same nodes")
	}
	if viaNodes.Requests != (figma.RequestStats{}) {
		t.Errorf("FromNodes counted requests: %+v", viaNodes.Requests)
	}
	if _, err := FromNodes(&resp, "KEY", []string{"9:999"}, Options{}); !errors.Is(err, ErrNodesNotFound) {
		t.Errorf("err = %v, want ErrNodesNotFound", err)
	}
}

func TestSameRunSameMarkdown(t *testing.T) {
	var first string
	for i := range 5 {
		f := newFake(t)
		res, err := Run(context.Background(), Options{
			Client: f.client(), FileURL: cardLink, ComponentTree: true, ExportImages: true, ImageDir: filepath.Join(t.TempDir(), "a"),
		})
		if err != nil {
			t.Fatal(err)
		}
		// The folder differs per run on purpose; it is written once, in one line.
		md := strings.ReplaceAll(res.Markdown, res.AssetDir, "DIR")
		if first == "" {
			first = md
		} else if md != first {
			t.Fatalf("run %d wrote different markdown", i+1)
		}
	}
}
