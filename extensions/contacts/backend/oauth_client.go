package backend

import (
	"fmt"
	"io"
	"net/http"

	"github.com/absonsnet/daak/internal/carddav"
	coreapi "github.com/absonsnet/daak/internal/core/api/v1"
)

// cardDAVWriteScope is the nominal scope passed when building a bearer client for an
// account-linked CardDAV write. Account-linked CardDAV sources are custom-OAuth-only,
// and the broker skips scope gating for custom (single opaque grant), so this never
// drives incremental consent — it's a label for diagnostics.
var cardDAVWriteScope = coreapi.AuthScope{
	Resource: "carddav",
	Reason:   "Write contacts to your CardDAV server",
}

// httpClientForSource returns an authenticated *http.Client for a Google /
// Microsoft contact source, dispatching on whether the source is linked to
// an email account or is a standalone contacts-only OAuth source.
//
// Mirrors `internal/carddav/sync.go::Syncer.getOAuthToken` — the sync side
// already handles both modes; the write side was account-linked-only before
// this, which produced "<provider> source has no linked account" for users
// who set up the source via the contacts-only OAuth flow rather than via
// link-to-existing-account.
//
// Account-linked path: delegates to the Auth Broker (core.Auth().HTTPClient),
// which captures per-(account, client_config) refresh state and routes scopes
// per the manifest's first_party_uses_core_for_scopes declaration.
//
// Standalone path: fetches a token via the host's getValidContactSourceOAuth
// token (proactively refreshed on expiry by the host), then wraps it in a
// bearer transport that refreshes once under a lock and retries on 401
// (fourth-audit finding 35) — a revoked token no longer fails every write
// until expiry.
func (a *API) httpClientForSource(source *carddav.Source, scope coreapi.AuthScope) (*http.Client, error) {
	if source == nil {
		return nil, fmt.Errorf("contacts: httpClientForSource: nil source")
	}

	if source.AccountID != nil && *source.AccountID != "" {
		if a.core == nil {
			return nil, fmt.Errorf("contacts: httpClientForSource: core not wired")
		}
		return a.core.Auth().HTTPClient(*source.AccountID, []coreapi.AuthScope{scope})
	}

	if a.getStandaloneSourceToken == nil {
		return nil, fmt.Errorf("contacts: httpClientForSource: standalone-source token getter not wired (source %s)", source.ID)
	}
	token, err := a.getStandaloneSourceToken(source.ID)
	if err != nil {
		return nil, fmt.Errorf("contacts: httpClientForSource: get standalone token for %s: %w", source.ID, err)
	}
	return &http.Client{
		Transport: &refreshingBearerTransport{
			api:      a,
			sourceID: source.ID,
			token:    token,
		},
	}, nil
}

// refreshingBearerTransport stamps a bearer token on every request and,
// on a 401, refreshes once under the per-source lock and retries —
// mirroring internal/extensions/auth.bearerRefreshTransport. A revoked
// token used to fail every write until its expiry; now the first 401
// triggers one force-refresh and a single retry with the fresh token.
//
//   - The token is re-read under the lock first: another goroutine may
//     have refreshed already, in which case the stored token differs
//     from the one that got the 401 and the retry uses it without
//     refreshing again (avoiding invalid_grant on the rotated token).
//   - With no refresher wired (tests, partial construction) the 401
//     response is returned unchanged — the pre-fix behaviour.
type refreshingBearerTransport struct {
	base     http.RoundTripper
	api      *API
	sourceID string
	token    string
}

func (t *refreshingBearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.do(req, t.token)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	// No refresher wired (tests, partial construction): return the 401
	// unchanged — the pre-fix behaviour.
	if t.api == nil || t.api.refreshStandaloneSourceToken == nil {
		return resp, nil
	}

	// 401 with a refresher: drain + close body before retrying.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	failedToken := t.token
	mu := t.api.standaloneRefreshLock(t.sourceID)
	mu.Lock()
	defer mu.Unlock()

	// Re-read: another goroutine may have refreshed already.
	if current, err := t.api.getStandaloneSourceToken(t.sourceID); err == nil && current != "" && current != failedToken {
		return t.do(req, current)
	}

	fresh, err := t.api.refreshStandaloneSourceToken(t.sourceID)
	if err != nil {
		return nil, fmt.Errorf("contacts: refresh standalone token after 401 for %s: %w", t.sourceID, err)
	}
	return t.do(req, fresh)
}

func (t *refreshingBearerTransport) do(req *http.Request, token string) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	if cloned.Header == nil {
		cloned.Header = make(http.Header)
	}
	cloned.Header.Set("Authorization", "Bearer "+token)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(cloned)
}
