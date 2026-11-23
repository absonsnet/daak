package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/absonsnet/daak/internal/logging"
	"github.com/absonsnet/daak/internal/reaction"
	"github.com/absonsnet/daak/internal/smtp"
)

// ============================================================================
// Message reactions API - Exposed to frontend via Wails bindings
// ============================================================================

// MessageReaction is one emoji reaction on a message, as returned to the
// frontend. IsOwn marks reactions from any of the current user's own
// addresses on the message's account (account email + identities).
type MessageReaction struct {
	Emoji        string    `json:"emoji"`
	ReactorEmail string    `json:"reactorEmail"`
	ReactorName  string    `json:"reactorName"`
	CreatedAt    time.Time `json:"createdAt"`
	IsOwn        bool      `json:"isOwn"`
}

// GetMessageReactions returns the current reactions on a message (internal
// id), oldest first. Reactions recorded before their original synced are
// linked on arrival, so this only ever returns reactions for a message
// that exists in the DB.
func (a *App) GetMessageReactions(messageID string) ([]MessageReaction, error) {
	msg, err := a.messageStore.Get(messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return nil, fmt.Errorf("message not found: %s", messageID)
	}

	reactions, err := a.reactionStore.ListByMessage(messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to list reactions: %w", err)
	}

	ownEmails := a.ownEmailsForAccount(msg.AccountID)

	result := make([]MessageReaction, 0, len(reactions))
	for _, r := range reactions {
		result = append(result, MessageReaction{
			Emoji:        r.Emoji,
			ReactorEmail: r.ReactorEmail,
			ReactorName:  r.ReactorName,
			CreatedAt:    r.CreatedAt,
			IsOwn:        ownEmails[strings.ToLower(strings.TrimSpace(r.ReactorEmail))],
		})
	}
	return result, nil
}

// RemoveOwnReaction deletes only the current user's own reaction on a
// message (internal id) from the local store. It sends no email: a
// reaction already sent to the original sender cannot be retracted from
// their mailbox — removing it only hides it here. A reaction still
// queued (inside the undo window) is cancelled as well, so it does not
// go out anyway and reappear on the next Sent sync.
func (a *App) RemoveOwnReaction(messageID string) error {
	msg, err := a.messageStore.Get(messageID)
	if err != nil {
		return fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return fmt.Errorf("message not found: %s", messageID)
	}

	own := a.ownEmailsForAccount(msg.AccountID)
	emails := make([]string, 0, len(own))
	for email := range own {
		emails = append(emails, email)
	}
	if _, err := a.reactionStore.RemoveReaction(messageID, emails); err != nil {
		return fmt.Errorf("failed to remove reaction: %w", err)
	}
	if n, err := a.ensureSendQueueService().store.DeletePendingReaction(messageID); err == nil && n > 0 {
		a.ensureSendQueueService().emitEvent("sendqueue:updated", nil)
	}
	return nil
}

// SendReaction queues an emoji reaction to a message (internal id) as a
// reaction email addressed to the original sender only, for background
// send after the undo window. It returns as soon as the reaction is
// recorded and queued — the picker closes at once and the pill shows
// immediately (optimistic), with the existing Undo toast offering the
// undo window. The worker sends the queued email at dispatch and saves
// it to the Sent folder, exactly as the old synchronous send did.
//
// Limits are enforced before anything is queued (see
// reaction.ValidateReaction): the emoji must be a single emoji, the
// original must not be encrypted, group-addressed, over 20 recipients,
// or one the user was BCC'd on. A limit violation returns a distinct
// error the UI surfaces.
func (a *App) SendReaction(messageID string, emoji string) error {
	return a.sendReaction(messageID, emoji)
}

