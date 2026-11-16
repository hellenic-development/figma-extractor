package figma

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultBaseURL is Figma's REST API.
const DefaultBaseURL = "https://api.figma.com/v1"

const (
	defaultMaxRetryWait = 30 * time.Second
	defaultTimeout      = 10 * time.Minute
	defaultBackoff      = time.Second
	maxAttempts         = 3
	errorBodyLimit      = 8 << 10
)

// Client talks to Figma's REST API with a personal access token.
//
// Every call takes a context and ends with it, waits included. Figma meters
// requests in three tiers, and the expensive one (reading a file or nodes,
// rendering images) is as low as a handful of calls a month on a View seat,
// so the client counts what it sends (Requests) and never retries a rate
// limit it cannot wait out.
type Client struct {
	token        string
	base         string
	http         *http.Client
	maxRetryWait time.Duration
	maxBody      int64
	timeout      time.Duration
	backoff      time.Duration

	tier1, tier2, tier3 atomic.Int64
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client. The default one honours the proxy
// environment variables and has no timeout of its own.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

// WithBaseURL points the client at another API root, a test server for
// instance.
func WithBaseURL(base string) Option {
	return func(c *Client) {
		if base = strings.TrimRight(base, "/"); base != "" {
			c.base = base
		}
	}
}

// WithMaxRetryWait sets the longest Retry-After the client waits out itself
// (30 seconds by default). A longer one comes back as the error, so the
// caller can say when the allowance frees instead of hanging until then.
func WithMaxRetryWait(d time.Duration) Option {
	return func(c *Client) { c.maxRetryWait = d }
}

// WithMaxBodyBytes caps the JSON body the client reads. 0, the default, is
// no cap.
func WithMaxBodyBytes(n int64) Option {
	return func(c *Client) { c.maxBody = n }
}

// WithTimeout bounds a call or a download whose context has no deadline (10
// minutes by default, since a large file takes Figma minutes to serialise).
// 0 leaves it unbounded.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = d }
}

// NewClient returns a client for the given personal access token.
func NewClient(token string, opts ...Option) *Client {
	c := &Client{
		token:        strings.TrimSpace(token),
		base:         DefaultBaseURL,
		maxRetryWait: defaultMaxRetryWait,
		timeout:      defaultTimeout,
		backoff:      defaultBackoff,
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.http == nil {
		// A clone of the default transport keeps its proxy lookup, keep-alives
		// and HTTP/2, without sharing its connection pool with the host app.
		// A host that wrapped the default transport (tracing, a test double)
		// gets its wrapper used as it is: there is nothing to clone.
		transport := http.DefaultTransport
		if base, ok := transport.(*http.Transport); ok {
			transport = base.Clone()
		}
		c.http = &http.Client{Transport: transport}
	}
	return c
}

// RequestStats counts the requests a client has sent, retries included, by
// Figma's rate-limit tier.
type RequestStats struct {
	Tier1 int // file, nodes, image renders
	Tier2 int // image fills and the like
	Tier3 int // metadata
}

// Requests returns what the client has sent so far.
func (c *Client) Requests() RequestStats {
	return RequestStats{Tier1: int(c.tier1.Load()), Tier2: int(c.tier2.Load()), Tier3: int(c.tier3.Load())}
}

// tierOf is the rate-limit tier of an API path, per Figma's rate limit table.
func tierOf(endpoint string) int {
	switch {
	case strings.HasPrefix(endpoint, "/images/"):
		return 1
	case strings.HasPrefix(endpoint, "/files/"):
		rest := strings.Split(strings.TrimPrefix(endpoint, "/files/"), "/")
		if len(rest) == 1 {
			return 1
		}
		switch rest[1] {
		case "nodes":
			return 1
		case "meta", "styles", "components", "component_sets":
			return 3
		default:
			return 2
		}
	default:
		return 3
	}
}

func (c *Client) count(endpoint string) {
	switch tierOf(endpoint) {
	case 1:
		c.tier1.Add(1)
	case 2:
		c.tier2.Add(1)
	default:
		c.tier3.Add(1)
	}
}

// Get sends one GET to the API and decodes the JSON answer into out. path
// starts with a slash and may already carry a query; query is added to it.
// It is the door for a caller that decodes into its own types; File, Nodes,
// Images and ImageFills are built on it.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	endpoint := path
	if i := strings.IndexByte(endpoint, '?'); i >= 0 {
		endpoint = endpoint[:i]
	}
	target := c.base + path
	if len(query) > 0 {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		target += sep + query.Encode()
	}

	if _, ok := ctx.Deadline(); !ok && c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	for attempt := 1; ; attempt++ {
		err := c.once(ctx, endpoint, target, out)
		if err == nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("the Figma request was stopped: %w", ctxErr)
		}
		wait, retry := c.retryWait(err, attempt)
		if !retry || attempt == maxAttempts {
			return err
		}
		// A wait the context will not live through is not started: the
		// caller would get "deadline exceeded" and lose what Figma said,
		// the rate limit and when it frees above all.
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < wait {
			return err
		}
		if err := sleep(ctx, wait); err != nil {
			return fmt.Errorf("the Figma request was stopped: %w", err)
		}
	}
}

