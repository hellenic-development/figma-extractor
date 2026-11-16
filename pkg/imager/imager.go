// Package imager saves the pictures of a Design to disk: a screenshot of
// each frame, the original bytes of every photo, each icon as an SVG, and
// the layers the designer set up for export.
//
// Renders are the expensive part of Figma's API (Tier 1, a handful a month
// on a View seat), so the package collects every render it needs before it
// asks for any, drops the duplicates, and sends one request per format and
// scale. A failed render or download is reported as a warning and never
// costs the caller the rest of the result.
package imager

import (
	"context"
	"fmt"
	"image"
	_ "image/gif"  // pixel sizes of downloaded GIFs
	_ "image/jpeg" // and JPEGs
	_ "image/png"  // and PNGs
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/hellenic-development/figma-extractor/pkg/extractor"
	"github.com/hellenic-development/figma-extractor/pkg/figma"
)

// Config says where the files go and what to render.
type Config struct {
	// Dir is the folder the files are written to. It is created if needed.
	Dir string
	// Scales are the scale factors for raster exports, [1] when empty.
	// Screenshots are always at 1.
	Scales []float64
	// Format, when set (png, jpg, svg or pdf), is used for every icon and
	// exported layer instead of the format each would get on its own (SVG
	// for an icon, the designer's choice for an exported layer).
	Format string
	// MaxFileBytes refuses a single download above this size. 0 is no limit.
	MaxFileBytes int64
}

// AssetKind is what a saved file is for.
type AssetKind string

const (
	AssetScreenshot AssetKind = "screenshot" // a render of a whole frame
	AssetSection    AssetKind = "section"    // a render of one part of a tall frame
	AssetImage      AssetKind = "image"      // a photo used as a fill
	AssetIcon       AssetKind = "icon"       // a vector graphic
	AssetExport     AssetKind = "export"     // a layer the designer set up for export
)

// Asset is one file the export wrote.
type Asset struct {
	Kind   AssetKind
	File   string  // the name inside Config.Dir
	Path   string  // Config.Dir and File joined, with forward slashes
	Format string  // the extension, without the dot
	Scale  float64 // the render scale; 0 for an original upload
	Name   string  // the layer's name
	// NodeIDs are the layers the file serves: several when layers share a
	// photo or are the same unchanged icon.
	NodeIDs []string
	// Width and Height are the picture's own pixels, read from the file.
	// They are 0 for an SVG or a PDF.
	Width, Height int
}

// Result is what an export produced.
type Result struct {
	Assets   []Asset
	Warnings []string
}

const (
	maxIDsPerRequest   = 100
	maxIDCharsPerBatch = 4000
	parallelDownloads  = 5
	// maxSplitRequests is how many extra render requests one export may
	// spend halving batches Figma could not render. Every one of them is a
	// metered request, so a batch that keeps failing is given up on and
	// reported instead of being chased down to its last layer.
	maxSplitRequests = 8
	// maxSlugBytes keeps a file name, with a prefix, a second slug and a
	// suffix around it, under the 255 bytes most file systems allow.
	maxSlugBytes = 80
)

// job is one file to produce.
type job struct {
	kind    AssetKind
	name    string // layer name
	base    string // file name without extension, already unique
	nodeIDs []string

	// A render: ask Figma for renderID in format at scale.
	renderID string
	format   string
	scale    float64

	// An original upload: download the URL of imageRef.
	imageRef string

	url  string
	file string // set once the extension is known
	w, h int
	done bool
}

// Export writes the design's pictures and fills in Src, Screenshot and
// SectionShots on the design's layers, as file names inside Config.Dir. The error is non-nil only when the
// folder cannot be made or the context ends; everything else that goes
// wrong is a line in Result.Warnings.
func Export(ctx context.Context, client *figma.Client, fileKey string, design *extractor.Design, cfg Config) (*Result, error) {
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("could not create the image folder %q: %w", cfg.Dir, err)
	}
	if len(cfg.Scales) == 0 {
		cfg.Scales = []float64{1}
	}
	cfg.Format = strings.ToLower(cfg.Format)

	e := &exporter{ctx: ctx, client: client, fileKey: fileKey, cfg: cfg, names: newNames(), splitsLeft: maxSplitRequests}
	e.collect(design)
	if err := e.resolveImageFills(); err != nil {
		return nil, err
	}
	if err := e.render(); err != nil {
		return nil, err
	}
	if err := e.download(); err != nil {
		return nil, err
	}
	e.attach(design)

	res := &Result{Warnings: e.warnings}
	for _, j := range e.jobs {
		if !j.done {
			continue
		}
		res.Assets = append(res.Assets, Asset{
			Kind: j.kind, File: j.file, Path: e.path(j.file), Format: strings.TrimPrefix(filepath.Ext(j.file), "."),
			Scale: j.scale, Name: j.name, NodeIDs: j.nodeIDs, Width: j.w, Height: j.h,
		})
	}
	return res, nil
}

