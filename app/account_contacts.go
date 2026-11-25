package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/absonsnet/daak/internal/account"
	"github.com/absonsnet/daak/internal/carddav"
	"github.com/absonsnet/daak/internal/contact"
	"github.com/absonsnet/daak/internal/logging"
	"github.com/absonsnet/daak/internal/smtp"
)

// Scopes required to write a contact into the provider's own address book.
// The read scopes stay granted alongside these; the write scope is what the
// auto-add checks for before calling the provider.
const (
	googleContactsWriteScope = "https://www.googleapis.com/auth/contacts"
	msContactsWriteScope     = "https://graph.microsoft.com/Contacts.ReadWrite"
)

// sendRecipient is one deduplicated recipient of a sent message.
type sendRecipient struct {
	email string
	name  string
}

// collectSendRecipients returns the message's recipients — To, Cc and Bcc,
// in that order — deduplicated by lowercased email (first occurrence wins,
// so the To name wins over a Cc repeat).
func collectSendRecipients(msg smtp.ComposeMessage) []sendRecipient {
	seen := make(map[string]bool)
	var out []sendRecipient
	add := func(address, name string) {
		email := strings.ToLower(strings.TrimSpace(address))
		if email == "" || seen[email] {
			return
		}
		seen[email] = true
		out = append(out, sendRecipient{email: email, name: strings.TrimSpace(name)})
	}
	for _, to := range msg.To {
		add(to.Address, to.Name)
	}
	for _, cc := range msg.Cc {
		add(cc.Address, cc.Name)
	}
	for _, bcc := range msg.Bcc {
		add(bcc.Address, bcc.Name)
	}
	return out
}

func hasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}

// saveRecipientsToAccountContacts saves each new send recipient to the
// sending account's own contacts (Google Contacts, Microsoft contacts, or
// the account's CardDAV address book) — in addition to the local collected
// contact, which the caller already added.
//
// Best-effort and silent: every failure path logs at Debug and returns
// normally. It runs on a worker goroutine after the send already succeeded,
// so it must never delay or fail the send. Only recipients NOT already in
// that account's contacts are created.
func (ops *composeOps) saveRecipientsToAccountContacts(ctx context.Context, accountID string, msg smtp.ComposeMessage) {
	log := logging.WithComponent("app.account-contacts")

	if ops.settingsStore == nil {
		return
	}
	enabled, err := ops.settingsStore.GetSaveRecipientsToAccountContacts()
	if err != nil {
		log.Debug().Err(err).Msg("Failed to read save-recipients setting, skipping provider save")
		return
	}
	if !enabled {
		return
	}

	acc, err := ops.accountStore.Get(accountID)
	if err != nil || acc == nil {
		log.Debug().Err(err).Str("accountID", accountID).Msg("Account not found, skipping provider save")
		return
	}

	recipients := collectSendRecipients(msg)
	if len(recipients) == 0 {
		return
	}

	switch ops.contactsProviderFor(acc) {
	case "google":
		ops.saveRecipientsToGoogleContacts(ctx, accountID, recipients)
	case "microsoft":
		ops.saveRecipientsToMicrosoftContacts(ctx, acc, recipients)
	case "carddav":
		ops.saveRecipientsToCardDAVContacts(ctx, acc, recipients)
	default:
		log.Debug().Str("accountID", accountID).Msg("No contacts provider for account, local collected only")
	}
}

// contactsProviderFor decides which provider address book a sent recipient
// belongs in for this account. Google/Microsoft OAuth accounts use their
// provider APIs; everything else (password IMAP, custom OAuth) uses the
// account's linked CardDAV source. A nil/empty result means local collected
// only.
func (ops *composeOps) contactsProviderFor(acc *account.Account) string {
	if acc.AuthType != account.AuthOAuth2 {
		// Password accounts have no OAuth token, so neither the People API
		// nor Graph is reachable — CardDAV only (a manual Gmail IMAP
		// account has no CardDAV server, so this falls through to
		// local-only in practice).
		return "carddav"
	}

	provider := ""
	if ops.credStore != nil {
		if p, err := ops.credStore.GetOAuthProvider(acc.ID); err == nil {
			provider = p
		}
	}
	switch provider {
	case "google":
		return "google"
	case "microsoft":
		return "microsoft"
	}

	// Fall back to host matching for OAuth accounts whose provider row is
	// missing (e.g. upgraded installs).
	if strings.Contains(acc.IMAPHost, "gmail") {
		return "google"
	}
	if strings.Contains(acc.IMAPHost, "outlook.office365") {
		return "microsoft"
	}
	// Other OAuth accounts (custom OIDC): CardDAV via the account token.
	return "carddav"
}

