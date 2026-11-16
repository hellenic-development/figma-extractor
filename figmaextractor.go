package figmaextractor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hellenic-development/figma-extractor/pkg/extractor"
	"github.com/hellenic-development/figma-extractor/pkg/figma"
	"github.com/hellenic-development/figma-extractor/pkg/formatter"
	"github.com/hellenic-development/figma-extractor/pkg/imager"
)

var (
	// ErrNodesNotFound is returned when none of the nodes asked for is in
	// the file. The error names them.
	ErrNodesNotFound = errors.New("the file has no such node")
	// ErrInvalidOption is returned for an option outside what Figma accepts.
	ErrInvalidOption = errors.New("invalid option")
)

// Options configures an extraction.
type Options struct {
	// AccessToken is a Figma personal access token. It is ignored when
	// Client is set.
	AccessToken string
	// FileURL is a link to a Figma file, a frame or a prototype.
	FileURL string
	// NodeIDs are the nodes to extract, in any form ("12:34", "12-34").
	// When empty, the nodes the link selects are used, and when the link
	// selects none, the whole file.
	NodeIDs []string

	// Outline returns the map of the file (its pages and the frames on
	// them, with ids and sizes) instead of a design. It is one cheap
	// request and the way to pick frames in a file too large to read whole.
	Outline bool

	// ComponentTree includes each frame's layer tree. Without it the result
	// holds the tokens, the components and the list of frames.
	ComponentTree bool
	// Depth cuts each frame's tree this many levels down. A layer at the
	// limit says how many children were left out. 0 keeps everything.
	Depth int

	// ExportImages saves a screenshot of each frame, the original photos,
	// the icons as SVG and the layers the designer set up for export.
	ExportImages bool
	// ImageFormat, when set (png, jpg, svg or pdf), replaces the format
	// each icon and exported layer would get on its own.
	ImageFormat string
	// ImageScales are the scale factors for raster exports, between 0.01
	// and 4. [1] when empty.
	ImageScales []float64
	// ImageDir is where the files go, "figma-assets" when empty.
	ImageDir string
	// MaxImageBytes refuses a single download above this size. 0 is no
	// limit.
	MaxImageBytes int64

	// Client is the Figma client to use: one with its own HTTP client,
	// size caps or base URL, or one shared with other calls so the request
	// counts add up. A new client for AccessToken is made when it is nil.
	Client *figma.Client

	// Logger receives progress messages. nil is silent.
	Logger Logger
}

