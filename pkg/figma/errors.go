package figma

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrorKind says what a refusal from Figma means, so a caller can pick the
// next step without reading the message.
type ErrorKind string

const (
	// KindTokenRejected: the token is invalid or expired.
	KindTokenRejected ErrorKind = "token_rejected"
	// KindMissingScope: the token is valid but lacks a scope the call needs.
	KindMissingScope ErrorKind = "missing_scope"
	// KindForbidden: the token is valid but its account may not open the file.
	KindForbidden ErrorKind = "forbidden"
	// KindNotFound: no such file or node, or one the token cannot see.
	KindNotFound ErrorKind = "not_found"
	// KindRateLimited: the allowance for this file's plan and seat is used up.
	KindRateLimited ErrorKind = "rate_limited"
	// KindTooLarge: the request asked for more than Figma will return at once.
	KindTooLarge ErrorKind = "too_large"
	// KindRenderFailed: the images endpoint could not render the batch.
	KindRenderFailed ErrorKind = "render_failed"
	// KindBadRequest: any other 4xx.
	KindBadRequest ErrorKind = "bad_request"
	// KindServer: a 5xx.
	KindServer ErrorKind = "server"
)

// APIError is a refusal from Figma's API. The rate-limit fields are set only
// for KindRateLimited.
type APIError struct {
	Status   int
	Kind     ErrorKind
	Message  string // Figma's own sentence, when it sent one
	Endpoint string // the path of the call, no host and no query

	RetryAfter    time.Duration // how long until the allowance frees
	PlanTier      string        // enterprise, org, pro, starter or student
	RateLimitType string        // "low" for View and Collab seats, "high" for Full and Dev
	UpgradeLink   string
}

func (e *APIError) Error() string {
	var what string
	switch e.Kind {
	case KindTokenRejected:
		what = "Figma rejected the token"
	case KindMissingScope:
		what = "the Figma token lacks a scope this call needs"
	case KindForbidden:
		what = "the Figma token's account may not open this file"
	case KindNotFound:
		what = "Figma has no such file or node for this token"
	case KindRateLimited:
		what = "Figma's rate limit is used up"
		if e.RetryAfter > 0 {
			what += ", it frees in " + e.RetryAfter.Round(time.Second).String()
		}
	case KindTooLarge:
		what = "the request is larger than Figma returns at once"
	case KindRenderFailed:
		what = "Figma could not render the images"
	case KindServer:
		what = "Figma answered with a server error"
	default:
		what = "Figma refused the request"
	}
	s := fmt.Sprintf("%s (status %d", what, e.Status)
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s + ")"
}

// AsAPIError returns the *APIError inside err, if there is one.
func AsAPIError(err error) (*APIError, bool) {
	if apiErr, ok := errors.AsType[*APIError](err); ok {
		return apiErr, true
	}
	return nil, false
}

// KindOf is the kind of the *APIError inside err, or "" when err is not one.
func KindOf(err error) ErrorKind {
	if apiErr, ok := AsAPIError(err); ok {
		return apiErr.Kind
	}
	return ""
}

// newAPIError builds the error for a non-200 answer. Figma writes its reason
// as {"status":403,"err":"Invalid token"} on the file endpoints and as
// {"error":true,"status":404,"message":"Not found"} elsewhere; both are read.
func newAPIError(endpoint string, status int, header http.Header, body []byte) *APIError {
	e := &APIError{Status: status, Endpoint: endpoint}

	var parsed struct {
		Err     string `json:"err"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &parsed) == nil {
		e.Message = parsed.Err
		if e.Message == "" {
			e.Message = parsed.Message
		}
	}
	if e.Message == "" {
		// Not Figma's JSON. A plain-text line is kept; a proxy's or gateway's
		// HTML page says nothing a person can act on, so it is dropped.
		if text := firstLine(string(body), 200); !strings.HasPrefix(text, "<") && !strings.HasPrefix(text, "{") {
			e.Message = text
		}
	}

	lower := strings.ToLower(e.Message)
	switch {
	case status == http.StatusUnauthorized:
		e.Kind = KindTokenRejected
	case status == http.StatusForbidden:
		switch {
		case strings.Contains(lower, "scope"):
			e.Kind = KindMissingScope
		case strings.Contains(lower, "token"):
			e.Kind = KindTokenRejected
		default:
			e.Kind = KindForbidden
		}
	case status == http.StatusNotFound:
		e.Kind = KindNotFound
	case status == http.StatusTooManyRequests:
		e.Kind = KindRateLimited
		if secs, err := strconv.Atoi(strings.TrimSpace(header.Get("Retry-After"))); err == nil && secs > 0 {
			e.RetryAfter = time.Duration(secs) * time.Second
		}
		e.PlanTier = header.Get("X-Figma-Plan-Tier")
		e.RateLimitType = header.Get("X-Figma-Rate-Limit-Type")
		e.UpgradeLink = header.Get("X-Figma-Upgrade-Link")
	case status == http.StatusRequestEntityTooLarge, strings.Contains(lower, "too large"):
		e.Kind = KindTooLarge
	case strings.Contains(lower, "render"):
		e.Kind = KindRenderFailed
	case status >= 500:
		e.Kind = KindServer
	default:
		e.Kind = KindBadRequest
	}
	return e
}

func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}
