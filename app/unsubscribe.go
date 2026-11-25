package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/absonsnet/daak/internal/account"
	"github.com/absonsnet/daak/internal/folder"
	"github.com/absonsnet/daak/internal/logging"
	"github.com/absonsnet/daak/internal/message"
	"github.com/absonsnet/daak/internal/smtp"
	"github.com/absonsnet/daak/internal/unsubscribe"
)

// ============================================================================
// Unsubscribe API - Exposed to frontend via Wails bindings (Phase C, Gmail
// behaviour: the List-Unsubscribe headers on the message decide
// everything; there is no footer-link fallback).
//
// App and ViewerApp (detached popup) both expose these methods; the
// shared work lives on unsubscribeDeps so the two windows cannot drift.
// ============================================================================

// UnsubscribeStatus is one message's unsubscribe state, as returned to
// the frontend.
type UnsubscribeStatus struct {
	// MessageID is the internal id of the message this status is for.
	MessageID string `json:"messageId"`
	// Available is true when the message carries List-Unsubscribe
	// headers and sits in Inbox or Spam (never Sent/Drafts).
	Available bool `json:"available"`
	// AlreadyUnsubscribed is true when a completed unsubscribe (one-click
	// POST sent, or unsubscribe email sent) is already recorded for
	// this sender on this account.
	AlreadyUnsubscribed bool `json:"alreadyUnsubscribed"`
	// Method is the action the headers offer: one_click, browser,
	// mailto, or none.
	Method string `json:"method"`
	// Domain is the host the action goes to, shown in the button
	// tooltip before the click.
	Domain string `json:"domain"`
	// DomainMismatch is true when that host is neither the sender
	// domain nor a known email-service provider.
	DomainMismatch bool   `json:"domainMismatch"`
	IsSpam         bool   `json:"isSpam"`
	SenderEmail    string `json:"senderEmail"`
	SenderName     string `json:"senderName"`
}

// UnsubscribeResult is what an unsubscribe click returns to the
// frontend. For the browser method the frontend opens URL itself (via
// OpenURL) — the unsubscribe is not complete in Daak, and the result
// says so.
type UnsubscribeResult struct {
	// Method is one of one_click, browser, mailto, already.
	Method string `json:"method"`
	// URL is the sender page to open (browser method only).
	URL            string `json:"url"`
	Domain         string `json:"domain"`
	DomainMismatch bool   `json:"domainMismatch"`
	IsSpam         bool   `json:"isSpam"`
	SenderEmail    string `json:"senderEmail"`
	SenderName     string `json:"senderName"`
}

// SubscriptionSender is one row of Settings → Subscriptions.
type SubscriptionSender struct {
	SenderEmail       string `json:"senderEmail"`
	SenderName        string `json:"senderName"`
	AccountID         string `json:"accountId"`
	CountAll          int    `json:"countAll"`
	Count30Days       int    `json:"count30Days"`
	LastReceived      string `json:"lastReceived"`
	OneClick          bool   `json:"oneClick"`
	Unsubscribed      bool   `json:"unsubscribed"`
	UnsubscribeMethod string `json:"unsubscribeMethod"`
	DomainMismatch    bool   `json:"domainMismatch"`
	AutoDelete        bool   `json:"autoDelete"`
}

// unsubscribeDeps is everything the unsubscribe flow needs from its
// host window. App and ViewerApp both carry these stores, so one
// implementation serves both.
type unsubscribeDeps struct {
	messageStore *message.Store
	accountStore *account.Store
	folderStore  *folder.Store
	composeOps   *composeOps
	// trash moves messages to the account's Trash folder (App.Trash /
	// ViewerApp.Trash).
	trash func(messageIDs []string) (bool, error)
	// httpClient performs the one-click POST; nil uses the package
	// default (15s timeout). Tests inject a client pointed at a local
	// TLS server.
	httpClient *http.Client
	// recordSentSaveFailure records the persistent "sent but not
	// saved to Sent" warning when the unsubscribe email was delivered
	// but its Sent copy could not be saved (fourth-audit finding
	// 46). Nil means log only (deps built by hand in tests).
	recordSentSaveFailure func(accountID, subject, recipient, fromAddress string, saveErr error)
}

