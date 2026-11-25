package backend

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// stubRoundTripper answers with 401 for the revoked token and 200 for the
// fresh one, recording the bearer token of every request.
type stubRoundTripper struct {
	mu     sync.Mutex
	tokens []string
}

func (s *stubRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	tok := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	s.tokens = append(s.tokens, tok)
	s.mu.Unlock()
	if tok == "fresh-token" {
		return &http.Response{
			Status:     "200 OK",
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Request:    req,
		}, nil
	}
	return &http.Response{
		Status:     "401 Unauthorized",
		StatusCode: http.StatusUnauthorized,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Request:    req,
	}, nil
}

// TestRefreshingBearerTransport_RetriesOnceAfter401 (fourth-audit finding
// 35): a revoked standalone-source token is force-refreshed once under
// the per-source lock and the request retried with the fresh token.
func TestRefreshingBearerTransport_RetriesOnceAfter401(t *testing.T) {
	api := NewAPI(nil, nil, nil, nil, nil)
	api.SetStandaloneSourceTokenGetter(func(sourceID string) (string, error) {
		return "revoked-token", nil
	})
	refreshes := 0
	api.SetStandaloneSourceTokenRefresher(func(sourceID string) (string, error) {
		refreshes++
		return "fresh-token", nil
	})

	base := &stubRoundTripper{}
	tr := &refreshingBearerTransport{base: base, api: api, sourceID: "src-1", token: "revoked-token"}
	req, err := http.NewRequest(http.MethodGet, "https://example.invalid/contacts", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after the 401 retry", resp.StatusCode)
	}
	if refreshes != 1 {
		t.Fatalf("refreshes = %d, want exactly 1", refreshes)
	}
	if got := base.tokens; len(got) != 2 || got[0] != "revoked-token" || got[1] != "fresh-token" {
		t.Fatalf("request tokens = %v, want [revoked-token fresh-token]", got)
	}
}

// TestRefreshingBearerTransport_ReusesConcurrentRefresh: when the stored
// token already changed (another goroutine refreshed), the retry uses it
// without refreshing again.
func TestRefreshingBearerTransport_ReusesConcurrentRefresh(t *testing.T) {
	api := NewAPI(nil, nil, nil, nil, nil)
	current := "revoked-token"
	api.SetStandaloneSourceTokenGetter(func(sourceID string) (string, error) {
		return current, nil
	})
	refreshes := 0
	api.SetStandaloneSourceTokenRefresher(func(sourceID string) (string, error) {
		refreshes++
		return "fresh-token", nil
	})

	base := &stubRoundTripper{}
	tr := &refreshingBearerTransport{base: base, api: api, sourceID: "src-1", token: "revoked-token"}
	// Simulate a concurrent refresh having landed before this transport
	// re-reads under the lock.
	current = "fresh-token"

	req, err := http.NewRequest(http.MethodGet, "https://example.invalid/contacts", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if refreshes != 0 {
		t.Fatalf("refreshes = %d, want 0 (stored token had already changed)", refreshes)
	}
}

// TestRefreshingBearerTransport_NoRefresherReturns401: with no refresher
// wired, the 401 is returned unchanged (pre-fix behaviour).
func TestRefreshingBearerTransport_NoRefresherReturns401(t *testing.T) {
	api := NewAPI(nil, nil, nil, nil, nil)
	base := &stubRoundTripper{}
	tr := &refreshingBearerTransport{base: base, api: api, sourceID: "src-1", token: "revoked-token"}
	req, err := http.NewRequest(http.MethodGet, "https://example.invalid/contacts", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 with no refresher wired", resp.StatusCode)
	}
}