// saveRecipientsToGoogleContacts saves new recipients to Google Contacts
// using the sending account's own OAuth grant. The grant must include the
// contacts write scope — accounts that have not re-authorized since the
// scope was added are skipped silently (local collected still happened).
func (ops *composeOps) saveRecipientsToGoogleContacts(ctx context.Context, accountID string, recipients []sendRecipient) {
	log := logging.WithComponent("app.account-contacts")
	if ops.googleContactsClient == nil {
		return
	}

	tokens, err := ops.getValidOAuthToken(ctx, accountID)
	if err != nil {
		log.Debug().Err(err).Str("accountID", accountID).Msg("No valid Google token, skipping provider save")
		return
	}
	if !hasScope(tokens.Scopes, googleContactsWriteScope) {
		log.Debug().Str("accountID", accountID).Msg("Google account lacks contacts write scope (re-authorization required), skipping provider save")
		return
	}

	for _, r := range recipients {
		exists, err := ops.googleContactsClient.ContactExists(tokens.AccessToken, r.email)
		if err != nil {
			log.Debug().Err(err).Str("email", r.email).Msg("Google contacts existence check failed, skipping recipient")
			continue
		}
		if exists {
			continue
		}
		if err := ops.googleContactsClient.CreateContact(tokens.AccessToken, r.name, r.email); err != nil {
			log.Debug().Err(err).Str("email", r.email).Msg("Failed to save recipient to Google Contacts")
			continue
		}
		log.Info().Str("email", r.email).Str("accountID", accountID).Msg("Saved new recipient to Google Contacts")
	}
}

// saveRecipientsToMicrosoftContacts saves new recipients to Microsoft
// contacts using the standalone Microsoft contacts source linked to this
// mail account. The mail account's own token cannot be used — its Outlook
// grant has no Graph audience — so the source's own Graph grant (with
// Contacts.ReadWrite) is used instead. Without a matching source, or
// without the write scope on it, this silently falls back to local only.
func (ops *composeOps) saveRecipientsToMicrosoftContacts(ctx context.Context, acc *account.Account, recipients []sendRecipient) {
	log := logging.WithComponent("app.account-contacts")
	if ops.microsoftContactsSyncer == nil || ops.carddavStore == nil || ops.getContactSourceToken == nil {
		return
	}

	sourceID, ok := ops.microsoftContactsSourceForAccount(ctx, acc)
	if !ok {
		log.Debug().Str("accountID", acc.ID).Msg("No Microsoft contacts source for this account (add one in Settings → Contacts), skipping provider save")
		return
	}

	stored, err := ops.credStore.GetContactSourceOAuthTokens(sourceID)
	if err != nil {
		log.Debug().Err(err).Str("sourceID", sourceID).Msg("No stored tokens for Microsoft contacts source, skipping provider save")
		return
	}
	if !hasScope(stored.Scopes, msContactsWriteScope) {
		log.Debug().Str("sourceID", sourceID).Msg("Microsoft contacts source lacks Contacts.ReadWrite scope (re-authorization required), skipping provider save")
		return
	}
	accessToken, err := ops.getContactSourceToken(sourceID)
	if err != nil {
		log.Debug().Err(err).Str("sourceID", sourceID).Msg("No valid token for Microsoft contacts source, skipping provider save")
		return
	}

	for _, r := range recipients {
		exists, err := ops.microsoftContactsSyncer.ContactExists(accessToken, r.email)
		if err != nil {
			log.Debug().Err(err).Str("email", r.email).Msg("Microsoft contacts existence check failed, skipping recipient")
			continue
		}
		if exists {
			continue
		}
		if err := ops.microsoftContactsSyncer.CreateContact(accessToken, r.name, r.email); err != nil {
			log.Debug().Err(err).Str("email", r.email).Msg("Failed to save recipient to Microsoft contacts")
			continue
		}
		log.Info().Str("email", r.email).Str("accountID", acc.ID).Msg("Saved new recipient to Microsoft contacts")
	}
}