// statusForMessage derives one message's unsubscribe status. Folder
// eligibility is Inbox or Spam only. A folder that cannot be resolved
// (empty FolderID, lookup error, deleted folder) defaults to unavailable
// and returns early — it must never fall through to the header check,
// which would offer unsubscribe on Sent/Drafts mail whose folder row
// is gone.
func (d *unsubscribeDeps) statusForMessage(msg *message.Message) *UnsubscribeStatus {
	st := &UnsubscribeStatus{
		MessageID:   msg.ID,
		SenderEmail: msg.FromEmail,
		SenderName:  msg.FromName,
	}
	if msg.FolderID == "" {
		return st
	}
	f, err := d.folderStore.Get(msg.FolderID)
	if err != nil || f == nil {
		return st
	}
	st.IsSpam = f.Type == folder.TypeSpam
	if f.Type != folder.TypeInbox && f.Type != folder.TypeSpam {
		return st
	}
	if strings.TrimSpace(msg.ListUnsubscribe) == "" {
		return st
	}
	target := unsubscribe.Resolve(msg.ListUnsubscribe, msg.ListUnsubscribePost, msg.FromEmail)
	st.Available = target.Method != unsubscribe.MethodNone
	st.Method = string(target.Method)
	st.Domain = target.Domain
	st.DomainMismatch = target.DomainMismatch
	if st.Available {
		if done, err := d.messageStore.HasUnsubscribed(msg.FromEmail, msg.AccountID); err == nil {
			st.AlreadyUnsubscribed = done
		}
	}
	return st
}

// GetUnsubscribeStatuses returns the unsubscribe status for each
// message id (missing ids are skipped, never an error). A slice, not a
// map: Wails does not generate a model class for map-value structs.
func (d *unsubscribeDeps) getStatuses(messageIDs []string) []*UnsubscribeStatus {
	out := make([]*UnsubscribeStatus, 0, len(messageIDs))
	for _, id := range messageIDs {
		msg, err := d.messageStore.Get(id)
		if err != nil || msg == nil {
			continue
		}
		out = append(out, d.statusForMessage(msg))
	}
	return out
}

// unsubscribeFromMessage carries out the unsubscribe for one message
// (internal id). The click itself is the consent — nothing fires on
// open or sync, only here.
func (d *unsubscribeDeps) unsubscribeFromMessage(ctx context.Context, messageID string) (*UnsubscribeResult, error) {
	log := logging.WithComponent("app.unsubscribe")

	msg, err := d.messageStore.Get(messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return nil, fmt.Errorf("message not found: %s", messageID)
	}
	if strings.TrimSpace(msg.FromEmail) == "" {
		return nil, fmt.Errorf("message has no sender to unsubscribe from")
	}
	st := d.statusForMessage(msg)
	if !st.Available {
		return nil, fmt.Errorf("no unsubscribe information on this message")
	}
	if st.AlreadyUnsubscribed {
		return &UnsubscribeResult{
			Method: "already", Domain: st.Domain, DomainMismatch: st.DomainMismatch,
			IsSpam: st.IsSpam, SenderEmail: msg.FromEmail, SenderName: msg.FromName,
		}, nil
	}

	target := unsubscribe.Resolve(msg.ListUnsubscribe, msg.ListUnsubscribePost, msg.FromEmail)
	res := &UnsubscribeResult{
		Domain: target.Domain, DomainMismatch: target.DomainMismatch,
		IsSpam: st.IsSpam, SenderEmail: msg.FromEmail, SenderName: msg.FromName,
	}
	switch target.Method {
	case unsubscribe.MethodOneClick:
		if err := unsubscribe.PostOneClick(ctx, target.URL, d.httpClient); err != nil {
			return nil, fmt.Errorf("unsubscribe request failed: %w", err)
		}
		if err := d.messageStore.RecordUnsubscribeAction(msg.FromEmail, msg.AccountID, "one_click"); err != nil {
			return nil, fmt.Errorf("unsubscribed but failed to record: %w", err)
		}
		res.Method = "one_click"
	case unsubscribe.MethodBrowser:
		// The sender's page completes it; record the click for the
		// Subscriptions view, but this is NOT a completed unsubscribe
		// — the button keeps being offered.
		if err := d.messageStore.RecordUnsubscribeAction(msg.FromEmail, msg.AccountID, "browser"); err != nil {
			return nil, fmt.Errorf("failed to record unsubscribe action: %w", err)
		}
		res.Method = "browser"
		res.URL = target.URL
	case unsubscribe.MethodMailto:
		if err := d.sendUnsubscribeEmail(ctx, msg, target); err != nil {
			return nil, err
		}
		if err := d.messageStore.RecordUnsubscribeAction(msg.FromEmail, msg.AccountID, "mailto"); err != nil {
			return nil, fmt.Errorf("unsubscribe email sent but failed to record: %w", err)
		}
		res.Method = "mailto"
	default:
		return nil, fmt.Errorf("no unsubscribe information on this message")
	}

	log.Info().Str("messageID", messageID).Str("method", res.Method).
		Str("from", msg.FromEmail).Msg("Unsubscribed")
	return res, nil
}

