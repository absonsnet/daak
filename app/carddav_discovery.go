package app

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/absonsnet/daak/internal/account"
	"github.com/absonsnet/daak/internal/carddav"
	"github.com/absonsnet/daak/internal/kit/davutil"
	"github.com/absonsnet/daak/internal/logging"
)

// CardDAV auto-discovery on account add.
//
// When a new IMAP account is added, Daak probes for a CardDAV address book
// and, when one answers, creates a linked CardDAV source automatically —
// so the send-time auto-add (see app/account_contacts.go) has somewhere to
// write to without the user ever opening Settings → Contacts.
//
// Best-effort and non-blocking by design: it runs on its own goroutine
// (callers fire and forget), it never delays or fails account setup, and
// when nothing is found — or discovery errors — it creates nothing and
// logs at Debug only. The manual path (Settings → Contacts → add source)
// is unchanged.

// cardDAVPresets maps an email domain to a known CardDAV server base URL.
// These endpoints were verified live (Nov 21, 2026): each answers on
// /.well-known/carddav. Presets are tried before the generic derivation
// because several providers redirect their mail-domain .well-known to a
// different host (www subdomain) or port — the discovered address-book
// paths live on the redirect target, so storing the mail domain as the
// source URL would point the syncer at the wrong host. Presets name the
// real host directly.
var cardDAVPresets = map[string]string{
	// Apple
	"icloud.com": "https://contacts.icloud.com",
	"me.com":     "https://contacts.icloud.com",
	"mac.com":    "https://contacts.icloud.com",
	// Fastmail (one CardDAV host serves all Fastmail domains)
	"fastmail.com":    "https://carddav.fastmail.com",
	"fastmail.fm":     "https://carddav.fastmail.com",
	"fastmail.net":    "https://carddav.fastmail.com",
	"fastmail.co.uk":  "https://carddav.fastmail.com",
	"fastmail.us":     "https://carddav.fastmail.com",
	"fastmail.com.au": "https://carddav.fastmail.com",
	// GMX / Mail.com
	"gmx.com":  "https://carddav.gmx.net",
	"gmx.net":  "https://carddav.gmx.net",
	"gmx.de":   "https://carddav.gmx.net",
	"gmx.at":   "https://carddav.gmx.net",
	"gmx.ch":   "https://carddav.gmx.net",
	"gmx.fr":   "https://carddav.gmx.net",
	"mail.com": "https://carddav.mail.com",
	// mailbox.org
	"mailbox.org": "https://dav.mailbox.org",
	// Posteo: the mail domain's .well-known redirects to port 8843.
	"posteo.de": "https://posteo.de:8843",
}

// cardDAVDiscoveryTimeout bounds one candidate attempt. The discovery
// client itself has a 30 s per-request timeout and tries several methods
// per base URL, so without a cap a dead host could keep the background
// sweep busy for minutes.
const cardDAVDiscoveryTimeout = 20 * time.Second

// cardDAVDiscoveryCandidates returns the CardDAV base URLs to try for an
// account, in order: provider preset (when the email domain matches one),
// the generic https://<email-domain> (RFC 6764 .well-known derivation),
// then https://<imap-host> (self-hosted / unified servers where DAV lives
// on the mail host). Duplicates are removed, order preserved.
func cardDAVDiscoveryCandidates(acc *account.Account) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(u string) {
		u = strings.TrimSuffix(strings.TrimSpace(u), "/")
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, u)
	}

	domain := ""
	if parts := strings.Split(acc.Email, "@"); len(parts) == 2 {
		domain = strings.ToLower(strings.TrimSpace(parts[1]))
	}
	if preset, ok := cardDAVPresets[domain]; ok {
		add(preset)
	}
	if domain != "" {
		add("https://" + domain)
	}
	if acc.IMAPHost != "" {
		add("https://" + strings.TrimSpace(acc.IMAPHost))
	}
	return out
}