// microsoftContactsSourceForAccount finds the standalone Microsoft contacts
// source belonging to this mail account. Source rows store no account email,
// so the match is made by asking Graph who the source's token belongs to
// (/me) and comparing with the account's email. The match is cached per
// account (verified against the store on each hit) so later sends do not
// repeat the /me call.
func (ops *composeOps) microsoftContactsSourceForAccount(ctx context.Context, acc *account.Account) (string, bool) {
	log := logging.WithComponent("app.account-contacts")

	ops.msContactsSourceCacheMu.Lock()
	if ops.msContactsSourceCache != nil {
		if id, ok := ops.msContactsSourceCache[acc.ID]; ok {
			ops.msContactsSourceCacheMu.Unlock()
			if src, err := ops.carddavStore.GetSource(id); err == nil && src != nil && src.Enabled && src.Type == carddav.SourceTypeMicrosoft {
				return id, true
			}
			// Stale cache entry — fall through and re-resolve.
		} else {
			ops.msContactsSourceCacheMu.Unlock()
		}
	} else {
		ops.msContactsSourceCacheMu.Unlock()
	}

	sources, err := ops.carddavStore.ListSources()
	if err != nil {
		log.Debug().Err(err).Msg("Failed to list contact sources")
		return "", false
	}
	for _, src := range sources {
		if src == nil || src.Type != carddav.SourceTypeMicrosoft || !src.Enabled {
			continue
		}
		token, err := ops.getContactSourceToken(src.ID)
		if err != nil {
			continue
		}
		email, err := ops.microsoftContactsSyncer.GetMyEmail(token)
		if err != nil {
			log.Debug().Err(err).Str("sourceID", src.ID).Msg("Could not resolve owner of Microsoft contacts source")
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(email), strings.TrimSpace(acc.Email)) {
			continue
		}
		ops.msContactsSourceCacheMu.Lock()
		if ops.msContactsSourceCache == nil {
			ops.msContactsSourceCache = make(map[string]string)
		}
		ops.msContactsSourceCache[acc.ID] = src.ID
		ops.msContactsSourceCacheMu.Unlock()
		return src.ID, true
	}
	return "", false
}