// sendUnsubscribeEmail builds and sends the unsubscribe email for a
// mailto target, from the identity the original was addressed to (else
// the default identity), and files it in Sent like a normal send.
func (d *unsubscribeDeps) sendUnsubscribeEmail(ctx context.Context, msg *message.Message, target unsubscribe.Target) error {
	log := logging.WithComponent("app.unsubscribe")

	from := smtp.Address{}
	identities, _ := d.accountStore.GetIdentities(msg.AccountID)
	if ident := selectReplyFromIdentity(identities, msg); ident != nil {
		from = smtp.Address{Name: ident.Name, Address: ident.Email}
	} else if acc, accErr := d.accountStore.Get(msg.AccountID); accErr == nil && acc != nil {
		from = smtp.Address{Name: acc.Name, Address: acc.Email}
	}
	if from.Address == "" {
		return fmt.Errorf("no sender identity for account %s", msg.AccountID)
	}

	raw, _, err := unsubscribe.BuildMailtoMessage(from.Name, from.Address, target.MailtoAddress, target.MailtoSubject)
	if err != nil {
		return fmt.Errorf("failed to build unsubscribe email: %w", err)
	}
	if err := d.composeOps.sendRawMessage(ctx, msg.AccountID, from.Address, []string{target.MailtoAddress}, raw); err != nil {
		return fmt.Errorf("failed to send unsubscribe email: %w", err)
	}
	if acc, accErr := d.accountStore.Get(msg.AccountID); accErr == nil && acc != nil && !providerAutoSavesSentMail(acc.IMAPHost) {
		if err := d.composeOps.saveToSentFolder(ctx, msg.AccountID, acc, raw); err != nil {
			// The mail was delivered — a bare log line used to be
			// the only trace of the missing Sent copy
			// (fourth-audit finding 46). Record the same
			// persistent "sent but not saved" warning normal
			// sends get, so the user finds it in the Outbox.
			if d.recordSentSaveFailure != nil {
				d.recordSentSaveFailure(msg.AccountID, target.MailtoSubject, target.MailtoAddress, from.Address, err)
			} else {
				log.Warn().Err(err).Str("accountID", msg.AccountID).Msg("Failed to save unsubscribe email to Sent folder")
			}
		}
	}
	return nil
}