type exporter struct {
	ctx     context.Context
	client  *figma.Client
	fileKey string
	cfg     Config
	names   *names

	jobs       []*job
	byKey      map[string]*job // dedupe: what the picture is, its format and scale
	warnings   []string
	splitsLeft int // extra render requests still allowed for halving failed batches

	screenshots map[string]*job   // frame id -> its screenshot
	sections    map[string][]*job // frame id -> its section shots
	srcOf       map[string]*job   // layer id -> the file that is the layer's own picture
}

func (e *exporter) path(file string) string {
	return filepath.ToSlash(filepath.Join(e.cfg.Dir, file))
}

func (e *exporter) warn(format string, args ...any) {
	e.warnings = append(e.warnings, fmt.Sprintf(format, args...))
}

// add registers a job unless the same picture is already wanted, in which
// case the layer joins the one that is.
func (e *exporter) add(key string, j *job, nodeID string) *job {
	if e.byKey == nil {
		e.byKey = map[string]*job{}
	}
	if existing, ok := e.byKey[key]; ok {
		existing.nodeIDs = append(existing.nodeIDs, nodeID)
		return existing
	}
	j.nodeIDs = []string{nodeID}
	j.base = e.names.take(j.base)
	e.byKey[key] = j
	e.jobs = append(e.jobs, j)
	return j
}

// collect walks the design in document order, so the jobs, and with them
// the file names, come out the same on every run.
func (e *exporter) collect(design *extractor.Design) {
	e.screenshots = map[string]*job{}
	e.sections = map[string][]*job{}
	e.srcOf = map[string]*job{}

	for _, frame := range design.Frames {
		frameSlug := Slug(frame.Name, "frame")
		e.screenshots[frame.ID] = e.add("shot:"+frame.ID, &job{
			kind: AssetScreenshot, name: frame.Name, base: "screenshot-" + frameSlug,
			renderID: frame.ID, format: "png", scale: 1,
		}, frame.ID)

		// A frame much taller than it is wide (a whole landing page) is
		// unreadable once a model's viewer fits it to its long edge, so its
		// parts are rendered too. They ride in the same request.
		if frame.W > 0 && frame.H > 2*frame.W && len(frame.Children) > 1 {
			for _, part := range frame.Children {
				if part.W <= 0 || part.H <= 0 {
					continue
				}
				j := e.add("shot:"+part.ID, &job{
					kind: AssetSection, name: part.Name, base: "screenshot-" + frameSlug + "--" + Slug(part.Name, "part"),
					renderID: part.ID, format: "png", scale: 1,
				}, part.ID)
				e.sections[frame.ID] = append(e.sections[frame.ID], j)
			}
		}

		frame.Walk(func(n *extractor.Node) { e.collectLayer(n) })
	}
}

func (e *exporter) collectLayer(n *extractor.Node) {
	switch {
	case n.Icon:
		format := "svg"
		if e.cfg.Format != "" {
			format = e.cfg.Format
		}
		e.renderJobs(n, AssetIcon, "icon:"+n.AssetKey, format, "icon")

	case len(n.ImageRefs()) > 0:
		// The photo itself, as it was uploaded: sharper than any render and
		// fetched without spending a render request. Layers that show the
		// same upload share the file.
		ref := n.ImageRefs()[0]
		j := e.add("fill:"+ref, &job{kind: AssetImage, name: n.Name, base: Slug(n.Name, "image"), imageRef: ref}, n.ID)
		e.srcOf[n.ID] = j

	case n.ExportFormat != "":
		format := n.ExportFormat
		if e.cfg.Format != "" {
			format = e.cfg.Format
		}
		e.renderJobs(n, AssetExport, "export:"+n.ID, format, "layer")
	}
}

// renderJobs adds the renders of one layer: a single one for a vector
// format, one per scale for a raster one. The layer's own picture (Src) is
// the first of them.
func (e *exporter) renderJobs(n *extractor.Node, kind AssetKind, key, format, fallback string) {
	base := Slug(n.Name, fallback)
	scales := []float64{0}
	if format == "png" || format == "jpg" {
		scales = e.cfg.Scales
	}
	for i, scale := range scales {
		suffix := ""
		if scale != 0 && scale != 1 {
			suffix = "@" + strconv.FormatFloat(scale, 'g', -1, 64) + "x"
		}
		j := e.add(key+"|"+format+"|"+suffix, &job{
			kind: kind, name: n.Name, base: base + suffix,
			renderID: n.ID, format: format, scale: scale,
		}, n.ID)
		if i == 0 {
			e.srcOf[n.ID] = j
		}
	}
}