func (a *App) sendReaction(messageID, emoji string) error {
	log := logging.WithComponent("app")

	msg, err := a.messageStore.Get(messageID)
	if err != nil {
		return fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return fmt.Errorf("message not found: %s", messageID)
	}
	if strings.TrimSpace(msg.FromEmail) == "" {
		return fmt.Errorf("original message has no sender to react to")
	}

	// From is the identity the original was addressed to, else the
	// default identity — the same resolution a reply uses.
	from := smtp.Address{}
	identities, _ := a.accountStore.GetIdentities(msg.AccountID)
	if ident := selectReplyFromIdentity(identities, msg); ident != nil {
		from = smtp.Address{Name: ident.Name, Address: ident.Email}
	} else if acc, accErr := a.accountStore.Get(msg.AccountID); accErr == nil && acc != nil {
		from = smtp.Address{Name: acc.Name, Address: acc.Email}
	}
	if from.Address == "" {
		return fmt.Errorf("no sender identity for account %s", msg.AccountID)
	}

	orig := reaction.Original{
		MessageID:      msg.MessageID,
		Subject:        msg.Subject,
		FromName:       msg.FromName,
		FromEmail:      msg.FromEmail,
		To:             addressStrings(parseAddressList(msg.ToList)),
		Cc:             addressStrings(parseAddressList(msg.CcList)),
		Bcc:            addressStrings(parseAddressList(msg.BccList)),
		Date:           msg.Date,
		SMIMEEncrypted: msg.SMIMEEncrypted,
		PGPEncrypted:   msg.PGPEncrypted,
	}
	if err := reaction.ValidateReaction(emoji, orig, a.ownEmailsForAccount(msg.AccountID)); err != nil {
		return err
	}

	// The quoted original needs the full body: fetch it first when it
	// has not been fetched yet, rather than sending a body-less quote.
	// (The viewer normally fetched it already when the message was
	// opened, so this is the exception, not the rule.) Limits run
	// first, so a disallowed reaction fails fast without a fetch.
	if strings.TrimSpace(msg.BodyText) == "" && strings.TrimSpace(msg.BodyHTML) == "" {
		fetched, err := a.FetchMessageBody(messageID)
		if err != nil {
			return fmt.Errorf("failed to fetch original body for the reaction quote: %w", err)
		}
		msg = fetched
	}

	raw, reactionMessageID, err := reaction.BuildMessage(reaction.BuildOptions{
		Emoji:            emoji,
		SenderName:       from.Name,
		SenderEmail:      from.Address,
		Original:         orig,
		References:       threadReferences(msg),
		OriginalBodyText: msg.BodyText,
		OriginalBodyHTML: msg.BodyHTML,
	})
	if err != nil {
		return fmt.Errorf("failed to build reaction message: %w", err)
	}

	// Queue first, record second: if recording fails the queued row is
	// deleted again, so a pill never shows without its send (and a
	// send never happens without its pill). The queue worker — not
	// this call — sends the bytes at dispatch and saves to Sent.
	// ComposeMessage cannot express the reaction's multipart
	// structure, so the pre-built bytes go through as a raw item.
	svc := a.ensureSendQueueService()
	to := smtp.Address{Name: msg.FromName, Address: strings.TrimSpace(msg.FromEmail)}
	res, err := svc.QueueReaction(msg.AccountID, from, []string{to.Address}, raw,
		reaction.ReactionSubject(orig.Subject), to.Address, msg.ID)
	if err != nil {
		return fmt.Errorf("failed to queue reaction: %w", err)
	}

	// Record immediately under the reaction email's own Message-ID, so
	// the UI shows it at once and a later Sent-folder sync dedups
	// against the same source id instead of double-recording.
	rec := &reaction.Reaction{
		MessageID:       msg.ID,
		TargetMessageID: msg.MessageID,
		ReactorEmail:    from.Address,
		ReactorName:     from.Name,
		Emoji:           emoji,
		CreatedAt:       time.Now().UTC(),
		SourceMessageID: reactionMessageID,
	}
	if err := a.reactionStore.Record(rec); err != nil {
		_, _ = svc.store.Delete(res.QueueID)
		return fmt.Errorf("reaction queued but failed to record: %w", err)
	}

	log.Info().
		Str("messageID", messageID).
		Str("emoji", emoji).
		Str("from", from.Address).
		Str("to", to.Address).
		Str("queueID", res.QueueID).
		Msg("Reaction queued")
	return nil
}

// addressStrings extracts plain email strings from parsed addresses.
func addressStrings(addrs []smtp.Address) []string {
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if strings.TrimSpace(addr.Address) != "" {
			out = append(out, strings.TrimSpace(addr.Address))
		}
	}
	return out
}

// ownEmailsForAccount returns the set of the user's own addresses for an
// account: the account email plus every identity (alias) on it. Lookup
// failures degrade to an empty set rather than failing the whole call —
// IsOwn just reports false for every reaction then.
func (a *App) ownEmailsForAccount(accountID string) map[string]bool {
	own := make(map[string]bool)
	if acc, err := a.accountStore.Get(accountID); err == nil && acc != nil && acc.Email != "" {
		own[strings.ToLower(strings.TrimSpace(acc.Email))] = true
	}
	if identities, err := a.accountStore.GetIdentities(accountID); err == nil {
		for _, ident := range identities {
			if ident.Email != "" {
				own[strings.ToLower(strings.TrimSpace(ident.Email))] = true
			}
		}
	}
	return own
}
