package figma

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc, opts ...Option) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewClient("test-token", append([]Option{WithBaseURL(srv.URL)}, opts...)...)
	c.backoff = 5 * time.Millisecond
	return c, srv
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestNodesSendsTokenAndColonIDsAndSeesNull(t *testing.T) {
	var gotToken, gotIDs, gotPath string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotToken, gotIDs, gotPath = r.Header.Get("X-Figma-Token"), r.URL.Query().Get("ids"), r.URL.Path
		w.Write(fixture(t, "card.json"))
	})

	// The dash form is what a person copies out of a link.
	resp, err := c.Nodes(context.Background(), "KEY", []string{"1-2", " 9%3A999 ", "1:2"}, NodesQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if gotToken != "test-token" || gotPath != "/files/KEY/nodes" {
		t.Errorf("token %q path %q", gotToken, gotPath)
	}
	if gotIDs != "1:2,9:999" {
		t.Errorf("ids sent as %q, want the colon form without duplicates", gotIDs)
	}
	if resp.Nodes["1:2"] == nil || resp.Nodes["1:2"].Document.Name != "Card" {
		t.Errorf("the card did not decode")
	}
	if missing := resp.Missing([]string{"1:2", "9:999"}); len(missing) != 1 || missing[0] != "9:999" {
		t.Errorf("Missing = %v, want only the null node", missing)
	}
	if got := c.Requests(); got.Tier1 != 1 || got.Tier2 != 0 {
		t.Errorf("request count %+v, want one Tier 1 call", got)
	}
}

func TestRateLimitThatFreesSoonIsWaitedOut(t *testing.T) {
	var hits atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"name":"ok","document":{"id":"0:0"}}`))
	})
	start := time.Now()
	file, err := c.File(context.Background(), "KEY", FileQuery{Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if file.Name != "ok" || hits.Load() != 2 {
		t.Errorf("name %q after %d hits", file.Name, hits.Load())
	}
	if waited := time.Since(start); waited < 900*time.Millisecond {
		t.Errorf("retried after %v, before Retry-After had passed", waited)
	}
}

func TestRateLimitThatFreesLateComesBackAtOnce(t *testing.T) {
	var hits atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "259200")
		w.Header().Set("X-Figma-Plan-Tier", "starter")
		w.Header().Set("X-Figma-Rate-Limit-Type", "low")
		w.Header().Set("X-Figma-Upgrade-Link", "https://www.figma.com/pricing")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"status":429,"err":"Rate limit exceeded"}`))
	})
	start := time.Now()
	_, err := c.File(context.Background(), "KEY", FileQuery{})
	apiErr, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("err = %v, want an *APIError", err)
	}
	if apiErr.Kind != KindRateLimited || apiErr.RetryAfter != 72*time.Hour || apiErr.PlanTier != "starter" || apiErr.RateLimitType != "low" {
		t.Errorf("got %+v", apiErr)
	}
	if hits.Load() != 1 {
		t.Errorf("%d requests spent on a limit that frees in three days, want 1", hits.Load())
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("waited on a limit it could not outlast")
	}
}

func TestCallEndsWhenContextEnds(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, err := c.Nodes(ctx, "KEY", []string{"1:2"}, NodesQuery{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("the call outlived its context")
	}
}

func TestRetryWaitEndsWhenContextEnds(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "20")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(80*time.Millisecond, cancel)
	start := time.Now()
	_, err := c.File(ctx, "KEY", FileQuery{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("the retry wait outlived its context")
	}
}

func TestServerErrorIsRetried(t *testing.T) {
	var hits atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte(`{"name":"ok","document":{"id":"0:0"}}`))
	})
	if _, err := c.File(context.Background(), "KEY", FileQuery{}); err != nil {
		t.Fatal(err)
	}
	if got := c.Requests().Tier1; got != 3 {
		t.Errorf("counted %d Tier 1 requests, want 3 (retries spend allowance too)", got)
	}
}

