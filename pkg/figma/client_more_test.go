package figma

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// wrapped is what a host app puts in place of http.DefaultTransport: a
// tracer, a recorder, a test double.
type wrapped struct{ next http.RoundTripper }

func (w wrapped) RoundTrip(r *http.Request) (*http.Response, error) { return w.next.RoundTrip(r) }

// The default transport is not always an *http.Transport. A client built in
// an app that wrapped it must still work.
func TestNewClientWorksWhenTheDefaultTransportIsWrapped(t *testing.T) {
	prev := http.DefaultTransport
	http.DefaultTransport = wrapped{prev}
	t.Cleanup(func() { http.DefaultTransport = prev })

	_, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"ok","document":{"id":"0:0"}}`))
	})
	c := NewClient("tok", WithBaseURL(srv.URL)) // built with no HTTP client of its own
	file, err := c.File(context.Background(), "KEY", FileQuery{Depth: 1})
	if err != nil || file.Name != "ok" {
		t.Errorf("file %+v, err %v", file, err)
	}
}

// A storage host that stalls mid-file must not hold a caller that set no
// deadline (the command-line tool) forever.
func TestDownloadIsBoundedWhenTheContextHasNoDeadline(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("x"), 16<<10))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}, WithTimeout(200*time.Millisecond))
	dir := t.TempDir()
	dest := filepath.Join(dir, "stalled.png")

	done := make(chan error, 1)
	go func() {
		_, err := c.Download(context.Background(), srv.URL+"/x", dest, 0)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Errorf("a stalled download reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the download is still blocked after 5s although the client's timeout is 200ms")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("a cut-off file was left at the destination")
	}
	if parts := partFiles(t, dir); len(parts) != 0 {
		t.Errorf("left temporary files: %v", parts)
	}
}

// Figma says the limit frees in 20 seconds; the caller has 1 left. Waiting
// would end in "deadline exceeded" and lose the one thing worth knowing.
func TestARateLimitPastTheDeadlineKeepsItsMeaning(t *testing.T) {
	var hits atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "20")
		w.Header().Set("X-Figma-Rate-Limit-Type", "low")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err := c.File(ctx, "KEY", FileQuery{})

	apiErr, ok := AsAPIError(err)
	if !ok || apiErr.Kind != KindRateLimited || apiErr.RetryAfter != 20*time.Second || apiErr.RateLimitType != "low" {
		t.Errorf("err = %v, want the rate-limit error with its Retry-After", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("waited %v on a limit that frees after the deadline", elapsed)
	}
	if hits.Load() != 1 {
		t.Errorf("%d requests, want 1", hits.Load())
	}
}

// The second and last attempt of a download is not followed by a pause.
func TestDownloadDoesNotPauseAfterItsLastAttempt(t *testing.T) {
	var hits atomic.Int32
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	})
	c.backoff = 400 * time.Millisecond
	start := time.Now()
	if _, err := c.Download(context.Background(), srv.URL+"/x", filepath.Join(t.TempDir(), "a.png"), 0); err == nil {
		t.Fatal("a 502 reported success")
	}
	if hits.Load() != 2 {
		t.Errorf("%d attempts, want 2", hits.Load())
	}
	// One pause between the two attempts, none after the second.
	if elapsed := time.Since(start); elapsed > 700*time.Millisecond {
		t.Errorf("took %v: it paused after the attempt that ended the download", elapsed)
	}
}