// permanentError marks a failure another attempt cannot fix.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// retryWait decides whether err is worth another attempt and how long to
// wait first. A rate limit is waited out only when Figma says it frees
// within the ceiling: a View seat's monthly allowance can answer with days.
func (c *Client) retryWait(err error, attempt int) (time.Duration, bool) {
	if _, ok := errors.AsType[*permanentError](err); ok {
		return 0, false
	}
	backoff := c.backoff << (attempt - 1)
	apiErr, ok := AsAPIError(err)
	if !ok {
		return backoff, true // could not reach Figma, or the body was cut
	}
	switch apiErr.Kind {
	case KindRateLimited:
		if apiErr.RetryAfter == 0 {
			return backoff, true
		}
		if apiErr.RetryAfter <= c.maxRetryWait {
			return apiErr.RetryAfter, true
		}
		return 0, false
	case KindServer:
		return backoff, true
	default:
		return 0, false
	}
}

func (c *Client) once(ctx context.Context, endpoint, target string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return &permanentError{err}
	}
	req.Header.Set("X-Figma-Token", c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "figma-extractor/"+Version)

	c.count(endpoint)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach Figma: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		return newAPIError(endpoint, resp.StatusCode, resp.Header, body)
	}

	// Decoded straight off the wire: no second copy of a body that can run
	// to hundreds of megabytes.
	body := &cappedReader{r: resp.Body, left: c.maxBody, unlimited: c.maxBody <= 0}
	if err := json.NewDecoder(body).Decode(out); err != nil {
		if body.exceeded {
			return &permanentError{fmt.Errorf("the answer from Figma for %s is larger than the %d MB limit", endpoint, c.maxBody>>20)}
		}
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
			return &permanentError{fmt.Errorf("could not read Figma's answer for %s: %w", endpoint, err)}
		}
		return fmt.Errorf("could not read Figma's answer for %s: %w", endpoint, err)
	}
	return nil
}

