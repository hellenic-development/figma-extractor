package imager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hellenic-development/figma-extractor/pkg/extractor"
	"github.com/hellenic-development/figma-extractor/pkg/figma"
)

// fakeFigma stands in for both Figma's API and the storage host its URLs
// point at.
type fakeFigma struct {
	t   *testing.T
	srv *httptest.Server

	mu      sync.Mutex
	renders []string // "format@scale: id,id" per render request, in order

	missingUploads map[string]bool // imageRefs Figma has no URL for
	nullRenders    map[string]bool // node ids Figma answers null for
	timeoutOver    int             // a render request with more ids than this times out; 0 never
	rateLimited    bool            // every render request answers 429
	hangDownloads  bool            // downloads never finish
	refuseWith     string          // every render request answers 400 with this reason
}

func newFakeFigma(t *testing.T) *fakeFigma {
	f := &fakeFigma{t: t, missingUploads: map[string]bool{}, nullRenders: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/files/KEY/images", f.imageFills)
	mux.HandleFunc("/images/KEY", f.images)
	mux.HandleFunc("/upload/", f.upload)
	mux.HandleFunc("/render/", f.rendered)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeFigma) client() *figma.Client {
	return figma.NewClient("test-token", figma.WithBaseURL(f.srv.URL))
}

func (f *fakeFigma) imageFills(w http.ResponseWriter, r *http.Request) {
	images := map[string]string{}
	for _, ref := range []string{"ref_photo", "ref_a", "ref_b", "ref_c", "ref_shared"} {
		if !f.missingUploads[ref] {
			images[ref] = f.srv.URL + "/upload/" + ref
		}
	}
	json.NewEncoder(w).Encode(map[string]any{"error": false, "status": 200, "meta": map[string]any{"images": images}})
}

func (f *fakeFigma) images(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ids := strings.Split(q.Get("ids"), ",")
	scale := q.Get("scale")
	if scale == "" {
		scale = "-"
	}
	f.mu.Lock()
	f.renders = append(f.renders, q.Get("format")+"@"+scale+": "+strings.Join(ids, ","))
	f.mu.Unlock()

	if f.rateLimited {
		w.Header().Set("Retry-After", "86400")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	if f.refuseWith != "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"status": 400, "err": f.refuseWith})
		return
	}
	if f.timeoutOver > 0 && len(ids) > f.timeoutOver {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"status":400,"err":"Render timeout, try requesting fewer or smaller images"}`))
		return
	}
	images := map[string]any{}
	for _, id := range ids {
		if f.nullRenders[id] {
			images[id] = nil
			continue
		}
		images[id] = f.srv.URL + "/render/" + q.Get("format") + "/" + url.PathEscape(id)
	}
	json.NewEncoder(w).Encode(map[string]any{"err": nil, "images": images})
}

// upload serves a photo with no extension in its URL, as Figma does. The
// hero photo is a JPEG, the rest are PNGs.
func (f *fakeFigma) upload(w http.ResponseWriter, r *http.Request) {
	if f.hang(w, r) {
		return
	}
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	if strings.HasSuffix(r.URL.Path, "ref_photo") {
		jpeg.Encode(w, img, nil)
		return
	}
	png.Encode(w, img)
}

func (f *fakeFigma) rendered(w http.ResponseWriter, r *http.Request) {
	if f.hang(w, r) {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/render/svg/") {
		w.Write([]byte(`<svg width="16" height="16" xmlns="http://www.w3.org/2000/svg"><path d="M0 0h16v16H0z"/></svg>`))
		return
	}
	png.Encode(w, image.NewRGBA(image.Rect(0, 0, 4, 3)))
}

func (f *fakeFigma) hang(w http.ResponseWriter, r *http.Request) bool {
	if !f.hangDownloads {
		return false
	}
	w.Write(bytes.Repeat([]byte("x"), 32<<10))
	w.(http.Flusher).Flush()
	<-r.Context().Done()
	return true
}

func (f *fakeFigma) renderLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.renders...)
}

func cardDesign(t *testing.T) *extractor.Design {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "card.json"))
	if err != nil {
		t.Fatal(err)
	}
	var resp figma.NodesResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	src, _ := extractor.SourceFromNodes(&resp, []string{"1:2"})
	return extractor.Build(src, extractor.Options{})
}

func filesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func layer(t *testing.T, d *extractor.Design, id string) *extractor.Node {
	t.Helper()
	var got *extractor.Node
	for _, f := range d.Frames {
		f.Walk(func(n *extractor.Node) {
			if n.ID == id {
				got = n
			}
		})
	}
	if got == nil {
		t.Fatalf("layer %s is not in the design", id)
	}
	return got
}

func assetFor(res *Result, file string) *Asset {
	for i := range res.Assets {
		if res.Assets[i].File == file {
			return &res.Assets[i]
		}
	}
	return nil
}

func TestExportWritesEachPictureOnceInFewRequests(t *testing.T) {
	fake := newFakeFigma(t)
	client := fake.client()
	design := cardDesign(t)
	dir := filepath.Join(t.TempDir(), "figma-assets")

	res, err := Export(context.Background(), client, "KEY", design, Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings: %v", res.Warnings)
	}

	want := []string{
		"arrow-right-2.svg", // the rotated, half-transparent instance: a different picture
		"arrow-right.svg",
		"image-2.png", "image-3.png", "image.png", // three layers named "Image", three uploads
		"logo.svg",            // the designer's own export setting
		"photo.jpg",           // a JPEG behind a URL with no extension
		"screenshot-card.png", // one screenshot, of the frame asked for
		"spark.svg",           // an icon, not its paths
		"εικόνα.png",          // a Greek layer name keeps its letters
	}
	if got := filesIn(t, dir); !reflect.DeepEqual(got, want) {
		t.Errorf("files:\n got %v\nwant %v", got, want)
	}

	// One render request per format: the screenshot as PNG, the icons and
	// the exported logo as SVG. The photos are original uploads and cost a
	// single Tier 2 call between them.
	wantRenders := []string{"png@1: 1:2", "svg@-: 1:7,I1:13;4:11,1:26,1:28"}
	if got := fake.renderLog(); !reflect.DeepEqual(got, wantRenders) {
		t.Errorf("render requests:\n got %v\nwant %v", got, wantRenders)
	}
	if got := client.Requests(); got.Tier1 != 2 || got.Tier2 != 1 {
		t.Errorf("requests %+v, want 2 Tier 1 and 1 Tier 2", got)
	}

	// Two layers show the same upload: one file serves both.
	shared := assetFor(res, "εικόνα.png")
	if shared == nil || !reflect.DeepEqual(shared.NodeIDs, []string{"1:24", "1:25"}) {
		t.Errorf("shared photo asset = %+v", shared)
	}
	if a, b := layer(t, design, "1:24").Src, layer(t, design, "1:25").Src; a == "" || a != b {
		t.Errorf("the two layers point at %q and %q, want one file", a, b)
	}

	photo := assetFor(res, "photo.jpg")
	if photo == nil || photo.Kind != AssetImage || photo.Width != 40 || photo.Height != 30 || photo.Scale != 0 {
		t.Errorf("photo asset = %+v, want the upload's own 40x30", photo)
	}
	if got := layer(t, design, "1:3").Src; got != "photo.jpg" {
		t.Errorf("photo layer src = %q", got)
	}
	if got := layer(t, design, "1:7").Src; got != "spark.svg" {
		t.Errorf("icon layer src = %q", got)
	}
	if got := design.Frames[0].Screenshot; got != "screenshot-card.png" {
		t.Errorf("frame screenshot = %q", got)
	}
	if !strings.HasSuffix(photo.Path, "/figma-assets/photo.jpg") || strings.Contains(photo.Path, `\`) {
		t.Errorf("asset path = %q, want the folder and the file joined with forward slashes", photo.Path)
	}
}

func TestSameDesignSameFileNames(t *testing.T) {
	var first []string
	for i := range 5 {
		fake := newFakeFigma(t)
		dir := t.TempDir()
		if _, err := Export(context.Background(), fake.client(), "KEY", cardDesign(t), Config{Dir: dir}); err != nil {
			t.Fatal(err)
		}
		got := filesIn(t, dir)
		if first == nil {
			first = got
		} else if !reflect.DeepEqual(first, got) {
			t.Fatalf("run %d wrote %v, the first wrote %v", i+1, got, first)
		}
	}
}

func TestUnchangedInstancesOfAnIconAreRenderedOnce(t *testing.T) {
	icon := func(id string) *figma.Node {
		return &figma.Node{
			ID: id, Name: "chevron", Type: "INSTANCE", ComponentID: "6:1",
			AbsoluteBoundingBox: &figma.Rectangle{Width: 20, Height: 20},
			Children:            []*figma.Node{{ID: id + ";v", Type: "VECTOR"}},
		}
	}
	root := &figma.Node{ID: "1:1", Name: "Row", Type: "FRAME", AbsoluteBoundingBox: &figma.Rectangle{Width: 200, Height: 40},
		Children: []*figma.Node{icon("1:2"), icon("1:3"), icon("1:4")}}
	design := extractor.Build(extractor.Source{Roots: []*figma.Node{root}}, extractor.Options{})

	fake := newFakeFigma(t)
	dir := t.TempDir()
	res, err := Export(context.Background(), fake.client(), "KEY", design, Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fake.renderLog(), []string{"png@1: 1:1", "svg@-: 1:2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("render requests %v, want %v: three identical icons are one render", got, want)
	}
	for _, id := range []string{"1:2", "1:3", "1:4"} {
		if got := layer(t, design, id).Src; got != "chevron.svg" {
			t.Errorf("icon %s src = %q, want the shared file", id, got)
		}
	}
	if a := assetFor(res, "chevron.svg"); a == nil || len(a.NodeIDs) != 3 {
		t.Errorf("chevron asset = %+v", a)
	}
}

func TestAFailedRenderIsAWarningNotAnError(t *testing.T) {
	fake := newFakeFigma(t)
	fake.nullRenders["1:7"] = true
	design := cardDesign(t)
	dir := t.TempDir()

	res, err := Export(context.Background(), fake.client(), "KEY", design, Config{Dir: dir})
	if err != nil {
		t.Fatalf("one failed render failed the export: %v", err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], `"spark"`) || !strings.Contains(res.Warnings[0], "1:7") {
		t.Errorf("warnings = %v, want one naming the spark icon", res.Warnings)
	}
	if layer(t, design, "1:7").Src != "" {
		t.Errorf("the icon that failed to render has a src")
	}
	if assetFor(res, "screenshot-card.png") == nil || assetFor(res, "photo.jpg") == nil || assetFor(res, "logo.svg") == nil {
		t.Errorf("the other pictures were lost with it: %v", filesIn(t, dir))
	}
}

func TestARenderTimeoutIsSplitUntilItFits(t *testing.T) {
	fake := newFakeFigma(t)
	fake.timeoutOver = 1 // Figma times out on any request for more than one layer
	dir := t.TempDir()

	res, err := Export(context.Background(), fake.client(), "KEY", cardDesign(t), Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings: %v", res.Warnings)
	}
	for _, file := range []string{"spark.svg", "arrow-right.svg", "arrow-right-2.svg", "logo.svg"} {
		if assetFor(res, file) == nil {
			t.Errorf("%s is missing after the batch was split", file)
		}
	}
	// The screenshot alone, then the four vectors as 4, 2, 1, 1, 2, 1, 1: a
	// split costs metered requests, so its count is part of the contract.
	if got := len(fake.renderLog()); got != 8 {
		t.Errorf("%d render requests, want 8:\n%s", got, strings.Join(fake.renderLog(), "\n"))
	}
}

// Halving is for a batch Figma could not render as a whole. Any other
// failure would fail the halves too, and every attempt is a metered request:
// sixteen icons behind a bad request once cost thirty-two of them.
func TestOnlyARenderFailureIsSplit(t *testing.T) {
	for _, reason := range []string{"Invalid scale", "Internal error"} {
		t.Run(reason, func(t *testing.T) {
			fake := newFakeFigma(t)
			fake.refuseWith = reason
			res, err := Export(context.Background(), fake.client(), "KEY", cardDesign(t), Config{Dir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			if got := fake.renderLog(); len(got) != 1 {
				t.Errorf("%d render requests after a refusal that splitting cannot fix, want 1:\n%s", len(got), strings.Join(got, "\n"))
			}
			if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], reason) {
				t.Errorf("warnings = %v, want one carrying Figma's reason", res.Warnings)
			}
			if assetFor(res, "photo.jpg") == nil {
				t.Errorf("the photos, which need no render, were lost")
			}
		})
	}
}

// A batch that keeps timing out is not chased down to the last layer: the
// split has a budget, and what is left when it runs out is reported.
func TestSplittingStopsAtItsBudget(t *testing.T) {
	root := &figma.Node{ID: "1:1", Name: "Icons", Type: "FRAME", AbsoluteBoundingBox: &figma.Rectangle{Width: 800, Height: 400}}
	for i := 0; i < 64; i++ {
		id := fmt.Sprintf("2:%d", i)
		root.Children = append(root.Children, &figma.Node{
			ID: id, Name: fmt.Sprintf("icon %d", i), Type: "FRAME", AbsoluteBoundingBox: &figma.Rectangle{Width: 24, Height: 24},
			Children: []*figma.Node{{ID: id + ";v", Type: "VECTOR"}},
		})
	}
	design := extractor.Build(extractor.Source{Roots: []*figma.Node{root}}, extractor.Options{})

	fake := newFakeFigma(t)
	fake.refuseWith = "Render timeout, try requesting fewer or smaller images"
	res, err := Export(context.Background(), fake.client(), "KEY", design, Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// One request for the screenshot, one for the 64 icons, then the budget.
	if got, most := len(fake.renderLog()), 2+maxSplitRequests; got > most {
		t.Errorf("%d render requests chasing a batch that never renders, want at most %d", got, most)
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, " "), "were not rendered") {
		t.Errorf("warnings = %v, want one that says how many pictures were given up on", res.Warnings)
	}
}

func TestAPhotoWithNoUploadIsRendered(t *testing.T) {
	fake := newFakeFigma(t)
	fake.missingUploads["ref_a"] = true
	design := cardDesign(t)
	dir := t.TempDir()

	res, err := Export(context.Background(), fake.client(), "KEY", design, Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.renderLog()[0]; got != "png@1: 1:2,1:21" {
		t.Errorf("png request = %q, want the layer of the missing upload in the same request as the screenshot", got)
	}
	a := assetFor(res, "image.png")
	if a == nil || a.Width != 4 || a.Scale != 1 || layer(t, design, "1:21").Src == "" {
		t.Errorf("fallback asset = %+v", a)
	}
}

func TestARateLimitStopsRenderingAndKeepsThePhotos(t *testing.T) {
	fake := newFakeFigma(t)
	fake.rateLimited = true
	design := cardDesign(t)
	dir := t.TempDir()

	res, err := Export(context.Background(), fake.client(), "KEY", design, Config{Dir: dir})
	if err != nil {
		t.Fatalf("a rate limit on renders failed the export: %v", err)
	}
	if got := len(fake.renderLog()); got != 1 {
		t.Errorf("%d render requests after the limit was hit, want 1", got)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "stopped rendering") {
		t.Errorf("warnings = %v", res.Warnings)
	}
	if assetFor(res, "photo.jpg") == nil {
		t.Errorf("the photos, which need no render, were lost: %v", filesIn(t, dir))
	}
	if design.Frames[0].Screenshot != "" {
		t.Errorf("a screenshot path was set for a render that never happened")
	}
}

func TestStopEndsTheExportAndLeavesNoHalfFiles(t *testing.T) {
	fake := newFakeFigma(t)
	fake.hangDownloads = true
	dir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(150*time.Millisecond, cancel)
	start := time.Now()
	_, err := Export(ctx, fake.client(), "KEY", cardDesign(t), Config{Dir: dir})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("the export outlived its context")
	}
	if left := filesIn(t, dir); len(left) != 0 {
		t.Errorf("files left behind by a stopped export: %v", left)
	}
}

func TestATallFrameGetsSectionShotsInTheSameRequest(t *testing.T) {
	part := func(id, name string, y float64) *figma.Node {
		return &figma.Node{ID: id, Name: name, Type: "FRAME", AbsoluteBoundingBox: &figma.Rectangle{Y: y, Width: 1440, Height: 1200}}
	}
	page := &figma.Node{ID: "2:1", Name: "Landing page", Type: "FRAME", AbsoluteBoundingBox: &figma.Rectangle{Width: 1440, Height: 3600},
		Children: []*figma.Node{part("2:2", "Hero", 0), part("2:3", "Features", 1200), part("2:4", "Footer", 2400)}}
	design := extractor.Build(extractor.Source{Roots: []*figma.Node{page}}, extractor.Options{})

	fake := newFakeFigma(t)
	dir := t.TempDir()
	if _, err := Export(context.Background(), fake.client(), "KEY", design, Config{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if got, want := fake.renderLog(), []string{"png@1: 2:1,2:2,2:3,2:4"}; !reflect.DeepEqual(got, want) {
		t.Errorf("render requests %v, want %v", got, want)
	}
	want := []string{"screenshot-landing-page--features.png", "screenshot-landing-page--footer.png", "screenshot-landing-page--hero.png", "screenshot-landing-page.png"}
	if got := filesIn(t, dir); !reflect.DeepEqual(got, want) {
		t.Errorf("files %v, want %v", got, want)
	}
	if got := len(design.Frames[0].SectionShots); got != 3 {
		t.Errorf("%d section shots on the frame, want 3", got)
	}
}

func TestFormatAndScalesFromTheCaller(t *testing.T) {
	fake := newFakeFigma(t)
	dir := t.TempDir()
	res, err := Export(context.Background(), fake.client(), "KEY", cardDesign(t), Config{Dir: dir, Format: "png", Scales: []float64{1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	// Screenshots stay at 1 and share the PNG request at scale 1.
	want := []string{"png@1: 1:2,1:7,I1:13;4:11,1:26,1:28", "png@2: 1:7,I1:13;4:11,1:26,1:28"}
	if got := fake.renderLog(); !reflect.DeepEqual(got, want) {
		t.Errorf("render requests:\n got %v\nwant %v", got, want)
	}
	for _, file := range []string{"spark.png", "spark@2x.png", "logo.png", "logo@2x.png", "screenshot-card.png"} {
		if assetFor(res, file) == nil {
			t.Errorf("%s is missing: %v", file, filesIn(t, dir))
		}
	}
	if a := assetFor(res, "spark@2x.png"); a == nil || a.Scale != 2 {
		t.Errorf("2x asset = %+v", a)
	}
}

func TestSlug(t *testing.T) {
	tests := []struct{ name, fallback, want string }{
		{"Hero / Section #1", "x", "hero-section-1"},
		{"Εικόνα", "x", "εικόνα"},
		{"🌈  About", "x", "about"},
		{"写真 01", "x", "写真-01"},
		{"   ", "image", "image"},
		{"../../etc/passwd", "x", "etc-passwd"},
		{"CON", "x", "_con"},
		{"arrow_right.svg", "x", "arrow-right-svg"},
		{strings.Repeat("ab", 80), "x", strings.Repeat("ab", 40)},
		{strings.Repeat("α", 100), "x", strings.Repeat("α", 40)}, // 80 bytes, not 60 letters
	}
	for _, tt := range tests {
		if got := Slug(tt.name, tt.fallback); got != tt.want {
			t.Errorf("Slug(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestNamesNeverRepeat(t *testing.T) {
	n := newNames()
	var got []string
	for _, base := range []string{"image", "image", "image", "Logo", "logo", "image-2"} {
		got = append(got, n.take(base))
	}
	want := []string{"image", "image-2", "image-3", "Logo", "logo-2", "image-2-2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}
	seen := map[string]bool{}
	for _, name := range got {
		if key := strings.ToLower(name); seen[key] {
			t.Errorf("%q repeats ignoring case", name)
		} else {
			seen[key] = true
		}
	}
}

func TestBatchesStayUnderTheLimits(t *testing.T) {
	var jobs []*job
	for i := range 250 {
		jobs = append(jobs, &job{renderID: fmt.Sprintf("%d:%d", i, i)})
	}
	got := batches(jobs)
	total := 0
	for _, b := range got {
		if len(b) > maxIDsPerRequest {
			t.Errorf("a batch of %d ids", len(b))
		}
		total += len(b)
	}
	if total != 250 || len(got) != 3 {
		t.Errorf("%d jobs in %d batches, want 250 in 3", total, len(got))
	}
}