// followUp applies the optional after-unsubscribe extras for one
// message: move that sender's existing inbox mail to Trash, and/or
// create the auto-delete rule for future mail. Each is created only
// when asked for; both default off.
func (d *unsubscribeDeps) followUp(messageID string, moveExisting, autoDelete bool) error {
	msg, err := d.messageStore.Get(messageID)
	if err != nil {
		return fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return fmt.Errorf("message not found: %s", messageID)
	}
	if moveExisting {
		inbox, err := d.folderStore.GetByType(msg.AccountID, folder.TypeInbox)
		if err != nil || inbox == nil {
			return fmt.Errorf("inbox folder not found for account %s", msg.AccountID)
		}
		ids, err := d.messageStore.ListMessageIDsFromSenderInFolder(msg.AccountID, inbox.ID, msg.FromEmail)
		if err != nil {
			return fmt.Errorf("failed to list sender mail: %w", err)
		}
		if len(ids) > 0 {
			if _, err := d.trash(ids); err != nil {
				return fmt.Errorf("failed to move sender mail to Trash: %w", err)
			}
		}
	}
	if autoDelete {
		if err := d.messageStore.SetSenderRule(msg.FromEmail, msg.AccountID, "auto_trash"); err != nil {
			return fmt.Errorf("failed to set auto-delete rule: %w", err)
		}
	}
	return nil
}

// listSubscriptions returns every sender who ever sent
// List-Unsubscribe headers, most messages first, enriched with the
// recorded unsubscribe state, the domain-mismatch flag, and the
// auto-delete rule.
func (d *unsubscribeDeps) listSubscriptions() ([]SubscriptionSender, error) {
	rows, err := d.messageStore.ListSubscriptionSenders()
	if err != nil {
		return nil, err
	}
	out := make([]SubscriptionSender, 0, len(rows))
	for _, row := range rows {
		s := SubscriptionSender{
			SenderEmail: row.SenderEmail,
			SenderName:  row.SenderName,
			AccountID:   row.AccountID,
			CountAll:    row.CountAll,
			Count30Days: row.Count30Days,
			OneClick:    row.OneClick,
		}
		if !row.LastReceived.IsZero() {
			s.LastReceived = row.LastReceived.Format("2006-01-02")
		}
		if row.ListUnsubscribe != "" {
			s.DomainMismatch = unsubscribe.Resolve(row.ListUnsubscribe, row.OneClick, row.SenderEmail).DomainMismatch
		}
		if method, ok, err := d.messageStore.UnsubscribeActionMethod(row.SenderEmail, row.AccountID); err == nil && ok {
			s.UnsubscribeMethod = method
			s.Unsubscribed = method == "one_click" || method == "mailto"
		}
		if auto, err := d.messageStore.HasSenderRule(row.SenderEmail, row.AccountID, "auto_trash"); err == nil {
			s.AutoDelete = auto
		}
		out = append(out, s)
	}
	return out, nil
}

// unsubscribeFromSubscription unsubscribes from a sender listed in the
// Subscriptions view, using that sender's newest message carrying
// List-Unsubscribe headers.
func (d *unsubscribeDeps) unsubscribeFromSubscription(ctx context.Context, senderEmail, accountID string) (*UnsubscribeResult, error) {
	msg, err := d.messageStore.GetLatestMessageFromSender(accountID, senderEmail)
	if err != nil {
		return nil, fmt.Errorf("failed to find sender mail: %w", err)
	}
	if msg == nil {
		return nil, fmt.Errorf("no mail from %s with unsubscribe information", senderEmail)
	}
	return d.unsubscribeFromMessage(ctx, msg.ID)
}

// setSenderAutoDelete creates or removes the auto-delete rule for a
// sender (future mail from the sender goes to Trash at sync time).
func (d *unsubscribeDeps) setSenderAutoDelete(senderEmail, accountID string, enabled bool) error {
	if enabled {
		return d.messageStore.SetSenderRule(senderEmail, accountID, "auto_trash")
	}
	return d.messageStore.DeleteSenderRule(senderEmail, accountID, "auto_trash")
}

// --- App bindings ----------------------------------------------------------

// GetUnsubscribeStatuses returns the unsubscribe status for each
// message id.
func (a *App) GetUnsubscribeStatuses(messageIDs []string) ([]*UnsubscribeStatus, error) {
	return a.unsubscribeDeps().getStatuses(messageIDs), nil
}