// saveRecipientsToCardDAVContacts saves new recipients as vCards in
// the CardDAV source linked to this account, trying each enabled
// address book of THAT source in turn until one accepts the write
// (R22). Before creating, every enabled address book of THAT source
// is checked: a recipient already in the second (or any other) book
// of the same source must not be duplicated into the first. The
// check never looks at another account's source — a contact there
// does not suppress creating it here.
//
// Writability is per address book, not per source: books the server
// has shown read-only (no write privilege at discovery, or a 403 on
// an earlier PUT — recorded on the book row) are skipped, and a book
// that refuses a write mid-send is marked read-only and the next
// writable book tried. Only when no book accepts the recipient is
// the failure surfaced: the source's last-error is recorded (so
// Settings → Contacts shows it) and the onCardDAVAutoAddFailure
// hook fires (production: user-visible warning), instead of the
// failure being only a log line while the local copy lands and
// nothing looks wrong. The source-level Writable flag remains the
// user's opt-out: when it is off, no write is attempted and nothing
// is reported — that is a deliberate setting, not a failure.
func (ops *composeOps) saveRecipientsToCardDAVContacts(ctx context.Context, acc *account.Account, recipients []sendRecipient) {
	log := logging.WithComponent("app.account-contacts")
	if ops.carddavStore == nil || ops.carddavSyncer == nil {
		return
	}

	sources, err := ops.carddavStore.ListSources()
	if err != nil {
		log.Debug().Err(err).Msg("Failed to list contact sources")
		return
	}
	var source *carddav.Source
	for _, s := range sources {
		if s == nil || s.Type != carddav.SourceTypeCardDAV || !s.Enabled {
			continue
		}
		if s.AccountID != nil && *s.AccountID == acc.ID {
			source = s
			break
		}
	}
	if source == nil {
		log.Debug().Str("accountID", acc.ID).Msg("No CardDAV source linked to this account, skipping provider save")
		return
	}

	// reportFailure records a persistent failure where the user can
	// see it (source last-error + the warning hook) instead of only
	// logging it. Called once per send batch.
	reportFailure := func(emails []string, err error) {
		msg := fmt.Sprintf("Could not save %d new contact(s) to %s: %v", len(emails), source.Name, err)
		log.Warn().Str("sourceID", source.ID).Strs("emails", emails).Err(err).Msg("CardDAV auto-add failed for every address book")
		if uerr := ops.carddavStore.UpdateSourceSyncStatus(source.ID, msg); uerr != nil {
			log.Debug().Err(uerr).Msg("Failed to record CardDAV auto-add failure on source")
		}
		if ops.onCardDAVAutoAddFailure != nil {
			ops.onCardDAVAutoAddFailure(source.ID, acc.ID, emails, err)
		}
	}

	if !source.Writable {
		// The user's opt-out for this source: no write is attempted
		// and nothing is reported — unlike a book that proves
		// read-only, this is a deliberate setting, not a failure.
		log.Debug().Str("sourceID", source.ID).Msg("CardDAV source is not writable, skipping provider save")
		return
	}

	addressbooks, err := ops.carddavStore.ListEnabledAddressbooks(source.ID)
	if err != nil || len(addressbooks) == 0 {
		log.Debug().Err(err).Str("sourceID", source.ID).Msg("No enabled address book for CardDAV source")
		reportFailure(collectEmails(recipients), fmt.Errorf("no enabled address book for source %q", source.Name))
		return
	}

	// Candidate books, in stored order: only books not already known
	// read-only. A book that refuses a write below is skipped for
	// the rest of the batch (see provenReadOnly).
	candidates := make([]*carddav.Addressbook, 0, len(addressbooks))
	for _, ab := range addressbooks {
		if ab != nil && ab.Writable {
			candidates = append(candidates, ab)
		}
	}
	if len(candidates) == 0 {
		names := make([]string, 0, len(addressbooks))
		for _, ab := range addressbooks {
			if ab != nil {
				names = append(names, ab.Name)
			}
		}
		reportFailure(collectEmails(recipients), fmt.Errorf("all address books of source %q are read-only (%s)", source.Name, strings.Join(names, ", ")))
		return
	}

	client, err := ops.carddavSyncer.ClientForSource(source)
	if err != nil {
		log.Debug().Err(err).Str("sourceID", source.ID).Msg("No CardDAV client for source, skipping provider save")
		reportFailure(collectEmails(recipients), err)
		return
	}

	var failed []string
	var lastErr error
	// provenReadOnly collects books that refused a write during
	// this batch, so later recipients skip them without re-trying.
	provenReadOnly := make(map[string]bool)
	for _, r := range recipients {
		exists, err := ops.carddavContactExistsInSource(r.email, addressbooks)
		if err != nil {
			log.Debug().Err(err).Str("email", r.email).Msg("CardDAV existence check failed, skipping recipient")
			continue
		}
		if exists {
			continue
		}
		given, family := contact.SplitDisplayName(r.name)
		rec := &contact.Record{
			Fn:      r.name,
			NGiven:  given,
			NFamily: family,
			Emails:  []contact.RecordEmail{{Email: r.email, IsPrimary: true}},
		}
		saved := false
		for _, ab := range candidates {
			if provenReadOnly[ab.ID] {
				continue
			}
			// Fresh record id per attempt: a PUT that the server
			// accepted but we failed to mirror locally must not be
			// retried under the same href into another book.
			attempt := *rec
			attempt.ID = ""
			if _, err := ops.carddavStore.CreateRecord(ab.ID, &attempt, client); err != nil {
				lastErr = err
				if carddav.IsWriteForbidden(err) {
					// The server has positively shown this
					// book read-only: remember it, skip it
					// for the rest of the batch, try the next.
					log.Warn().Err(err).Str("email", r.email).Str("addressbook", ab.Name).Msg("CardDAV address book refused the write, trying the next writable book")
					provenReadOnly[ab.ID] = true
					if serr := ops.carddavStore.SetAddressbookWritable(ab.ID, false); serr != nil {
						log.Debug().Err(serr).Str("addressbookID", ab.ID).Msg("Failed to record address book as read-only")
					}
				} else {
					log.Debug().Err(err).Str("email", r.email).Str("addressbook", ab.Name).Msg("Failed to save recipient to CardDAV address book, trying the next book")
				}
				continue
			}
			log.Info().Str("email", r.email).Str("accountID", acc.ID).Str("addressbook", ab.Name).Msg("Saved new recipient to CardDAV address book")
			saved = true
			break
		}
		if !saved {
			failed = append(failed, r.email)
		}
	}
	if len(failed) > 0 {
		reportFailure(failed, lastErr)
	}
}

// collectEmails returns the recipients' emails in order.
func collectEmails(recipients []sendRecipient) []string {
	emails := make([]string, 0, len(recipients))
	for _, r := range recipients {
		emails = append(emails, r.email)
	}
	return emails
}

// carddavContactExistsInSource reports whether any enabled address book
// of the sending account's own CardDAV source already holds this email.
// The check is scoped to the given books (all from one source) —
// deliberately not the store-wide lookup, which would also see other
// accounts' sources and wrongly suppress creating the contact here.
func (ops *composeOps) carddavContactExistsInSource(email string, addressbooks []*carddav.Addressbook) (bool, error) {
	for _, ab := range addressbooks {
		if ab == nil {
			continue
		}
		existing, err := ops.carddavStore.GetContactByEmailInAddressbook(email, ab.ID)
		if err != nil {
			return false, err
		}
		if existing != nil {
			return true, nil
		}
	}
	return false, nil
}