// autoDiscoverCardDAVForAccount attempts CardDAV auto-discovery for a newly
// added account and creates a linked source when an address book is found.
// It is always called on a fresh goroutine (fire and forget) and never
// returns an error to the caller — every failure is a Debug log.
//
// password is the account's IMAP password (password accounts only);
// useOAuth selects bearer discovery with the account's token (custom-OAuth
// accounts). Google/Microsoft accounts never reach here — their contacts
// live in People/Graph, not CardDAV.
func (a *App) autoDiscoverCardDAVForAccount(acc *account.Account, password string, useOAuth bool) {
	defer recoverPanic("app.carddav-discovery", "auto-discover CardDAV")
	log := logging.WithComponent("app.carddav-discovery")

	if acc == nil {
		return
	}
	if acc.SharedMailboxParentID != "" {
		return
	}

	// Google/Microsoft accounts keep their contacts in People/Graph.
	// (Also covers manual Gmail/Outlook IMAP adds, which have no OAuth
	// token for the provider APIs anyway.)
	if acc.AuthType == account.AuthOAuth2 {
		if provider, err := a.credStore.GetOAuthProvider(acc.ID); err == nil {
			if provider == "google" || provider == "microsoft" {
				return
			}
		}
	}
	if strings.Contains(acc.IMAPHost, "gmail") || strings.Contains(acc.IMAPHost, "outlook.office365") {
		return
	}

	// Already linked — nothing to discover (covers re-adds and the manual
	// path: a user-added source always wins).
	if existing, _ := a.carddavStore.GetSourceByAccountID(acc.ID); existing != nil {
		return
	}

	candidates := cardDAVDiscoveryCandidates(acc)
	for _, baseURL := range candidates {
		addressbooks, err := discoverCardDAVWithTimeout(func() ([]carddav.AddressbookInfo, error) {
			if useOAuth {
				return a.DiscoverCardDAVAddressbooksOAuth(baseURL, acc.ID)
			}
			return carddav.DiscoverAddressbooks(baseURL, acc.Username, password)
		}, cardDAVDiscoveryTimeout)
		if err != nil {
			log.Debug().Err(err).Str("url", baseURL).Str("accountID", acc.ID).Msg("CardDAV discovery failed for candidate")
			continue
		}
		if len(addressbooks) == 0 {
			continue
		}

		// Verify the discovered address book answers at the stored base
		// URL before creating the source. Discovery follows redirects,
		// so a cross-host .well-known redirect would otherwise leave a
		// source pointing at the wrong host (e.g. the mail domain when
		// the books live on a www host or another port).
		if err := a.verifyCardDAVAddressbook(acc, password, useOAuth, baseURL, addressbooks[0].Path); err != nil {
			log.Debug().Err(err).Str("url", baseURL).Str("accountID", acc.ID).Msg("CardDAV verification failed for candidate")
			continue
		}

		paths := make([]string, 0, len(addressbooks))
		names := make(map[string]string, len(addressbooks))
		// Per-book writability (R22): keep only what the probe
		// actually established. A book the server showed read-only
		// is stored read-only; an inconclusive probe is left out
		// of the map and stores as writable (optimistic — the save
		// path still falls through on a 403). The source is marked
		// writable when at least one book may take writes; when
		// every book is known read-only there is nowhere to write
		// to, so the source is not writable either. Previously every
		// discovered source was marked writable without checking.
		writableByPath := make(map[string]bool, len(addressbooks))
		anyWritable := false
		for _, ab := range addressbooks {
			if ab.WritableKnown {
				writableByPath[ab.Path] = ab.Writable
			}
			if ab.Writable {
				anyWritable = true
			}
		}
		for _, ab := range addressbooks {
			paths = append(paths, ab.Path)
			if ab.Name != "" {
				names[ab.Path] = ab.Name
			}
		}
		source, err := a.AddContactSource(carddav.SourceConfig{
			Name:                fmt.Sprintf("CardDAV (%s)", acc.Email),
			Type:                carddav.SourceTypeCardDAV,
			URL:                 baseURL,
			Username:            acc.Username,
			Password:            password,
			AccountID:           acc.ID,
			Enabled:             true,
			Writable:            anyWritable,
			SyncInterval:        60,
			EnabledAddressbooks: paths,
			AddressbookNames:    names,
			AddressbookWritable: writableByPath,
		})
		if err != nil {
			log.Debug().Err(err).Str("url", baseURL).Str("accountID", acc.ID).Msg("Failed to create auto-discovered CardDAV source")
			return
		}
		log.Info().Str("sourceID", source.ID).Str("accountID", acc.ID).Str("url", baseURL).Int("addressbooks", len(paths)).Msg("CardDAV source auto-discovered for new account")
		return
	}

	log.Debug().Str("accountID", acc.ID).Str("email", acc.Email).Msg("No CardDAV server found for new account")
}

// discoverCardDAVWithTimeout runs a discovery attempt bounded by timeout.
// The attempt may linger in its own goroutine after a timeout (its HTTP
// client has its own 30 s per-request cap), but the sweep moves on.
func discoverCardDAVWithTimeout(fn func() ([]carddav.AddressbookInfo, error), timeout time.Duration) ([]carddav.AddressbookInfo, error) {
	type result struct {
		abs []carddav.AddressbookInfo
		err error
	}
	ch := make(chan result, 1)
	go func() {
		abs, err := fn()
		ch <- result{abs, err}
	}()
	select {
	case r := <-ch:
		return r.abs, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("discovery timed out after %s", timeout)
	}
}

// verifyCardDAVAddressbook issues a single Depth-0 PROPFIND against the
// first discovered address book at the candidate base URL, using the same
// auth as discovery. A 207 means the stored base URL really serves the
// book; anything else means discovery's redirect chain ended somewhere
// the stored URL does not cover, and the candidate must not be kept.
func (a *App) verifyCardDAVAddressbook(acc *account.Account, password string, useOAuth bool, baseURL, addressbookPath string) error {
	var httpClient *http.Client
	if useOAuth {
		tokens, err := a.getValidOAuthToken(acc.ID)
		if err != nil {
			return fmt.Errorf("no valid OAuth token: %w", err)
		}
		httpClient = davutil.NewBearerHTTPClient(tokens.AccessToken, 10*time.Second)
	} else {
		httpClient = davutil.NewBasicDigestHTTPClient(acc.Username, password, 10*time.Second)
	}

	target := addressbookPath
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = strings.TrimSuffix(baseURL, "/") + "/" + strings.TrimPrefix(target, "/")
	}
	body := `<?xml version="1.0" encoding="utf-8"?><propfind xmlns="DAV:"><prop><displayname/></prop></propfind>`
	req, err := http.NewRequest("PROPFIND", target, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 207 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}