// UnsubscribeFromMessage carries out the unsubscribe for one message
// (internal id). One-click fires the silent POST at once; a plain https
// URL is returned for the frontend to open; a mailto target sends the
// unsubscribe email from this account.
func (a *App) UnsubscribeFromMessage(messageID string) (*UnsubscribeResult, error) {
	return a.unsubscribeDeps().unsubscribeFromMessage(a.ctx, messageID)
}

// UnsubscribeFollowUp applies the optional extras after an unsubscribe:
// move the sender's existing inbox mail to Trash, and/or auto-delete
// future mail from the sender. Each is created only when asked for.
func (a *App) UnsubscribeFollowUp(messageID string, moveExisting bool, autoDelete bool) error {
	return a.unsubscribeDeps().followUp(messageID, moveExisting, autoDelete)
}

// GetSubscriptions returns every sender who ever sent List-Unsubscribe
// headers, most messages first, for Settings → Subscriptions.
func (a *App) GetSubscriptions() ([]SubscriptionSender, error) {
	return a.unsubscribeDeps().listSubscriptions()
}

// UnsubscribeFromSubscription unsubscribes from a sender listed in the
// Subscriptions view.
func (a *App) UnsubscribeFromSubscription(senderEmail string, accountID string) (*UnsubscribeResult, error) {
	return a.unsubscribeDeps().unsubscribeFromSubscription(a.ctx, senderEmail, accountID)
}

// SetSenderAutoDelete creates or removes the auto-delete rule for a
// sender: future mail from the sender goes to Trash at sync time.
func (a *App) SetSenderAutoDelete(senderEmail string, accountID string, enabled bool) error {
	return a.unsubscribeDeps().setSenderAutoDelete(senderEmail, accountID, enabled)
}

// unsubscribeDeps builds the shared unsubscribe service from the App's
// stores. The App's Trash is used for the follow-up move.
func (a *App) unsubscribeDeps() *unsubscribeDeps {
	return &unsubscribeDeps{
		messageStore: a.messageStore,
		accountStore: a.accountStore,
		folderStore:  a.folderStore,
		composeOps:   &a.composeOps,
		trash:        a.Trash,
		recordSentSaveFailure: func(accountID, subject, recipient, fromAddress string, saveErr error) {
			a.ensureSendQueueService().RecordSentSaveFailure(accountID, subject, recipient, fromAddress, saveErr)
		},
	}
}

// applyRules moves newly synced inbox mail from auto-delete senders
// to Trash. It runs after a successful inbox header sync — the minimal
// sync-time application of the sender_rules table (Daak has no general
// filter-rule system yet). A missing Trash folder or no rules is a
// silent no-op; a move failure only warns, never fails the sync.
func (d *unsubscribeDeps) applyRules(accountID, folderID string) {
	log := logging.WithComponent("app.senderRules")

	f, err := d.folderStore.Get(folderID)
	if err != nil || f == nil || f.Type != folder.TypeInbox {
		return
	}
	if trashFolder, err := d.folderStore.GetByType(accountID, folder.TypeTrash); err != nil || trashFolder == nil {
		return
	}
	senders, err := d.messageStore.ListSenderRuleSenders(accountID, "auto_trash")
	if err != nil || len(senders) == 0 {
		return
	}
	for _, sender := range senders {
		ids, err := d.messageStore.ListMessageIDsFromSenderInFolder(accountID, folderID, sender)
		if err != nil {
			log.Warn().Err(err).Str("sender", sender).Msg("Sender rule: failed to list sender mail")
			continue
		}
		if len(ids) == 0 {
			continue
		}
		if _, err := d.trash(ids); err != nil {
			log.Warn().Err(err).Str("sender", sender).Int("count", len(ids)).Msg("Sender rule: failed to move mail to Trash")
			continue
		}
		log.Info().Str("sender", sender).Int("count", len(ids)).Msg("Sender rule: moved mail to Trash")
	}
}

// applySenderRules moves newly synced inbox mail from auto-delete
// senders to Trash (see applyRules). It runs after a successful inbox
// header sync and never fails the sync itself.
func (a *App) applySenderRules(accountID, folderID string) {
	a.unsubscribeDeps().applyRules(accountID, folderID)
}