// Logger receives progress messages. A nil Logger means silent operation.
type Logger interface {
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// Result is what an extraction produced.
type Result struct {
	// Markdown is the document for a reader, a coding model above all.
	Markdown string

	FileName     string
	FileKey      string
	LastModified string
	Version      string
	// NodeIDs are the nodes that were asked for, in colon form.
	NodeIDs []string

	// Design is set for a design extraction, Outline for an outline.
	Design  *extractor.Design
	Outline *extractor.Outline
	// Assets are the files ExportImages wrote, and AssetDir the folder they
	// are in, with forward slashes. The markdown names the folder once per
	// frame and refers to each file by its name.
	Assets   []imager.Asset
	AssetDir string
	// Warnings are the things that went wrong without failing the call: a
	// node that is not in the file, a picture Figma could not render. They
	// are also printed in Markdown.
	Warnings []string
	// Requests counts what this call sent to Figma, by rate-limit tier.
	Requests figma.RequestStats
}

func (o *Options) logInfo(f string, a ...any) {
	if o.Logger != nil {
		o.Logger.Infof(f, a...)
	}
}

func (o *Options) logWarn(f string, a ...any) {
	if o.Logger != nil {
		o.Logger.Warnf(f, a...)
	}
}

func (o *Options) validate() error {
	switch strings.ToLower(o.ImageFormat) {
	case "", "png", "jpg", "svg", "pdf":
	default:
		return fmt.Errorf("%w: image format %q is not png, jpg, svg or pdf", ErrInvalidOption, o.ImageFormat)
	}
	for _, s := range o.ImageScales {
		if s < 0.01 || s > 4 {
			return fmt.Errorf("%w: image scale %g is outside Figma's range of 0.01 to 4", ErrInvalidOption, s)
		}
	}
	if o.Depth < 0 {
		return fmt.Errorf("%w: depth %d is negative", ErrInvalidOption, o.Depth)
	}
	return nil
}

// Run reads a Figma file and returns its design, or its outline.
//
// It spends as few requests as it can, because Figma counts them against a
// small allowance: one request for the nodes (or the file), and with
// ExportImages one more per image format in use plus a single cheap one for
// the photos. Everything stops when ctx ends.
//
// A node that is missing, or a picture that could not be rendered or
// downloaded, does not fail the call: it is a line in Result.Warnings. The
// error is for what leaves nothing to return: a bad link, a refused token
// (a *figma.APIError, see figma.KindOf), no node found at all, or ctx
// ending.
func Run(ctx context.Context, opts Options) (*Result, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	ref, err := figma.ParseURL(opts.FileURL)
	if err != nil {
		return nil, err
	}
	ids := figma.NormalizeNodeIDs(opts.NodeIDs)
	if len(ids) == 0 {
		ids = ref.NodeIDs
	}
	client := opts.Client
	if client == nil {
		client = figma.NewClient(opts.AccessToken)
	}
	before := client.Requests()

	res := &Result{FileKey: ref.FileKey, NodeIDs: ids}
	if opts.Outline {
		err = runOutline(ctx, client, &opts, res)
	} else {
		err = runDesign(ctx, client, &opts, res)
	}
	after := client.Requests()
	res.Requests = figma.RequestStats{Tier1: after.Tier1 - before.Tier1, Tier2: after.Tier2 - before.Tier2, Tier3: after.Tier3 - before.Tier3}
	if err != nil {
		return nil, err
	}
	for _, w := range res.Warnings {
		opts.logWarn("%s", w)
	}
	return res, nil
}

func runOutline(ctx context.Context, client *figma.Client, opts *Options, res *Result) error {
	// Two levels below a requested node, three below the document: the
	// frames and a count of what is directly inside each.
	if len(res.NodeIDs) > 0 {
		opts.logInfo("Reading the outline of %d node(s) from Figma", len(res.NodeIDs))
		resp, err := client.Nodes(ctx, res.FileKey, res.NodeIDs, figma.NodesQuery{Depth: 2})
		if err != nil {
			return err
		}
		res.FileName, res.LastModified, res.Version = resp.Name, resp.LastModified, resp.Version
		src, err := sourceOf(resp, res)
		if err != nil {
			return err
		}
		res.Outline = extractor.BuildOutline(src.Roots, 2)
	} else {
		opts.logInfo("Reading the file's outline from Figma")
		file, err := client.File(ctx, res.FileKey, figma.FileQuery{Depth: 3})
		if err != nil {
			return err
		}
		res.FileName, res.LastModified, res.Version = file.Name, file.LastModified, file.Version
		res.Outline = extractor.BuildOutline([]*figma.Node{&file.Document}, 3)
	}
	res.Markdown = formatter.OutlineMarkdown(formatter.OutlineDoc{
		FileName: res.FileName, FileKey: res.FileKey, LastModified: res.LastModified,
		Outline: res.Outline, Warnings: res.Warnings,
	})
	return nil
}

func runDesign(ctx context.Context, client *figma.Client, opts *Options, res *Result) error {
	var src extractor.Source
	if len(res.NodeIDs) > 0 {
		opts.logInfo("Reading %d node(s) from Figma", len(res.NodeIDs))
		resp, err := client.Nodes(ctx, res.FileKey, res.NodeIDs, figma.NodesQuery{Depth: fetchDepth(opts.Depth)})
		if err != nil {
			return err
		}
		res.FileName, res.LastModified, res.Version = resp.Name, resp.LastModified, resp.Version
		if src, err = sourceOf(resp, res); err != nil {
			return err
		}
	} else {
		opts.logInfo("Reading the whole file from Figma")
		file, err := client.File(ctx, res.FileKey, figma.FileQuery{Depth: fetchDepth(opts.Depth)})
		if err != nil {
			return err
		}
		res.FileName, res.LastModified, res.Version = file.Name, file.LastModified, file.Version
		src = extractor.SourceFromFile(file)
	}

	res.Design = extractor.Build(src, extractor.Options{Depth: opts.Depth})
	if len(res.Design.Frames) == 0 {
		res.Warnings = append(res.Warnings, "Nothing visible was found in what was asked for.")
	}

	if opts.ExportImages && len(res.Design.Frames) > 0 {
		dir := opts.ImageDir
		if dir == "" {
			dir = "figma-assets"
		}
		opts.logInfo("Saving pictures to %s", dir)
		exported, err := imager.Export(ctx, client, res.FileKey, res.Design, imager.Config{
			Dir: dir, Scales: opts.ImageScales, Format: opts.ImageFormat, MaxFileBytes: opts.MaxImageBytes,
		})
		switch {
		case err != nil && ctx.Err() != nil:
			return fmt.Errorf("the extraction was stopped: %w", ctx.Err())
		case err != nil:
			// The design is already in hand; losing the pictures is not
			// worth losing it too.
			res.Warnings = append(res.Warnings, fmt.Sprintf("The pictures could not be saved: %v.", err))
		default:
			res.Assets, res.AssetDir = exported.Assets, filepath.ToSlash(dir)
			res.Warnings = append(res.Warnings, exported.Warnings...)
		}
	}

	res.Markdown = markdownOf(res, opts)
	return nil
}

func markdownOf(res *Result, opts *Options) string {
	return formatter.Markdown(formatter.Doc{
		FileName: res.FileName, FileKey: res.FileKey, LastModified: res.LastModified,
		Design: res.Design, Assets: res.Assets, AssetDir: res.AssetDir, Warnings: res.Warnings, Tree: opts.ComponentTree,
	})
}

// fetchDepth is how deep to ask Figma for when the tree is cut at depth. It
// is deeper than the cut: the frames may sit under a page and a section,
// the layers at the cut need their children counted, and telling an icon
// from a frame takes a look inside it.
func fetchDepth(depth int) int {
	if depth <= 0 {
		return 0
	}
	return depth + 5
}

// sourceOf gathers the requested nodes of a response, turning the ones the
// file does not have into warnings, or into the error when it has none.
func sourceOf(resp *figma.NodesResponse, res *Result) (extractor.Source, error) {
	src, missing := extractor.SourceFromNodes(resp, res.NodeIDs)
	if len(src.Roots) == 0 {
		return src, fmt.Errorf("%w: %s", ErrNodesNotFound, strings.Join(missing, ", "))
	}
	for _, id := range missing {
		res.Warnings = append(res.Warnings, fmt.Sprintf("Node %s is not in this file.", id))
	}
	return src, nil
}

// FromNodes builds a design from a nodes response that was already fetched,
// without sending a request. It is for a caller that needs the same answer
// for something else too and should not pay for it twice. fileKey is used
// for the links in the markdown. The image options are ignored: nothing is
// downloaded.
func FromNodes(resp *figma.NodesResponse, fileKey string, ids []string, opts Options) (*Result, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	res := &Result{
		FileKey: fileKey, NodeIDs: figma.NormalizeNodeIDs(ids),
		FileName: resp.Name, LastModified: resp.LastModified, Version: resp.Version,
	}
	src, err := sourceOf(resp, res)
	if err != nil {
		return nil, err
	}
	res.Design = extractor.Build(src, extractor.Options{Depth: opts.Depth})
	res.Markdown = markdownOf(res, &opts)
	return res, nil
}

// ParseScales parses a comma-separated string of scale factors into a float64 slice.
func ParseScales(scalesStr string) ([]float64, error) {
	parts := strings.Split(scalesStr, ",")
	scales := make([]float64, 0, len(parts))

	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}

		s, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid scale value %q: %w", trimmed, err)
		}
		if s <= 0 {
			return nil, fmt.Errorf("scale value must be positive, got %g", s)
		}

		scales = append(scales, s)
	}

	if len(scales) == 0 {
		return []float64{1}, nil
	}

	return scales, nil
}

// ParseNodeIDs parses a comma-separated string of node IDs and returns a slice.
func ParseNodeIDs(nodeIDsStr string) []string {
	parts := strings.Split(nodeIDsStr, ",")
	result := make([]string, 0, len(parts))

	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}

	return result
}