func TestRefusalsAreClassifiedAndNotRetried(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   ErrorKind
	}{
		{"invalid token", 403, `{"status":403,"err":"Invalid token"}`, KindTokenRejected},
		{"expired token", 403, `{"status":403,"err":"Token expired"}`, KindTokenRejected},
		{"unauthenticated", 401, `{"status":401,"err":"Unauthorized"}`, KindTokenRejected},
		{"missing scope", 403, `{"status":403,"err":"Invalid scope(s): file_metadata:read. This endpoint requires the file_content:read scope"}`, KindMissingScope},
		{"no access", 403, `{"status":403,"err":"Forbidden"}`, KindForbidden},
		{"not found", 404, `{"status":404,"err":"Not found"}`, KindNotFound},
		{"not found, message form", 404, `{"error":true,"status":404,"message":"Not found"}`, KindNotFound},
		{"too large", 400, `{"status":400,"err":"Request too large. If applicable, filter by query params"}`, KindTooLarge},
		{"render timeout", 400, `{"status":400,"err":"Render timeout, try requesting fewer or smaller images"}`, KindRenderFailed},
		{"bad parameter", 400, `{"status":400,"err":"Invalid scale"}`, KindBadRequest},
		{"html from a proxy", 407, "<html>\n<body>Proxy authentication required</body></html>", KindBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int32
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			})
			_, err := c.Nodes(context.Background(), "KEY", []string{"1:2"}, NodesQuery{})
			if got := KindOf(err); got != tt.want {
				t.Errorf("kind = %q (%v), want %q", got, err, tt.want)
			}
			if hits.Load() != 1 {
				t.Errorf("%d requests, want 1: a refusal is not retried", hits.Load())
			}
			if strings.Contains(err.Error(), "<html>") || strings.Contains(err.Error(), "\n") {
				t.Errorf("the error carries raw markup or a line break: %q", err.Error())
			}
		})
	}
}

func TestBodyOverTheCapIsRefused(t *testing.T) {
	var hits atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprintf(w, `{"name":%q}`, strings.Repeat("x", 4<<20))
	}, WithMaxBodyBytes(1<<20))
	_, err := c.File(context.Background(), "KEY", FileQuery{})
	if err == nil || !strings.Contains(err.Error(), "larger than the 1 MB limit") {
		t.Errorf("err = %v, want the size-limit sentence", err)
	}
	if hits.Load() != 1 {
		t.Errorf("%d requests, want 1", hits.Load())
	}
}

func TestBodyUnderTheCapDecodes(t *testing.T) {
	body := `{"name":"just under"}`
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}, WithMaxBodyBytes(int64(len(body))))
	file, err := c.File(context.Background(), "KEY", FileQuery{})
	if err != nil || file.Name != "just under" {
		t.Errorf("file %+v, err %v", file, err)
	}
}

func TestImageFillsReadsTheMetaMap(t *testing.T) {
	var gotPath string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"error":false,"status":200,"meta":{"images":{"ref_photo":"https://example.invalid/a","ref_b":"https://example.invalid/b"}},"i18n":null}`))
	})
	fills, err := c.ImageFills(context.Background(), "KEY")
	if err != nil {
		t.Fatal(err)
	}
	if len(fills) != 2 || fills["ref_photo"] != "https://example.invalid/a" {
		t.Errorf("fills = %v", fills)
	}
	if gotPath != "/files/KEY/images" {
		t.Errorf("path %q", gotPath)
	}
	if got := c.Requests(); got.Tier1 != 0 || got.Tier2 != 1 {
		t.Errorf("request count %+v, want one Tier 2 call", got)
	}
}

func TestImagesKeepsAFailedRenderAsEmpty(t *testing.T) {
	var query string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Write([]byte(`{"err":null,"images":{"1:2":"https://example.invalid/r","1:3":null}}`))
	})
	urls, err := c.Images(context.Background(), "KEY", []string{"1-2", "1:3"}, ImageQuery{Format: "PNG", Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	if urls["1:2"] == "" || urls["1:3"] != "" {
		t.Errorf("urls = %v", urls)
	}
	if _, present := urls["1:3"]; !present {
		t.Errorf("the failed render is not in the map")
	}
	if !strings.Contains(query, "format=png") || !strings.Contains(query, "scale=2") {
		t.Errorf("query %q", query)
	}

	// svg has no scale.
	if _, err := c.Images(context.Background(), "KEY", []string{"1:2"}, ImageQuery{Format: "svg", Scale: 3}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(query, "scale=") {
		t.Errorf("an svg render was sent with a scale: %q", query)
	}
}

func TestImagesErrFieldIsARenderFailure(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"err":"Unexpected rendering error","images":null}`))
	})
	_, err := c.Images(context.Background(), "KEY", []string{"1:2"}, ImageQuery{})
	if KindOf(err) != KindRenderFailed {
		t.Errorf("err = %v, want a render failure", err)
	}
}