// cappedReader stops with an error once more than its allowance was read.
type cappedReader struct {
	r         io.Reader
	left      int64
	unlimited bool
	exceeded  bool
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.unlimited {
		return c.r.Read(p)
	}
	if c.left <= 0 {
		// One more byte than allowed means the body is over the cap.
		var probe [1]byte
		if n, _ := c.r.Read(probe[:]); n > 0 {
			c.exceeded = true
			return 0, errors.New("body over the size cap")
		}
		return 0, io.EOF
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// FileQuery narrows GET /v1/files/:key.
type FileQuery struct {
	// IDs keeps only these nodes, their children and their ancestors.
	IDs []string
	// Depth is how many levels below the document to return: 1 is the
	// pages, 2 adds each page's top-level layers. 0 is the whole tree.
	Depth int
	// Geometry asks for vector path data.
	Geometry bool
}

// File reads a file. Without a Depth it returns the whole document, which
// for a large file is slow and large: prefer Nodes with the frames you need.
func (c *Client) File(ctx context.Context, fileKey string, q FileQuery) (*FileResponse, error) {
	query := url.Values{}
	if ids := NormalizeNodeIDs(q.IDs); len(ids) > 0 {
		query.Set("ids", strings.Join(ids, ","))
	}
	if q.Depth > 0 {
		query.Set("depth", strconv.Itoa(q.Depth))
	}
	if q.Geometry {
		query.Set("geometry", "paths")
	}
	var out FileResponse
	if err := c.Get(ctx, "/files/"+url.PathEscape(fileKey), query, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// NodesQuery narrows GET /v1/files/:key/nodes.
type NodesQuery struct {
	// Depth is how many levels below each requested node to return. 0 is
	// the whole subtree.
	Depth int
	// Geometry asks for vector path data.
	Geometry bool
}

// Nodes reads the given nodes with their subtrees. The ids may be in any
// form; the answer is keyed by the colon form and holds nil for an id the
// file does not have (see NodesResponse.Missing).
func (c *Client) Nodes(ctx context.Context, fileKey string, ids []string, q NodesQuery) (*NodesResponse, error) {
	ids = NormalizeNodeIDs(ids)
	if len(ids) == 0 {
		return nil, errors.New("no node ids given")
	}
	query := url.Values{"ids": {strings.Join(ids, ",")}}
	if q.Depth > 0 {
		query.Set("depth", strconv.Itoa(q.Depth))
	}
	if q.Geometry {
		query.Set("geometry", "paths")
	}
	var out NodesResponse
	if err := c.Get(ctx, "/files/"+url.PathEscape(fileKey)+"/nodes", query, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ImageQuery shapes a render.
type ImageQuery struct {
	// Format is png (the default), jpg, svg or pdf.
	Format string
	// Scale is between 0.01 and 4 and applies to png and jpg. 0 means 1.
	Scale float64
	// UseAbsoluteBounds renders the node's full box even where it is empty
	// or cropped, which keeps a text node from being trimmed.
	UseAbsoluteBounds bool
}

// Images asks Figma to render the given nodes and returns a download URL
// per node id. The URL is empty for a node Figma could not render. The
// URLs live for 30 days. Figma renders at most 32 megapixels and scales a
// larger node down.
func (c *Client) Images(ctx context.Context, fileKey string, ids []string, q ImageQuery) (map[string]string, error) {
	ids = NormalizeNodeIDs(ids)
	if len(ids) == 0 {
		return nil, errors.New("no node ids given")
	}
	format := strings.ToLower(q.Format)
	if format == "" {
		format = "png"
	}
	query := url.Values{"ids": {strings.Join(ids, ",")}, "format": {format}}
	if format == "png" || format == "jpg" {
		scale := q.Scale
		if scale <= 0 {
			scale = 1
		}
		query.Set("scale", strconv.FormatFloat(scale, 'g', -1, 64))
	}
	if q.UseAbsoluteBounds {
		query.Set("use_absolute_bounds", "true")
	}
	endpoint := "/images/" + url.PathEscape(fileKey)
	var out ImageResponse
	if err := c.Get(ctx, endpoint, query, &out); err != nil {
		return nil, err
	}
	if out.Err != nil && *out.Err != "" {
		return nil, &APIError{Status: http.StatusOK, Kind: KindRenderFailed, Message: *out.Err, Endpoint: endpoint}
	}
	return out.Images, nil
}

// ImageFills returns a download URL for every picture used as a fill in the
// file, keyed by the imageRef a Paint carries. These are the original
// uploads, not renders. The URLs live for up to 14 days.
func (c *Client) ImageFills(ctx context.Context, fileKey string) (map[string]string, error) {
	var out FileImagesResponse
	if err := c.Get(ctx, "/files/"+url.PathEscape(fileKey)+"/images", nil, &out); err != nil {
		return nil, err
	}
	return out.Meta.Images, nil
}

// Downloaded describes a file Download wrote.
type Downloaded struct {
	Path  string
	MIME  string // sniffed from the first bytes, "image/png" for example
	Bytes int64
}

// Download saves a URL Figma handed out (a render or an image fill) to dest.
// The bytes go to a temporary file beside dest that is renamed over it only
// when the download is whole, so a stopped or failed download leaves
// nothing behind. maxBytes, when above 0, refuses a larger file. The token
// is not sent: these URLs are pre-signed and go to a storage host.
func (c *Client) Download(ctx context.Context, rawURL, dest string, maxBytes int64) (Downloaded, error) {
	// Bounded like an API call when the caller set no deadline: a storage
	// host that stalls mid-file must not hold the caller forever.
	if _, ok := ctx.Deadline(); !ok && c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	const attempts = 2
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		got, retry, err := c.downloadOnce(ctx, rawURL, dest, maxBytes)
		if err == nil {
			return got, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Downloaded{}, fmt.Errorf("the download was stopped: %w", ctxErr)
		}
		lastErr = err
		if !retry || attempt == attempts {
			break
		}
		if err := sleep(ctx, c.backoff); err != nil {
			return Downloaded{}, fmt.Errorf("the download was stopped: %w", err)
		}
	}
	return Downloaded{}, lastErr
}

func (c *Client) downloadOnce(ctx context.Context, rawURL, dest string, maxBytes int64) (got Downloaded, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Downloaded{}, false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Downloaded{}, true, fmt.Errorf("could not download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Downloaded{}, resp.StatusCode >= 500, fmt.Errorf("the download answered status %d", resp.StatusCode)
	}

	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Downloaded{}, false, err
	}
	tmp, err := os.CreateTemp(dir, ".figma-*.part")
	if err != nil {
		return Downloaded{}, false, err
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		tmp.Close()
		if !keep {
			os.Remove(tmpPath)
		}
	}()

	var body io.Reader = resp.Body
	if maxBytes > 0 {
		body = io.LimitReader(resp.Body, maxBytes+1)
	}
	buffered := bufio.NewReaderSize(body, 32<<10)
	head, _ := buffered.Peek(512)
	mime := sniffMIME(head)

	n, err := io.Copy(tmp, buffered)
	if err != nil {
		return Downloaded{}, true, fmt.Errorf("could not save the download: %w", err)
	}
	if maxBytes > 0 && n > maxBytes {
		return Downloaded{}, false, fmt.Errorf("the file is larger than the %d MB limit", maxBytes>>20)
	}
	if err := tmp.Close(); err != nil {
		return Downloaded{}, false, err
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return Downloaded{}, false, err
	}
	keep = true
	return Downloaded{Path: dest, MIME: mime, Bytes: n}, false, nil
}

// sniffMIME names the type of a file from its first bytes. An SVG is XML or
// plain text to the standard sniffer, so it is looked for by its root tag.
func sniffMIME(head []byte) string {
	mime := http.DetectContentType(head)
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}
	if (mime == "text/xml" || mime == "text/plain") && bytes.Contains(head, []byte("<svg")) {
		return "image/svg+xml"
	}
	return mime
}

// ExtensionForMIME is the file extension, without the dot, for an image
// type Download can report, or "" for a type it does not know.
func ExtensionForMIME(mime string) string {
	switch mime {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	case "image/bmp":
		return "bmp"
	case "image/svg+xml":
		return "svg"
	case "application/pdf":
		return "pdf"
	default:
		return ""
	}
}