// resolveImageFills asks Figma once for the upload URLs of the photos. A
// photo Figma has no URL for is rendered instead.
func (e *exporter) resolveImageFills() error {
	var wanted []*job
	for _, j := range e.jobs {
		if j.imageRef != "" {
			wanted = append(wanted, j)
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	urls, err := e.client.ImageFills(e.ctx, e.fileKey)
	if err != nil {
		if e.ctx.Err() != nil {
			return e.ctx.Err()
		}
		e.warn("The original photos could not be listed (%v). They were rendered instead.", err)
	}
	for _, j := range wanted {
		if url := urls[j.imageRef]; url != "" {
			j.url = url
			continue
		}
		// Fall back to a render of the first layer that shows the photo.
		j.imageRef = ""
		j.renderID, j.format, j.scale = j.nodeIDs[0], "png", e.cfg.Scales[0]
	}
	return nil
}

type renderGroup struct {
	format string
	scale  float64
	jobs   []*job
}

// render asks Figma for the URLs of every render, one request per format
// and scale.
func (e *exporter) render() error {
	groups := map[string]*renderGroup{}
	var order []string
	for _, j := range e.jobs {
		if j.renderID == "" {
			continue
		}
		key := j.format + "|" + strconv.FormatFloat(j.scale, 'g', -1, 64)
		g := groups[key]
		if g == nil {
			g = &renderGroup{format: j.format, scale: j.scale}
			groups[key] = g
			order = append(order, key)
		}
		g.jobs = append(g.jobs, j)
	}

	for _, key := range order {
		g := groups[key]
		for _, batch := range batches(g.jobs) {
			stop, err := e.renderBatch(g, batch)
			if err != nil {
				return err
			}
			if stop {
				return nil
			}
		}
	}
	return nil
}

// batches splits jobs into requests that stay under Figma's id count and a
// sane URL length.
func batches(jobs []*job) [][]*job {
	var out [][]*job
	var current []*job
	chars := 0
	for _, j := range jobs {
		if len(current) == maxIDsPerRequest || (len(current) > 0 && chars+len(j.renderID) > maxIDCharsPerBatch) {
			out = append(out, current)
			current, chars = nil, 0
		}
		current = append(current, j)
		chars += len(j.renderID) + 1
	}
	if len(current) > 0 {
		out = append(out, current)
	}
	return out
}

// renderBatch fills in the URLs of one batch. When Figma cannot render a
// batch as a whole (it times out on large ones) the batch is halved and
// tried again, toward the single layer at fault, for as long as the split
// budget lasts. Only that failure is split: a refused token, a used-up rate
// limit, a bad parameter or a server error would fail the halves too, and
// each attempt costs a metered request. stop is true for those, because no
// further render request can succeed either.
func (e *exporter) renderBatch(g *renderGroup, batch []*job) (stop bool, err error) {
	ids := make([]string, len(batch))
	for i, j := range batch {
		ids[i] = j.renderID
	}
	urls, err := e.client.Images(e.ctx, e.fileKey, ids, figma.ImageQuery{Format: g.format, Scale: g.scale})
	if err != nil {
		if e.ctx.Err() != nil {
			return true, e.ctx.Err()
		}
		if kind := figma.KindOf(err); kind != figma.KindRenderFailed && kind != figma.KindTooLarge {
			e.warn("Figma stopped rendering pictures (%v). The pictures that were not rendered yet are missing.", err)
			return true, nil
		}
		if len(batch) == 1 {
			e.warn("Figma could not render %q (%s): %v.", batch[0].name, batch[0].renderID, err)
			return false, nil
		}
		if e.splitsLeft < 2 {
			e.warn("%d pictures were not rendered: Figma kept failing to render them in smaller batches (%v).", len(batch), err)
			return false, nil
		}
		e.splitsLeft -= 2
		half := len(batch) / 2
		if stop, err := e.renderBatch(g, batch[:half]); stop || err != nil {
			return stop, err
		}
		return e.renderBatch(g, batch[half:])
	}
	for _, j := range batch {
		j.url = urls[j.renderID]
		if j.url == "" {
			e.warn("Figma could not render %q (%s).", j.name, j.renderID)
		}
	}
	return false, nil
}

// download fetches every file that has a URL, a few at a time. The names
// were fixed when the jobs were collected, so the order downloads finish in
// changes nothing.
func (e *exporter) download() error {
	var pending []*job
	for _, j := range e.jobs {
		if j.url != "" {
			pending = append(pending, j)
		}
	}
	failures := make([]string, len(pending))
	sem := make(chan struct{}, parallelDownloads)
	var wg sync.WaitGroup
	for i, j := range pending {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			if e.ctx.Err() != nil {
				return
			}
			if err := e.downloadOne(j); err != nil && e.ctx.Err() == nil {
				failures[i] = fmt.Sprintf("%q could not be downloaded: %v.", j.name, err)
			}
		})
	}
	wg.Wait()
	if err := e.ctx.Err(); err != nil {
		return err
	}
	for _, f := range failures {
		if f != "" {
			e.warnings = append(e.warnings, f)
		}
	}
	return nil
}

