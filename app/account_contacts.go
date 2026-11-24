package app

import (
	"context"
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

// saveRecipientsToCardDAVContacts saves new recipients as vCards in the
// first enabled address book of the CardDAV source linked to this account.
// Before creating, every enabled address book of THAT source is checked:
// a recipient already in the second (or any other) book of the same source
// must not be duplicated into the first. The check never looks at another
// account's source — a contact there does not suppress creating it here.
// Only writable sources are written to; without a linked writable source
// this silently falls back to local collected only.
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
	if !source.Writable {
		log.Debug().Str("sourceID", source.ID).Msg("CardDAV source is not writable, skipping provider save")
		return
	}

	addressbooks, err := ops.carddavStore.ListEnabledAddressbooks(source.ID)
	if err != nil || len(addressbooks) == 0 {
		log.Debug().Err(err).Str("sourceID", source.ID).Msg("No enabled address book for CardDAV source")
		return
	}
	ab := addressbooks[0]

	client, err := ops.carddavSyncer.ClientForSource(source)
	if err != nil {
		log.Debug().Err(err).Str("sourceID", source.ID).Msg("No CardDAV client for source, skipping provider save")
		return
	}

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
		if _, err := ops.carddavStore.CreateRecord(ab.ID, rec, client); err != nil {
			log.Debug().Err(err).Str("email", r.email).Msg("Failed to save recipient to CardDAV address book")
			continue
		}
		log.Info().Str("email", r.email).Str("accountID", acc.ID).Str("addressbook", ab.Name).Msg("Saved new recipient to CardDAV address book")
	}
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