func partFiles(t *testing.T, dir string) []string {
	t.Helper()
	parts, err := filepath.Glob(filepath.Join(dir, ".figma-*.part"))
	if err != nil {
		t.Fatal(err)
	}
	return parts
}

func TestDownloadIsWholeSniffedAndSendsNoToken(t *testing.T) {
	picture := pngBytes(t, 3, 2)
	var sawToken bool
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		sawToken = r.Header.Get("X-Figma-Token") != ""
		w.Write(picture)
	})
	dir := t.TempDir()
	dest := filepath.Join(dir, "sub", "photo.bin")
	got, err := c.Download(context.Background(), srv.URL+"/img/no-extension?sig=1", dest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.MIME != "image/png" || got.Bytes != int64(len(picture)) || ExtensionForMIME(got.MIME) != "png" {
		t.Errorf("got %+v", got)
	}
	if onDisk, _ := os.ReadFile(dest); !bytes.Equal(onDisk, picture) {
		t.Errorf("the saved bytes differ from the served ones")
	}
	if sawToken {
		t.Errorf("the token was sent to the storage host")
	}
	if parts := partFiles(t, filepath.Dir(dest)); len(parts) != 0 {
		t.Errorf("left temporary files: %v", parts)
	}
}

func TestDownloadSniffsSVG(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<svg width="16" height="16" xmlns="http://www.w3.org/2000/svg"><path d="M0 0h16v16H0z"/></svg>`))
	})
	got, err := c.Download(context.Background(), srv.URL+"/x", filepath.Join(t.TempDir(), "icon"), 0)
	if err != nil || got.MIME != "image/svg+xml" {
		t.Errorf("got %+v, err %v", got, err)
	}
}

func TestDownloadOverTheLimitLeavesNothing(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("x"), 3<<20))
	})
	dir := t.TempDir()
	dest := filepath.Join(dir, "big.png")
	if _, err := c.Download(context.Background(), srv.URL+"/x", dest, 1<<20); err == nil {
		t.Fatal("a 3 MB download passed a 1 MB limit")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("the refused file was left on disk")
	}
	if parts := partFiles(t, dir); len(parts) != 0 {
		t.Errorf("left temporary files: %v", parts)
	}
}

func TestStoppedDownloadLeavesNothing(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("x"), 64<<10))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	dir := t.TempDir()
	dest := filepath.Join(dir, "cut.png")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(80*time.Millisecond, cancel)
	_, err := c.Download(ctx, srv.URL+"/x", dest, 0)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("a cut-off file was left at the destination")
	}
	if parts := partFiles(t, dir); len(parts) != 0 {
		t.Errorf("left temporary files: %v", parts)
	}
}

func TestTierOf(t *testing.T) {
	tests := map[string]int{
		"/files/KEY":            1,
		"/files/KEY/nodes":      1,
		"/images/KEY":           1,
		"/files/KEY/images":     2,
		"/files/KEY/comments":   2,
		"/files/KEY/meta":       3,
		"/files/KEY/styles":     3,
		"/files/KEY/components": 3,
		"/me":                   3,
	}
	for endpoint, want := range tests {
		if got := tierOf(endpoint); got != want {
			t.Errorf("tierOf(%q) = %d, want %d", endpoint, got, want)
		}
	}
}