func (e *exporter) downloadOne(j *job) error {
	ext := j.format
	// An upload's type is known only from its bytes: Figma's URL for it has
	// no extension. It lands under a provisional name and is renamed.
	provisional := j.imageRef != ""
	if provisional {
		ext = "download"
	}
	dest := filepath.Join(e.cfg.Dir, j.base+"."+ext)
	got, err := e.client.Download(e.ctx, j.url, dest, e.cfg.MaxFileBytes)
	if err != nil {
		return err
	}
	if provisional {
		ext = figma.ExtensionForMIME(got.MIME)
		if ext == "" {
			ext = "png"
		}
		final := filepath.Join(e.cfg.Dir, j.base+"."+ext)
		if err := os.Rename(dest, final); err != nil {
			os.Remove(dest)
			return err
		}
		dest = final
	}
	j.file = filepath.Base(dest)
	j.w, j.h = pixelSize(dest)
	j.done = true
	return nil
}

// pixelSize reads a raster picture's size from its header. Figma scales a
// render down past 32 megapixels, so the file is the only honest source.
func pixelSize(path string) (int, int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// attach writes the saved paths onto the design's layers.
func (e *exporter) attach(design *extractor.Design) {
	for _, frame := range design.Frames {
		if j := e.screenshots[frame.ID]; j != nil && j.done {
			frame.Screenshot = j.file
		}
		for _, j := range e.sections[frame.ID] {
			if j.done {
				frame.SectionShots = append(frame.SectionShots, j.file)
			}
		}
		frame.Walk(func(n *extractor.Node) {
			if j := e.srcOf[n.ID]; j != nil && j.done {
				n.Src = j.file
			}
		})
	}
	// An icon that shares its picture with an earlier one has no job of its
	// own; it takes the file of the job it joined.
	shared := map[string]*job{}
	for _, j := range e.jobs {
		if j.done {
			for _, id := range j.nodeIDs {
				if _, ok := shared[id]; !ok {
					shared[id] = j
				}
			}
		}
	}
	for _, frame := range design.Frames {
		frame.Walk(func(n *extractor.Node) {
			if n.Src == "" && n.Icon {
				if j := shared[n.ID]; j != nil && j.kind == AssetIcon {
					n.Src = j.file
				}
			}
		})
	}
}

// names hands out file names that never repeat within one export, whatever
// their extension and whatever the letter case, so no file is written over
// another on any file system.
type names struct {
	used map[string]bool
}

func newNames() *names { return &names{used: map[string]bool{}} }

func (n *names) take(base string) string {
	candidate := base
	for i := 2; n.used[strings.ToLower(candidate)]; i++ {
		candidate = base + "-" + strconv.Itoa(i)
	}
	n.used[strings.ToLower(candidate)] = true
	return candidate
}

// windowsReserved are the file names Windows refuses whatever the extension.
var windowsReserved = func() map[string]bool {
	m := map[string]bool{"con": true, "prn": true, "aux": true, "nul": true}
	for i := 1; i <= 9; i++ {
		m["com"+strconv.Itoa(i)] = true
		m["lpt"+strconv.Itoa(i)] = true
	}
	return m
}()

// Slug turns a layer name into a file name: lower case, letters and digits
// of any script kept, everything else folded into single dashes. fallback
// is used for a name with nothing usable in it.
func Slug(name, fallback string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		// Counted in bytes, which is what a file system limits: a name in
		// Greek or Japanese takes two or three per letter.
		if b.Len()+utf8.RuneLen(r) > maxSlugBytes {
			break
		}
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.TrimRight(b.String(), "-")
	if slug == "" {
		slug = fallback
	}
	if windowsReserved[slug] {
		slug = "_" + slug
	}
	return slug
}

// SortAssets orders assets for display: screenshots, then photos, icons and
// exports, each in the order they were collected.
func SortAssets(assets []Asset) {
	rank := map[AssetKind]int{AssetScreenshot: 0, AssetSection: 1, AssetImage: 2, AssetIcon: 3, AssetExport: 4}
	sort.SliceStable(assets, func(i, j int) bool { return rank[assets[i].Kind] < rank[assets[j].Kind] })
}
