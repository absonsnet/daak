package message

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/absonsnet/daak/internal/database"
	"github.com/absonsnet/daak/internal/enrich"
	"github.com/absonsnet/daak/internal/logging"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// Store provides message persistence operations
type Store struct {
	db  *database.DB
	log zerolog.Logger
}

// NewStore creates a new message store
func NewStore(db *database.DB) *Store {
	return &Store{
		db:  db,
		log: logging.WithComponent("message-store"),
	}
}

// filterHavingClause returns a HAVING clause for conversation-level filtering.
// prefix should be "" for single-table queries or "m." for joined queries.
func filterHavingClause(filter, prefix string) string {
	switch filter {
	case "unread":
		return fmt.Sprintf(" HAVING SUM(CASE WHEN %sis_read = 0 THEN 1 ELSE 0 END) > 0", prefix)
	case "starred":
		return fmt.Sprintf(" HAVING MAX(CASE WHEN %sis_starred = 1 THEN 1 ELSE 0 END) = 1", prefix)
	case "attachments":
		return fmt.Sprintf(" HAVING MAX(CASE WHEN %shas_attachments = 1 THEN 1 ELSE 0 END) = 1", prefix)
	default:
		return ""
	}
}

// filterWhereClause returns a WHERE condition for count queries.
// prefix should be "" for single-table queries or "m." for joined queries.
func filterWhereClause(filter, prefix string) string {
	switch filter {
	case "unread":
		return fmt.Sprintf(" AND %sis_read = 0", prefix)
	case "starred":
		return fmt.Sprintf(" AND %sis_starred = 1", prefix)
	case "attachments":
		return fmt.Sprintf(" AND %shas_attachments = 1", prefix)
	default:
		return ""
	}
}

// categoryFilterClause returns a WHERE fragment (plus its bound args)
// restricting a conversation list/count to one category tab (Phase E).
// category is "" (All — no restriction), "personal" or "automated";
// subCategory further restricts inside the Automated tab ("" = all
// automated). prefix is "" or "m." for the outer row's columns, and
// scopeColumn is the column the thread's latest message must share with
// the outer row ("folder_id" for a single folder, "account_id" for the
// Unified Inbox — the list groups by (thread, account) there).
//
// A conversation belongs to a tab by its LATEST message's category, so
// the condition is evaluated per row in WHERE (constant per thread —
// every row of a thread passes or fails together), never in HAVING,
// where a correlated subquery would see an arbitrary group row.
//
// Rows never classified (category IS NULL) count as Personal: the
// classifier defaults to Personal, so un-backfilled mail must not vanish
// from the Personal tab while the backfill is still running.
func categoryFilterClause(category, subCategory, prefix, scopeColumn string) (string, []any) {
	switch category {
	case "", "all":
		return "", nil
	case enrich.CategoryPersonal, enrich.CategoryAutomated:
	default:
		// Unknown tab value: show nothing rather than the whole folder.
		return " AND 1 = 0", nil
	}
	latestCategory := fmt.Sprintf(
		`(SELECT m2.category FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(%sthread_id, %sid) AND m2.%s = %s%s ORDER BY m2.date DESC LIMIT 1)`,
		prefix, prefix, scopeColumn, prefix, scopeColumn,
	)
	clause := fmt.Sprintf(" AND COALESCE(%s, 'personal') = ?", latestCategory)
	args := []any{category}
	if subCategory != "" {
		if category != enrich.CategoryAutomated {
			// Sub-categories only exist inside the Automated tab.
			return " AND 1 = 0", nil
		}
		latestSub := fmt.Sprintf(
			`(SELECT m2.sub_category FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(%sthread_id, %sid) AND m2.%s = %s%s ORDER BY m2.date DESC LIMIT 1)`,
			prefix, prefix, scopeColumn, prefix, scopeColumn,
		)
		clause += fmt.Sprintf(" AND %s = ?", latestSub)
		args = append(args, subCategory)
	}
	return clause, args
}

// CategoryCounts is the per-tab conversation count for one list scope
// (Phase E): the three category tabs plus, inside Automated, one count
// per sub-category. NULL (never classified) counts as Personal.
type CategoryCounts struct {
	Personal      int            `json:"personal"`
	Automated     int            `json:"automated"`
	Total         int            `json:"total"`
	SubCategories map[string]int `json:"subCategories"`
}

// ListByFolder returns message headers for a folder with pagination
func (s *Store) ListByFolder(folderID string, offset, limit int) ([]*MessageHeader, error) {
	query := `
		SELECT id, account_id, folder_id, uid, subject, from_name, from_email,
		       date, snippet, is_read, is_starred, has_attachments,
		       extract_kind, extract_value, extract_rule
		FROM messages
		WHERE folder_id = ? AND is_hidden = 0
		ORDER BY date DESC
		LIMIT ? OFFSET ?
	`

	rows, err := s.db.Query(query, folderID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()

	var messages []*MessageHeader
	for rows.Next() {
		m := &MessageHeader{}
		var dateStr sql.NullString
		var snippet sql.NullString
		var uidI64 int64

		var extractKind, extractValue, extractRule sql.NullString
		err := rows.Scan(
			&m.ID, &m.AccountID, &m.FolderID, &uidI64,
			&m.Subject, &m.FromName, &m.FromEmail,
			&dateStr, &snippet,
			&m.IsRead, &m.IsStarred, &m.HasAttachments,
			&extractKind, &extractValue, &extractRule,
		)
		if extractKind.Valid {
			m.ExtractKind = extractKind.String
		}
		if extractValue.Valid {
			m.ExtractValue = extractValue.String
		}
		if extractRule.Valid {
			m.ExtractRule = extractRule.String
		}
		if err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		m.UID = uint32(uidI64)

		if dateStr.Valid && dateStr.String != "" {
			m.Date = parseTimeString(dateStr.String)
		}
		if snippet.Valid {
			m.Snippet = snippet.String
		}

		messages = append(messages, m)
	}

	return messages, nil
}

// unifiedFolderJoin returns the folders JOIN for a unified folder view.
// Folder types (inbox/sent/drafts/spam/trash) restrict the join to folders
// of that type — which is how providers' differently-named folders combine:
// sync classifies each account's Spam/Junk folder as 'spam' and its
// Trash/Deleted Items/Bin folder as 'trash'. Starred is flag-based, not
// folder-based: every starred message across all folders.
func unifiedFolderJoin(folderType string) (string, []any, error) {
	switch folderType {
	case "inbox", "sent", "drafts", "spam", "trash":
		return "INNER JOIN folders f ON m.folder_id = f.id AND f.folder_type = ?", []any{folderType}, nil
	case "starred":
		return "INNER JOIN folders f ON m.folder_id = f.id", nil, nil
	default:
		return "", nil, fmt.Errorf("unknown unified folder type %q", folderType)
	}
}

// unifiedFolderStarredWhere is the extra WHERE fragment for the Starred
// view (flag-based); empty for folder-type views.
func unifiedFolderStarredWhere(folderType string) string {
	if folderType == "starred" {
		return " AND m.is_starred = 1"
	}
	return ""
}

// ListConversationsUnifiedFolder returns conversations from all folders of
// one type across all accounts (inbox/sent/drafts/spam/trash), or all
// starred conversations across all folders ("starred").
func (s *Store) ListConversationsUnifiedFolder(folderType string, offset, limit int, sortOrder, filter, category, subCategory string) ([]*Conversation, error) {
	folderJoin, folderArgs, err := unifiedFolderJoin(folderType)
	if err != nil {
		return nil, err
	}
	// Determine sort direction
	orderClause := "ORDER BY latest_date DESC"
	if sortOrder == "oldest" {
		orderClause = "ORDER BY latest_date ASC"
	}

	// Phase E: category tab filter ("" = All). A conversation belongs to
	// a tab by its latest message's category.
	categoryClause, categoryArgs := categoryFilterClause(category, subCategory, "m.", "account_id")

	// Query conversations from all folders of the unified type, joining with accounts for name and color
	query := `
		SELECT 
			COALESCE(m.thread_id, m.id) as conv_thread_id,
			MIN(m.subject) as subject,
			MAX(m.snippet) as snippet,
			COUNT(*) as message_count,
			SUM(CASE WHEN m.is_read = 0 THEN 1 ELSE 0 END) as unread_count,
			MAX(CASE WHEN m.has_attachments = 1 THEN 1 ELSE 0 END) as has_attachments,
			MAX(CASE WHEN m.is_starred = 1 THEN 1 ELSE 0 END) as is_starred,
			MAX(m.date) as latest_date,
			GROUP_CONCAT(m.id) as message_ids,
			MAX(CASE WHEN m.smime_encrypted = 1 OR m.pgp_encrypted = 1 THEN 1 ELSE 0 END) as is_encrypted,
			(SELECT m2.extract_kind FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as extract_kind,
			(SELECT m2.extract_value FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as extract_value,
			(SELECT m2.extract_rule FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as extract_rule,
			(SELECT m2.id FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as extract_message_id,
			(SELECT m2.category FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as category,
			(SELECT m2.sub_category FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as sub_category,
			a.id as account_id,
			a.name as account_name,
			a.color as account_color,
			f.id as folder_id,
			json_group_array(json_object('name', m.from_name, 'email', m.from_email, 'date', m.date, 'snippet', m.snippet)) as participants_json
		FROM messages m
		` + folderJoin + `
		INNER JOIN accounts a ON f.account_id = a.id AND a.enabled = 1
		WHERE m.is_hidden = 0` + unifiedFolderStarredWhere(folderType) + categoryClause + `
		GROUP BY COALESCE(m.thread_id, m.id), a.id` +
		filterHavingClause(filter, "m.") + `
		` + orderClause + `
		LIMIT ? OFFSET ?
	`

	args := append(append(append([]any{}, folderArgs...), categoryArgs...), limit, offset)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query unified folder conversations: %w", err)
	}
	defer rows.Close()

	var conversations []*Conversation
	for rows.Next() {
		c := &Conversation{}
		var latestDateStr sql.NullString
		var snippet sql.NullString
		var messageIDsStr sql.NullString
		var participantsJSON sql.NullString
		var extractKind, extractValue, extractRule, extractMessageID sql.NullString
		var categoryVal, subCategoryVal sql.NullString

		err := rows.Scan(
			&c.ThreadID,
			&c.Subject,
			&snippet,
			&c.MessageCount,
			&c.UnreadCount,
			&c.HasAttachments,
			&c.IsStarred,
			&latestDateStr,
			&messageIDsStr,
			&c.IsEncrypted,
			&extractKind, &extractValue, &extractRule, &extractMessageID,
			&categoryVal, &subCategoryVal,
			&c.AccountID,
			&c.AccountName,
			&c.AccountColor,
			&c.FolderID,
			&participantsJSON,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan unified folder conversation: %w", err)
		}
		if extractKind.Valid {
			c.ExtractKind = extractKind.String
		}
		if extractValue.Valid {
			c.ExtractValue = extractValue.String
		}
		if extractRule.Valid {
			c.ExtractRule = extractRule.String
		}
		if extractMessageID.Valid {
			c.ExtractMessageID = extractMessageID.String
		}
		if categoryVal.Valid {
			c.Category = categoryVal.String
		}
		if subCategoryVal.Valid {
			c.SubCategory = subCategoryVal.String
		}

		if snippet.Valid {
			c.Snippet = snippet.String
		}
		if latestDateStr.Valid && latestDateStr.String != "" {
			c.LatestDate = parseTimeString(latestDateStr.String)
		}

		// Parse message IDs from comma-separated string
		if messageIDsStr.Valid && messageIDsStr.String != "" {
			c.MessageIDs = strings.Split(messageIDsStr.String, ",")
		}

		if participantsJSON.Valid {
			var latestSnippet string
			c.Participants, latestSnippet = parseParticipantsJSON(participantsJSON.String)
			if latestSnippet != "" {
				c.Snippet = latestSnippet
			}
		}

		conversations = append(conversations, c)
	}

	return conversations, nil
}

// ListConversationsUnifiedInbox returns conversations from all inbox folders across all accounts
// This is used for the unified inbox view
func (s *Store) ListConversationsUnifiedInbox(offset, limit int, sortOrder, filter, category, subCategory string) ([]*Conversation, error) {
	return s.ListConversationsUnifiedFolder("inbox", offset, limit, sortOrder, filter, category, subCategory)
}

// CountConversationsUnifiedFolder returns the total count of conversations
// in a unified folder view (see ListConversationsUnifiedFolder).
func (s *Store) CountConversationsUnifiedFolder(folderType, filter, category, subCategory string) (int, error) {
	folderJoin, folderArgs, err := unifiedFolderJoin(folderType)
	if err != nil {
		return 0, err
	}
	filterCond := filterWhereClause(filter, "m.")
	categoryClause, categoryArgs := categoryFilterClause(category, subCategory, "m.", "account_id")
	wherePart := " WHERE m.is_hidden = 0" + unifiedFolderStarredWhere(folderType)
	if filterCond != "" {
		wherePart += filterCond
	}
	wherePart += categoryClause

	query := `
		SELECT COUNT(DISTINCT COALESCE(m.thread_id, m.id) || '-' || a.id)
		FROM messages m
		` + folderJoin + `
		INNER JOIN accounts a ON f.account_id = a.id AND a.enabled = 1
	` + wherePart

	args := append(append([]any{}, folderArgs...), categoryArgs...)
	var count int
	err = s.db.QueryRow(query, args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count unified folder conversations: %w", err)
	}
	return count, nil
}

// CountConversationsUnifiedInbox returns the total count of conversations across all inbox folders
func (s *Store) CountConversationsUnifiedInbox(filter, category, subCategory string) (int, error) {
	return s.CountConversationsUnifiedFolder("inbox", filter, category, subCategory)
}

// GetUnifiedInboxUnreadCount returns the total unread message count across all inbox folders
// Uses the cached folder.unread_count values to stay consistent with sidebar folder counts
func (s *Store) GetUnifiedInboxUnreadCount() (int, error) {
	// First, log individual inbox folders for debugging
	debugQuery := `
		SELECT f.id, f.name, f.folder_type, f.unread_count, a.name as account_name, a.enabled
		FROM folders f
		INNER JOIN accounts a ON f.account_id = a.id
		WHERE f.folder_type = 'inbox'
	`
	rows, err := s.db.Query(debugQuery)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var folderID, folderName, folderType, accountName string
			var unreadCount int
			var enabled bool
			if err := rows.Scan(&folderID, &folderName, &folderType, &unreadCount, &accountName, &enabled); err == nil {
				s.log.Debug().
					Str("folderID", folderID).
					Str("folderName", folderName).
					Str("folderType", folderType).
					Int("unreadCount", unreadCount).
					Str("accountName", accountName).
					Bool("enabled", enabled).
					Msg("Inbox folder for unified count")
			}
		}
	}

	query := `
		SELECT COALESCE(SUM(f.unread_count), 0)
		FROM folders f
		INNER JOIN accounts a ON f.account_id = a.id AND a.enabled = 1
		WHERE f.folder_type = 'inbox'
	`

	var count int
	err = s.db.QueryRow(query).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count unified inbox unread: %w", err)
	}

	s.log.Debug().Int("unreadCount", count).Msg("GetUnifiedInboxUnreadCount (sum of folder counts)")
	return count, nil
}

// GetUnifiedFolderUnreadCounts returns the unread message count per unified
// folder type (inbox/starred/sent/drafts/spam/trash) in one call, for the
// sidebar badges. Folder types use the cached folder.unread_count sums;
// Starred counts unread starred messages across all folders (there is no
// starred folder). Outbox is not a message folder — its badge comes from
// the Mail Merge queue stats instead.
func (s *Store) GetUnifiedFolderUnreadCounts() (map[string]int, error) {
	counts := map[string]int{
		"inbox": 0, "starred": 0, "sent": 0, "drafts": 0, "spam": 0, "trash": 0,
	}
	rows, err := s.db.Query(`
		SELECT f.folder_type, COALESCE(SUM(f.unread_count), 0)
		FROM folders f
		INNER JOIN accounts a ON f.account_id = a.id AND a.enabled = 1
		WHERE f.folder_type IN ('inbox', 'sent', 'drafts', 'spam', 'trash')
		GROUP BY f.folder_type
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to count unified folder unread: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var folderType string
		var count int
		if err := rows.Scan(&folderType, &count); err != nil {
			return nil, fmt.Errorf("failed to scan unified folder unread: %w", err)
		}
		counts[folderType] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read unified folder unread: %w", err)
	}

	var starred int
	err = s.db.QueryRow(`
		SELECT COUNT(*)
		FROM messages m
		INNER JOIN accounts a ON m.account_id = a.id AND a.enabled = 1
		WHERE m.is_starred = 1 AND m.is_read = 0 AND m.is_hidden = 0
	`).Scan(&starred)
	if err != nil {
		return nil, fmt.Errorf("failed to count unified starred unread: %w", err)
	}
	counts["starred"] = starred
	return counts, nil
}

// CountUnreadByFolder returns the unread message count for a folder
func (s *Store) CountUnreadByFolder(folderID string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM messages WHERE folder_id = ? AND is_read = 0 AND is_hidden = 0", folderID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count unread messages: %w", err)
	}
	return count, nil
}

// CountReadByFolder returns the read message count for a folder
func (s *Store) CountReadByFolder(folderID string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM messages WHERE folder_id = ? AND is_read = 1 AND is_hidden = 0", folderID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count read messages: %w", err)
	}
	return count, nil
}

// GetUnreadMessageIDsByFolder returns the IDs of all unread messages in a folder
func (s *Store) GetUnreadMessageIDsByFolder(folderID string) ([]string, error) {
	rows, err := s.db.Query("SELECT id FROM messages WHERE folder_id = ? AND is_read = 0 AND is_hidden = 0", folderID)
	if err != nil {
		return nil, fmt.Errorf("failed to query unread messages: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan message id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// GetReadMessageIDsByFolder returns the IDs of all read messages in a folder
func (s *Store) GetReadMessageIDsByFolder(folderID string) ([]string, error) {
	rows, err := s.db.Query("SELECT id FROM messages WHERE folder_id = ? AND is_read = 1 AND is_hidden = 0", folderID)
	if err != nil {
		return nil, fmt.Errorf("failed to query read messages: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan message id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// GetAllIDsByFolder returns the IDs of all messages in a folder
func (s *Store) GetAllIDsByFolder(folderID string) ([]string, error) {
	rows, err := s.db.Query("SELECT id FROM messages WHERE folder_id = ? AND is_hidden = 0", folderID)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan message id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// Get returns a full message by ID
func (s *Store) Get(id string) (*Message, error) {
	query := `
		SELECT id, account_id, folder_id, uid, message_id, in_reply_to, thread_id,
		       subject, from_name, from_email, to_list, cc_list, bcc_list, reply_to, date,
		       snippet, is_read, is_starred, is_answered, is_forwarded, is_draft, is_deleted,
		       size, has_attachments, body_text, body_html, body_fetched, body_truncated,
		       read_receipt_to, read_receipt_handled,
		       smime_status, smime_signer_email, smime_signer_subject,
		       smime_encrypted, (smime_raw_body IS NOT NULL) as has_smime,
		       pgp_status, pgp_signer_email, pgp_signer_key_id,
		       pgp_encrypted, (pgp_raw_body IS NOT NULL) as has_pgp,
		       list_unsubscribe, list_unsubscribe_post, category, sub_category,
		       extract_kind, extract_value, extract_rule,
		       received_at
		FROM messages
		WHERE id = ?
	`

	m := &Message{}
	var messageID, inReplyTo, threadID, toList, ccList, bccList, replyTo, snippet, bodyText, bodyHTML, readReceiptTo sql.NullString
	var smimeStatus, smimeSignerEmail, smimeSignerSubject sql.NullString
	var pgpStatus, pgpSignerEmail, pgpSignerKeyID sql.NullString
	var listUnsubscribe, category, subCategory, extractKind, extractValue, extractRule sql.NullString
	var dateStr, receivedAtStr sql.NullString
	var uidI64 int64

	err := s.db.QueryRow(query, id).Scan(
		&m.ID, &m.AccountID, &m.FolderID, &uidI64, &messageID, &inReplyTo, &threadID,
		&m.Subject, &m.FromName, &m.FromEmail, &toList, &ccList, &bccList, &replyTo, &dateStr,
		&snippet, &m.IsRead, &m.IsStarred, &m.IsAnswered, &m.IsForwarded, &m.IsDraft, &m.IsDeleted,
		&m.Size, &m.HasAttachments, &bodyText, &bodyHTML, &m.BodyFetched, &m.BodyTruncated,
		&readReceiptTo, &m.ReadReceiptHandled,
		&smimeStatus, &smimeSignerEmail, &smimeSignerSubject,
		&m.SMIMEEncrypted, &m.HasSMIME,
		&pgpStatus, &pgpSignerEmail, &pgpSignerKeyID,
		&m.PGPEncrypted, &m.HasPGP,
		&listUnsubscribe, &m.ListUnsubscribePost, &category, &subCategory,
		&extractKind, &extractValue, &extractRule,
		&receivedAtStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get message: %w", err)
	}
	m.UID = uint32(uidI64)

	if messageID.Valid {
		m.MessageID = messageID.String
	}
	if inReplyTo.Valid {
		m.InReplyTo = inReplyTo.String
	}
	if threadID.Valid {
		m.ThreadID = threadID.String
	}
	if toList.Valid {
		m.ToList = toList.String
	}
	if ccList.Valid {
		m.CcList = ccList.String
	}
	if bccList.Valid {
		m.BccList = bccList.String
	}
	if replyTo.Valid {
		m.ReplyTo = replyTo.String
	}
	if dateStr.Valid && dateStr.String != "" {
		m.Date = parseTimeString(dateStr.String)
	}
	if snippet.Valid {
		m.Snippet = snippet.String
	}
	if bodyText.Valid {
		m.BodyText = bodyText.String
	}
	if bodyHTML.Valid {
		m.BodyHTML = bodyHTML.String
	}
	if readReceiptTo.Valid {
		m.ReadReceiptTo = readReceiptTo.String
	}
	if smimeStatus.Valid {
		m.SMIMEStatus = smimeStatus.String
	}
	if smimeSignerEmail.Valid {
		m.SMIMESignerEmail = smimeSignerEmail.String
	}
	if smimeSignerSubject.Valid {
		m.SMIMESignerSubject = smimeSignerSubject.String
	}
	if pgpStatus.Valid {
		m.PGPStatus = pgpStatus.String
	}
	if pgpSignerEmail.Valid {
		m.PGPSignerEmail = pgpSignerEmail.String
	}
	if pgpSignerKeyID.Valid {
		m.PGPSignerKeyID = pgpSignerKeyID.String
	}
	if listUnsubscribe.Valid {
		m.ListUnsubscribe = listUnsubscribe.String
	}
	if category.Valid {
		m.Category = category.String
	}
	if subCategory.Valid {
		m.SubCategory = subCategory.String
	}
	if extractKind.Valid {
		m.ExtractKind = extractKind.String
	}
	if extractValue.Valid {
		m.ExtractValue = extractValue.String
	}
	if extractRule.Valid {
		m.ExtractRule = extractRule.String
	}
	if receivedAtStr.Valid && receivedAtStr.String != "" {
		m.ReceivedAt = parseTimeString(receivedAtStr.String)
	}

	return m, nil
}

// GetByUID returns a message by folder ID and UID
func (s *Store) GetByUID(folderID string, uid uint32) (*Message, error) {
	query := `
		SELECT id, account_id, folder_id, uid, message_id, in_reply_to, thread_id,
		       subject, from_name, from_email, to_list, cc_list, bcc_list, reply_to, date,
		       snippet, is_read, is_starred, is_answered, is_forwarded, is_draft, is_deleted,
		       size, has_attachments, body_text, body_html, body_fetched, body_truncated,
		       read_receipt_to, read_receipt_handled,
		       smime_status, smime_signer_email, smime_signer_subject,
		       smime_encrypted, (smime_raw_body IS NOT NULL) as has_smime,
		       pgp_status, pgp_signer_email, pgp_signer_key_id,
		       pgp_encrypted, (pgp_raw_body IS NOT NULL) as has_pgp,
		       list_unsubscribe, list_unsubscribe_post, category, sub_category,
		       extract_kind, extract_value, extract_rule,
		       received_at
		FROM messages
		WHERE folder_id = ? AND uid = ?
	`

	m := &Message{}
	var messageID, inReplyTo, threadID, toList, ccList, bccList, replyTo, snippet, bodyText, bodyHTML, readReceiptTo sql.NullString
	var smimeStatus, smimeSignerEmail, smimeSignerSubject sql.NullString
	var pgpStatus, pgpSignerEmail, pgpSignerKeyID sql.NullString
	var listUnsubscribe, category, subCategory, extractKind, extractValue, extractRule sql.NullString
	var dateStr, receivedAtStr sql.NullString
	var uidI64 int64

	err := s.db.QueryRow(query, folderID, uid).Scan(
		&m.ID, &m.AccountID, &m.FolderID, &uidI64, &messageID, &inReplyTo, &threadID,
		&m.Subject, &m.FromName, &m.FromEmail, &toList, &ccList, &bccList, &replyTo, &dateStr,
		&snippet, &m.IsRead, &m.IsStarred, &m.IsAnswered, &m.IsForwarded, &m.IsDraft, &m.IsDeleted,
		&m.Size, &m.HasAttachments, &bodyText, &bodyHTML, &m.BodyFetched, &m.BodyTruncated,
		&readReceiptTo, &m.ReadReceiptHandled,
		&smimeStatus, &smimeSignerEmail, &smimeSignerSubject,
		&m.SMIMEEncrypted, &m.HasSMIME,
		&pgpStatus, &pgpSignerEmail, &pgpSignerKeyID,
		&m.PGPEncrypted, &m.HasPGP,
		&listUnsubscribe, &m.ListUnsubscribePost, &category, &subCategory,
		&extractKind, &extractValue, &extractRule,
		&receivedAtStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get message: %w", err)
	}
	m.UID = uint32(uidI64)

	// Populate optional fields
	if messageID.Valid {
		m.MessageID = messageID.String
	}
	if inReplyTo.Valid {
		m.InReplyTo = inReplyTo.String
	}
	if threadID.Valid {
		m.ThreadID = threadID.String
	}
	if toList.Valid {
		m.ToList = toList.String
	}
	if ccList.Valid {
		m.CcList = ccList.String
	}
	if bccList.Valid {
		m.BccList = bccList.String
	}
	if replyTo.Valid {
		m.ReplyTo = replyTo.String
	}
	if dateStr.Valid && dateStr.String != "" {
		m.Date = parseTimeString(dateStr.String)
	}
	if snippet.Valid {
		m.Snippet = snippet.String
	}
	if bodyText.Valid {
		m.BodyText = bodyText.String
	}
	if bodyHTML.Valid {
		m.BodyHTML = bodyHTML.String
	}
	if readReceiptTo.Valid {
		m.ReadReceiptTo = readReceiptTo.String
	}
	if smimeStatus.Valid {
		m.SMIMEStatus = smimeStatus.String
	}
	if smimeSignerEmail.Valid {
		m.SMIMESignerEmail = smimeSignerEmail.String
	}
	if smimeSignerSubject.Valid {
		m.SMIMESignerSubject = smimeSignerSubject.String
	}
	if pgpStatus.Valid {
		m.PGPStatus = pgpStatus.String
	}
	if pgpSignerEmail.Valid {
		m.PGPSignerEmail = pgpSignerEmail.String
	}
	if pgpSignerKeyID.Valid {
		m.PGPSignerKeyID = pgpSignerKeyID.String
	}
	if listUnsubscribe.Valid {
		m.ListUnsubscribe = listUnsubscribe.String
	}
	if category.Valid {
		m.Category = category.String
	}
	if subCategory.Valid {
		m.SubCategory = subCategory.String
	}
	if extractKind.Valid {
		m.ExtractKind = extractKind.String
	}
	if extractValue.Valid {
		m.ExtractValue = extractValue.String
	}
	if extractRule.Valid {
		m.ExtractRule = extractRule.String
	}
	if receivedAtStr.Valid && receivedAtStr.String != "" {
		m.ReceivedAt = parseTimeString(receivedAtStr.String)
	}

	return m, nil
}

// Create creates a new message
func (s *Store) Create(m *Message) error {
	if m.ID == "" {
		m.ID = uuid.New().String()
	}
	if m.ReceivedAt.IsZero() {
		m.ReceivedAt = time.Now().UTC()
	}

	s.log.Debug().
		Str("id", m.ID).
		Str("subject", m.Subject).
		Str("messageID", m.MessageID).
		Str("threadID", m.ThreadID).
		Int("bodyTextLen", len(m.BodyText)).
		Int("bodyHTMLLen", len(m.BodyHTML)).
		Uint32("uid", m.UID).
		Msg("Creating message in store")

	query := `
		INSERT INTO messages (
			id, account_id, folder_id, uid, message_id, in_reply_to, references_list, thread_id,
			subject, from_name, from_email, to_list, cc_list, bcc_list, reply_to, date,
			snippet, is_read, is_starred, is_answered, is_forwarded, is_draft, is_deleted,
			size, has_attachments, body_text, body_html, body_fetched,
			read_receipt_to, read_receipt_handled, received_at,
			list_unsubscribe, list_unsubscribe_post, category, sub_category,
			extract_kind, extract_value, extract_rule
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.Exec(query,
		m.ID, m.AccountID, m.FolderID, m.UID,
		nullString(m.MessageID), nullString(m.InReplyTo), nullString(m.References), nullString(m.ThreadID),
		m.Subject, m.FromName, m.FromEmail,
		nullString(m.ToList), nullString(m.CcList), nullString(m.BccList), nullString(m.ReplyTo),
		m.Date, nullString(m.Snippet),
		m.IsRead, m.IsStarred, m.IsAnswered, m.IsForwarded, m.IsDraft, m.IsDeleted,
		m.Size, m.HasAttachments,
		nullString(m.BodyText), nullString(m.BodyHTML), m.BodyFetched,
		nullString(m.ReadReceiptTo), m.ReadReceiptHandled,
		m.ReceivedAt,
		nullString(m.ListUnsubscribe), m.ListUnsubscribePost,
		nullString(m.Category), nullString(m.SubCategory),
		nullString(m.ExtractKind), nullString(m.ExtractValue), nullString(m.ExtractRule),
	)
	if err != nil {
		return fmt.Errorf("failed to create message: %w", err)
	}

	return nil
}

// Upsert inserts a message or updates it if a row with the same (folder_id, uid) already exists.
// This handles cases where a previous copy was deleted but the stale row remains, or where
// the IMAP server reuses UIDs after EXPUNGE.
//
// On conflict only header/flag/enrichment columns are updated. The id, the body
// columns (body_text, body_html, body_fetched, snippet, has_attachments), the
// computed thread_id, and received_at are NEVER overwritten: the header path
// calls Upsert with a header-only message (BodyFetched=false, empty body), so
// overwriting them would wipe an already-fetched body, blank the snippet,
// clear the attachment flag, strand attachment/reaction rows (their foreign
// keys have no ON UPDATE CASCADE, so changing id fails outright), and reset
// received_at to "now" on every re-sync. received_at is therefore first-seen
// time, set on insert only; thread_id is computed by finalizeSavedHeaders
// after the save.
func (s *Store) Upsert(m *Message) error {
	// Reuse the stored id on conflict: it is the primary key referenced by
	// attachment/reaction rows, and the caller (header sync) needs m.ID to
	// keep pointing at the same row for the thread computation that follows.
	var existingID string
	err := s.db.QueryRow(
		"SELECT id FROM messages WHERE folder_id = ? AND uid = ?",
		m.FolderID, m.UID,
	).Scan(&existingID)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("failed to look up existing message: %w", err)
	}
	if existingID != "" {
		m.ID = existingID
	} else if m.ID == "" {
		m.ID = uuid.New().String()
	}
	if m.ReceivedAt.IsZero() {
		m.ReceivedAt = time.Now().UTC()
	}

	query := `
		INSERT INTO messages (
			id, account_id, folder_id, uid, message_id, in_reply_to, references_list, thread_id,
			subject, from_name, from_email, to_list, cc_list, bcc_list, reply_to, date,
			snippet, is_read, is_starred, is_answered, is_forwarded, is_draft, is_deleted,
			size, has_attachments, body_text, body_html, body_fetched,
			read_receipt_to, read_receipt_handled, received_at,
			list_unsubscribe, list_unsubscribe_post, category, sub_category,
			extract_kind, extract_value, extract_rule
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(folder_id, uid) DO UPDATE SET
			account_id=excluded.account_id,
			message_id=excluded.message_id, in_reply_to=excluded.in_reply_to,
			references_list=excluded.references_list,
			subject=excluded.subject, from_name=excluded.from_name, from_email=excluded.from_email,
			to_list=excluded.to_list, cc_list=excluded.cc_list, bcc_list=excluded.bcc_list,
			reply_to=excluded.reply_to, date=excluded.date,
			is_read=excluded.is_read, is_starred=excluded.is_starred,
			is_answered=excluded.is_answered, is_forwarded=excluded.is_forwarded,
			is_draft=excluded.is_draft, is_deleted=excluded.is_deleted,
			size=excluded.size,
			read_receipt_to=excluded.read_receipt_to, read_receipt_handled=excluded.read_receipt_handled,
			list_unsubscribe=excluded.list_unsubscribe,
			list_unsubscribe_post=excluded.list_unsubscribe_post,
			category=excluded.category, sub_category=excluded.sub_category,
			extract_kind=excluded.extract_kind, extract_value=excluded.extract_value,
			extract_rule=excluded.extract_rule
	`

	_, err = s.db.Exec(query,
		m.ID, m.AccountID, m.FolderID, m.UID,
		nullString(m.MessageID), nullString(m.InReplyTo), nullString(m.References), nullString(m.ThreadID),
		m.Subject, m.FromName, m.FromEmail,
		nullString(m.ToList), nullString(m.CcList), nullString(m.BccList), nullString(m.ReplyTo),
		m.Date, nullString(m.Snippet),
		m.IsRead, m.IsStarred, m.IsAnswered, m.IsForwarded, m.IsDraft, m.IsDeleted,
		m.Size, m.HasAttachments,
		nullString(m.BodyText), nullString(m.BodyHTML), m.BodyFetched,
		nullString(m.ReadReceiptTo), m.ReadReceiptHandled,
		m.ReceivedAt,
		nullString(m.ListUnsubscribe), m.ListUnsubscribePost,
		nullString(m.Category), nullString(m.SubCategory),
		nullString(m.ExtractKind), nullString(m.ExtractValue), nullString(m.ExtractRule),
	)
	if err != nil {
		return fmt.Errorf("failed to upsert message: %w", err)
	}

	return nil
}

// Update updates an existing message
func (s *Store) Update(m *Message) error {
	query := `
		UPDATE messages SET
			message_id = ?, in_reply_to = ?, references_list = ?, thread_id = ?,
			subject = ?, from_name = ?, from_email = ?,
			to_list = ?, cc_list = ?, bcc_list = ?, reply_to = ?, date = ?,
			snippet = ?, is_read = ?, is_starred = ?, is_answered = ?, is_forwarded = ?,
			is_draft = ?, is_deleted = ?, size = ?, has_attachments = ?,
			body_text = ?, body_html = ?, read_receipt_to = ?, read_receipt_handled = ?,
			list_unsubscribe = ?, list_unsubscribe_post = ?, category = ?, sub_category = ?,
			extract_kind = ?, extract_value = ?, extract_rule = ?
		WHERE id = ?
	`

	_, err := s.db.Exec(query,
		nullString(m.MessageID), nullString(m.InReplyTo), nullString(m.References), nullString(m.ThreadID),
		m.Subject, m.FromName, m.FromEmail,
		nullString(m.ToList), nullString(m.CcList), nullString(m.BccList), nullString(m.ReplyTo),
		m.Date, nullString(m.Snippet),
		m.IsRead, m.IsStarred, m.IsAnswered, m.IsForwarded,
		m.IsDraft, m.IsDeleted, m.Size, m.HasAttachments,
		nullString(m.BodyText), nullString(m.BodyHTML),
		nullString(m.ReadReceiptTo), m.ReadReceiptHandled,
		nullString(m.ListUnsubscribe), m.ListUnsubscribePost,
		nullString(m.Category), nullString(m.SubCategory),
		nullString(m.ExtractKind), nullString(m.ExtractValue), nullString(m.ExtractRule),
		m.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update message: %w", err)
	}

	return nil
}

// UpdateFlags updates only the flags for a message
func (s *Store) UpdateFlags(id string, isRead, isStarred, isAnswered, isForwarded, isDraft, isDeleted bool) error {
	query := `
		UPDATE messages SET
			is_read = ?, is_starred = ?, is_answered = ?, is_forwarded = ?,
			is_draft = ?, is_deleted = ?
		WHERE id = ?
	`

	_, err := s.db.Exec(query, isRead, isStarred, isAnswered, isForwarded, isDraft, isDeleted, id)
	if err != nil {
		return fmt.Errorf("failed to update flags: %w", err)
	}

	return nil
}

// UpdateFlagsByUID updates flags for a message by folder ID and UID
func (s *Store) UpdateFlagsByUID(folderID string, uid uint32, isRead, isStarred, isAnswered, isForwarded, isDraft, isDeleted bool) error {
	query := `
		UPDATE messages SET
			is_read = ?, is_starred = ?, is_answered = ?, is_forwarded = ?,
			is_draft = ?, is_deleted = ?
		WHERE folder_id = ? AND uid = ?
	`

	_, err := s.db.Exec(query, isRead, isStarred, isAnswered, isForwarded, isDraft, isDeleted, folderID, uid)
	if err != nil {
		return fmt.Errorf("failed to update flags by UID: %w", err)
	}

	return nil
}

// FlagUpdate represents a flag update for a single message by UID
type FlagUpdate struct {
	UID         uint32
	IsRead      bool
	IsStarred   bool
	IsAnswered  bool
	IsForwarded bool
	IsDraft     bool
	IsDeleted   bool
}

// UpdateFlagsByUIDBatch updates flags for multiple messages in a single transaction.
// This is much more efficient than calling UpdateFlagsByUID repeatedly.
func (s *Store) UpdateFlagsByUIDBatch(folderID string, updates []FlagUpdate) error {
	if len(updates) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
		UPDATE messages SET
			is_read = ?, is_starred = ?, is_answered = ?, is_forwarded = ?,
			is_draft = ?, is_deleted = ?
		WHERE folder_id = ? AND uid = ?
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, u := range updates {
		_, err := stmt.Exec(u.IsRead, u.IsStarred, u.IsAnswered, u.IsForwarded, u.IsDraft, u.IsDeleted, folderID, u.UID)
		if err != nil {
			return fmt.Errorf("failed to update flags for UID %d: %w", u.UID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// MarkReadReceiptHandled marks a message's read receipt as handled (sent or ignored)
func (s *Store) MarkReadReceiptHandled(id string) error {
	_, err := s.db.Exec("UPDATE messages SET read_receipt_handled = 1 WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to mark read receipt handled: %w", err)
	}
	return nil
}

// Delete deletes a message
func (s *Store) Delete(id string) error {
	_, err := s.db.Exec("DELETE FROM messages WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete message: %w", err)
	}
	return nil
}

// MarkHidden hides a message from the message list. Used for reaction
// emails once their reaction is recorded and linked to the original: the
// row is kept (deleting it would make the next sync re-fetch the UID as
// new) but excluded from every list/count/search/without-body query. It
// also marks the message read and its body fetched so a hidden row never
// inflates unread counts and is never re-fetched for a body.
func (s *Store) MarkHidden(id string) error {
	_, err := s.db.Exec(
		"UPDATE messages SET is_hidden = 1, is_read = 1, body_fetched = 1 WHERE id = ?",
		id,
	)
	if err != nil {
		return fmt.Errorf("failed to hide message: %w", err)
	}
	return nil
}

// GetInternalIDByMessageID returns the internal id of a message by its
// Message-ID header value within an account. The comparison ignores
// surrounding angle brackets, so In-Reply-To values and Message-ID values
// match regardless of which form the server sent. Returns sql.ErrNoRows
// when no such message is in the DB.
func (s *Store) GetInternalIDByMessageID(accountID, messageID string) (string, error) {
	var id string
	err := s.db.QueryRow(
		`SELECT id FROM messages
		 WHERE account_id = ? AND REPLACE(REPLACE(message_id, '<', ''), '>', '') = ?
		 LIMIT 1`,
		accountID, normalizeMessageID(messageID),
	).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, nil
}

// DeleteByUID deletes a message by folder ID and UID
func (s *Store) DeleteByUID(folderID string, uid uint32) error {
	_, err := s.db.Exec("DELETE FROM messages WHERE folder_id = ? AND uid = ?", folderID, uid)
	if err != nil {
		return fmt.Errorf("failed to delete message: %w", err)
	}
	return nil
}

// DeleteByFolder deletes all messages in a folder
func (s *Store) DeleteByFolder(folderID string) error {
	_, err := s.db.Exec("DELETE FROM messages WHERE folder_id = ?", folderID)
	if err != nil {
		return fmt.Errorf("failed to delete messages: %w", err)
	}
	return nil
}

// ExistsInFolder checks if a message with the given RFC 822 Message-ID exists
// in a folder of the specified type (e.g., "trash", "spam") for the account.
// The comparison ignores surrounding angle brackets: rows stored by the
// header-recovery path (and the IMAP envelope path, via go-message) carry no
// brackets, while locally created rows (e.g. sent mail, `<uuid@daak>`) and
// legacy rows do, so an exact match would miss the other form.
func (s *Store) ExistsInFolder(messageID string, folderType string, accountID string) (bool, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM messages m
		JOIN folders f ON m.folder_id = f.id
		WHERE REPLACE(REPLACE(m.message_id, '<', ''), '>', '') = ? AND f.folder_type = ? AND m.account_id = ?
	`, normalizeMessageID(messageID), folderType, accountID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check message in folder type: %w", err)
	}
	return count > 0, nil
}

// HasCopiesInOtherFolders checks if a message with the same RFC 822 Message-ID
// exists in any other folder (excluding the specified folder) for the account.
// Used by Gmail-aware permanent delete to avoid destroying the underlying message
// when it's still visible in other labels. The comparison ignores surrounding
// angle brackets, same as ExistsInFolder: stored rows carry both forms.
func (s *Store) HasCopiesInOtherFolders(messageIDHeader string, excludeFolderID string, accountID string) (bool, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM messages
		WHERE REPLACE(REPLACE(message_id, '<', ''), '>', '') = ? AND folder_id != ? AND account_id = ? AND uid > 0
	`, normalizeMessageID(messageIDHeader), excludeFolderID, accountID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check for copies: %w", err)
	}
	return count > 0, nil
}

// GetAllUIDs returns all UIDs for a folder
func (s *Store) GetAllUIDs(folderID string) ([]uint32, error) {
	rows, err := s.db.Query("SELECT uid FROM messages WHERE folder_id = ? AND uid > 0", folderID)
	if err != nil {
		return nil, fmt.Errorf("failed to query UIDs: %w", err)
	}
	defer rows.Close()

	var uids []uint32
	for rows.Next() {
		var uid uint32
		if err := rows.Scan(&uid); err != nil {
			return nil, fmt.Errorf("failed to scan UID: %w", err)
		}
		uids = append(uids, uid)
	}

	return uids, nil
}

// GetHighestUID returns the highest UID in a folder
func (s *Store) GetHighestUID(folderID string) (uint32, error) {
	var uid sql.NullInt64
	err := s.db.QueryRow("SELECT MAX(uid) FROM messages WHERE folder_id = ?", folderID).Scan(&uid)
	if err != nil {
		return 0, fmt.Errorf("failed to get highest UID: %w", err)
	}
	if uid.Valid {
		return uint32(uid.Int64), nil
	}
	return 0, nil
}

// UpdateBody updates the body content of a message and marks it as fetched
// UpdateBody persists a single fetched body. It mirrors UpdateBodiesBatch:
// the raw S/MIME/PGP bodies are stored alongside the text (they are what
// make an encrypted message decryptable on view), verification status is
// NOT cached (computed fresh on each view), and an empty raw body is stored
// as NULL so HasSMIME/HasPGP (computed as raw_body IS NOT NULL) stay
// accurate. Previously this updated only the text columns, so an encrypted
// message opened before the background body fetch ran was marked
// body_fetched=1 with an empty body and its raw body discarded —
// permanently undecryptable.
//
// A body that actually carries usable content (see BodyUpdate.HasUsableContent)
// also clears body_failed: a message flagged after a transient failure whose
// body later arrives — on-demand open of a body_fetched=0 row, or a Force
// Resync — must not keep a stale flag. An empty update leaves the flag
// alone, so the batch pass can't undo a flag markUnresolvedAsFailed just set.
func (s *Store) UpdateBody(u BodyUpdate) error {
	query := `
		UPDATE messages
		SET body_html = ?, body_text = ?, snippet = ?, body_fetched = 1, has_attachments = ?,
		    body_truncated = ?,
		    body_failed = CASE WHEN ? THEN 0 ELSE body_failed END,
		    smime_raw_body = ?, smime_encrypted = ?,
		    pgp_raw_body = ?, pgp_encrypted = ?
		WHERE id = ?
	`
	var smimeRawBody interface{}
	if len(u.SMIMERawBody) > 0 {
		smimeRawBody = u.SMIMERawBody
	}
	var pgpRawBody interface{}
	if len(u.PGPRawBody) > 0 {
		pgpRawBody = u.PGPRawBody
	}
	_, err := s.db.Exec(query,
		nullString(u.BodyHTML), nullString(u.BodyText), nullString(u.Snippet), u.HasAttachments,
		u.Truncated,
		u.HasUsableContent(),
		smimeRawBody, u.SMIMEEncrypted,
		pgpRawBody, u.PGPEncrypted,
		u.MessageID)
	if err != nil {
		return fmt.Errorf("failed to update body: %w", err)
	}
	return nil
}

// GetMessagesWithoutBody returns message IDs that don't have their body fetched yet,
// or have body_fetched=1 but empty body content (self-healing for failed parses).
// Attachment-only messages are NOT candidates: their fetch succeeded (the
// attachments were extracted), so re-fetching them every sync would loop
// forever — they are complete without body text, not failed.
// If sinceDate is not zero, only returns messages dated on or after that date.
func (s *Store) GetMessagesWithoutBody(folderID string, limit int, sinceDate time.Time) ([]string, error) {
	var query string
	var rows *sql.Rows
	var err error

	// Include messages where body_fetched=0 OR body was fetched but is empty (needs re-fetch)
	// Exclude encrypted messages which intentionally have empty body (decrypted on-view)
	if sinceDate.IsZero() {
		query = `
			SELECT id FROM messages
			WHERE folder_id = ? AND is_hidden = 0 AND (
				body_fetched = 0 OR
				(body_fetched = 1 AND smime_encrypted = 0 AND pgp_encrypted = 0 AND has_attachments = 0 AND (body_text IS NULL OR body_text = '') AND (body_html IS NULL OR body_html = ''))
			) AND body_failed = 0
			ORDER BY date DESC
			LIMIT ?
		`
		rows, err = s.db.Query(query, folderID, limit)
	} else {
		query = `
			SELECT id FROM messages
			WHERE folder_id = ? AND is_hidden = 0 AND (
				body_fetched = 0 OR
				(body_fetched = 1 AND smime_encrypted = 0 AND pgp_encrypted = 0 AND has_attachments = 0 AND (body_text IS NULL OR body_text = '') AND (body_html IS NULL OR body_html = ''))
			) AND body_failed = 0 AND (date >= ? OR date < '1970-01-01')
			ORDER BY date DESC
			LIMIT ?
		`
		rows, err = s.db.Query(query, folderID, sinceDate, limit)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to query messages without body: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan message id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// MessageWithSize holds a message ID and its RFC822 size for batch planning
type MessageWithSize struct {
	ID   string
	Size int
}

// GetMessagesWithoutBodyAndSize returns message IDs and sizes that don't have their body fetched yet,
// ordered by date descending (newest first). Used for byte-aware batch planning.
// If sinceDate is not zero, only returns messages dated on or after that date.
// Attachment-only messages are NOT candidates (see GetMessagesWithoutBody).
func (s *Store) GetMessagesWithoutBodyAndSize(folderID string, limit int, sinceDate time.Time) ([]MessageWithSize, error) {
	var query string
	var rows *sql.Rows
	var err error

	// Include messages where body_fetched=0 OR body was fetched but is empty (needs re-fetch)
	// Exclude encrypted messages which intentionally have empty body (decrypted on-view)
	if sinceDate.IsZero() {
		query = `
			SELECT id, size FROM messages
			WHERE folder_id = ? AND is_hidden = 0 AND (
				body_fetched = 0 OR
				(body_fetched = 1 AND smime_encrypted = 0 AND pgp_encrypted = 0 AND has_attachments = 0 AND (body_text IS NULL OR body_text = '') AND (body_html IS NULL OR body_html = ''))
			) AND body_failed = 0
			ORDER BY date DESC
			LIMIT ?
		`
		rows, err = s.db.Query(query, folderID, limit)
	} else {
		query = `
			SELECT id, size FROM messages
			WHERE folder_id = ? AND is_hidden = 0 AND (
				body_fetched = 0 OR
				(body_fetched = 1 AND smime_encrypted = 0 AND pgp_encrypted = 0 AND has_attachments = 0 AND (body_text IS NULL OR body_text = '') AND (body_html IS NULL OR body_html = ''))
			) AND body_failed = 0 AND (date >= ? OR date < '1970-01-01')
			ORDER BY date DESC
			LIMIT ?
		`
		rows, err = s.db.Query(query, folderID, sinceDate, limit)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to query messages without body: %w", err)
	}
	defer rows.Close()

	var messages []MessageWithSize
	for rows.Next() {
		var msg MessageWithSize
		if err := rows.Scan(&msg.ID, &msg.Size); err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

// CountMessagesWithoutBody returns the count of messages that don't have their body fetched,
// or have body_fetched=1 but empty body content (self-healing for failed parses).
// Attachment-only messages are NOT counted (see GetMessagesWithoutBody).
// If sinceDate is not zero, only counts messages dated on or after that date.
func (s *Store) CountMessagesWithoutBody(folderID string, sinceDate time.Time) (int, error) {
	var count int
	var err error

	// Include messages where body_fetched=0 OR body was fetched but is empty (needs re-fetch)
	// Exclude encrypted messages which intentionally have empty body (decrypted on-view)
	if sinceDate.IsZero() {
		err = s.db.QueryRow(
			`SELECT COUNT(*) FROM messages WHERE folder_id = ? AND is_hidden = 0 AND (
				body_fetched = 0 OR
				(body_fetched = 1 AND smime_encrypted = 0 AND pgp_encrypted = 0 AND has_attachments = 0 AND (body_text IS NULL OR body_text = '') AND (body_html IS NULL OR body_html = ''))
			) AND body_failed = 0`,
			folderID,
		).Scan(&count)
	} else {
		err = s.db.QueryRow(
			`SELECT COUNT(*) FROM messages WHERE folder_id = ? AND is_hidden = 0 AND (
				body_fetched = 0 OR
				(body_fetched = 1 AND smime_encrypted = 0 AND pgp_encrypted = 0 AND has_attachments = 0 AND (body_text IS NULL OR body_text = '') AND (body_html IS NULL OR body_html = ''))
			) AND body_failed = 0 AND (date >= ? OR date < '1970-01-01')`,
			folderID, sinceDate,
		).Scan(&count)
	}

	if err != nil {
		return 0, fmt.Errorf("failed to count messages without body: %w", err)
	}
	return count, nil
}

// CountByFolder returns the total message count for a folder
func (s *Store) CountByFolder(folderID string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM messages WHERE folder_id = ? AND is_hidden = 0", folderID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count messages: %w", err)
	}
	return count, nil
}

// DeleteOlderThan deletes messages older than the specified time for an account
// Returns the number of messages deleted
// DeleteOlderThan deletes messages in one folder whose first-seen time is
// older than the given cutoff (the sync-period purge). The retention clock
// is received_at — when Daak first saw the message, which for promptly
// synced mail is effectively the arrival time — NOT the header Date: a
// missing/invalid Date stores as zero (below every cutoff) and an old Date
// on a recently arrived message would otherwise be deleted on every sync
// and re-fetched forever (bodies re-downloaded, unread counts flickering,
// the message never settling). Rows with a zero/NULL received_at are never
// purged here, and the purge is scoped to the folder being synced, so old
// mail in other folders is untouched.
func (s *Store) DeleteOlderThan(accountID, folderID string, before time.Time) (int, error) {
	result, err := s.db.Exec(
		`DELETE FROM messages
		 WHERE account_id = ? AND folder_id = ?
		   AND received_at IS NOT NULL AND received_at > '1970-01-01'
		   AND received_at < ?`,
		accountID, folderID, before,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to delete old messages: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get affected rows: %w", err)
	}

	if affected > 0 {
		s.log.Info().
			Str("accountID", accountID).
			Time("before", before).
			Int64("deleted", affected).
			Msg("Deleted old messages based on sync period")
	}

	return int(affected), nil
}

// GetMessageUIDAndFolder returns the UID and folder_id for a message
func (s *Store) GetMessageUIDAndFolder(messageID string) (uint32, string, error) {
	var uidI64 int64
	var folderID string
	err := s.db.QueryRow(
		"SELECT uid, folder_id FROM messages WHERE id = ?",
		messageID,
	).Scan(&uidI64, &folderID)
	if err == sql.ErrNoRows {
		return 0, "", fmt.Errorf("message not found: %s", messageID)
	}
	if err != nil {
		return 0, "", fmt.Errorf("failed to get message: %w", err)
	}
	// A just-moved message carries a negative placeholder UID (uid = -rowid,
	// written by MoveMessages until the destination sync fetches the real
	// UID and DeleteTempUIDs cleans the row up). Converting it to uint32
	// would yield a huge bogus UID that the server can never return, and a
	// fetch under it returns nothing — which the old body-fetch code then
	// treated as a "ghost" and deleted. Refuse instead: callers leave the
	// row alone and the destination sync resolves it.
	if uidI64 <= 0 {
		return 0, "", fmt.Errorf("message has no server UID yet (placeholder UID): %s", messageID)
	}
	return uint32(uidI64), folderID, nil
}

// UIDInfo holds UID and folder information for a message
type UIDInfo struct {
	UID      uint32
	FolderID string
}

// GetMessageUIDsAndFolder returns UIDs and folder_ids for multiple messages in one query
func (s *Store) GetMessageUIDsAndFolder(messageIDs []string) (map[string]UIDInfo, error) {
	if len(messageIDs) == 0 {
		return make(map[string]UIDInfo), nil
	}

	// Build placeholders for IN clause
	placeholders := make([]string, len(messageIDs))
	args := make([]interface{}, len(messageIDs))
	for i, id := range messageIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(
		"SELECT id, uid, folder_id FROM messages WHERE id IN (%s)",
		strings.Join(placeholders, ", "),
	)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query message UIDs: %w", err)
	}
	defer rows.Close()

	result := make(map[string]UIDInfo)
	for rows.Next() {
		var id string
		var uidI64 int64
		var folderID string
		if err := rows.Scan(&id, &uidI64, &folderID); err != nil {
			return nil, fmt.Errorf("failed to scan message UID: %w", err)
		}
		// Mirror the singular lookup: a just-moved message carries a
		// negative placeholder UID (uid = -rowid, written by MoveMessages
		// until the destination sync fetches the real UID), and uid 0 is
		// no server UID at all. Converting either unchecked to uint32
		// would yield a huge bogus UID the server can never return, and
		// the batch fetch would then charge the ID body_failed. Skip the
		// row instead: the destination sync resolves the real UID, and
		// the caller must not pass the ID on as a fetchable request.
		if uidI64 <= 0 {
			continue
		}
		result[id] = UIDInfo{UID: uint32(uidI64), FolderID: folderID}
	}

	return result, nil
}

// BodyUpdate holds body content for batch updates
type BodyUpdate struct {
	MessageID      string
	BodyHTML       string
	BodyText       string
	Snippet        string
	HasAttachments bool
	// Truncated records that the stored body is only the first part of
	// the message: a part hit the 10 MB cap or the whole message hit the
	// 50 MB cap during fetch/parse (audit finding 26). Persisted as
	// body_truncated so the viewer can disclose it; a re-fetch would hit
	// the same cap, so this is not a retry gate.
	Truncated bool
	// Enrichment, when set, is persisted alongside the body by
	// UpdateBodiesBatch (the body pass is where extraction runs).
	Enrichment         *enrich.Result
	SMIMEStatus        string
	SMIMESignerEmail   string
	SMIMESignerSubject string
	SMIMERawBody       []byte
	SMIMEEncrypted     bool
	PGPRawBody         []byte
	PGPEncrypted       bool
}

// HasUsableContent reports whether a fetched body carries anything worth
// keeping: body text/HTML, an encrypted payload (decryptable on view), or
// attachments. This is the shared "resolved" definition for the body_failed
// flag: markUnresolvedAsFailed (internal/sync) and the flag-clearing in
// UpdateBody/UpdateBodiesBatch must agree, or a message flips between
// flagged and cleared on every sync. In particular an attachment-only
// message is resolved — it legitimately has no body text, and flagging it
// would leave it permanently body-less for no reason.
func (u BodyUpdate) HasUsableContent() bool {
	return u.BodyHTML != "" || u.BodyText != "" || u.SMIMEEncrypted || u.PGPEncrypted || u.HasAttachments
}

// UpdateEnrichment persists a sync-time enrichment result for one message.
// The extracted value is stored as-is; callers must never log it.
func (s *Store) UpdateEnrichment(id string, r enrich.Result) error {
	_, err := s.db.Exec(`
		UPDATE messages SET
			list_unsubscribe = ?, list_unsubscribe_post = ?,
			category = ?, sub_category = ?,
			extract_kind = ?, extract_value = ?, extract_rule = ?
		WHERE id = ?
	`, nullString(r.ListUnsubscribe), r.ListUnsubscribePost,
		nullString(r.Category), nullString(r.SubCategory),
		nullString(r.ExtractKind), nullString(r.ExtractValue), nullString(r.ExtractRule),
		id)
	if err != nil {
		return fmt.Errorf("failed to update enrichment: %w", err)
	}
	return nil
}

// ListUnenriched returns messages whose category has never been computed
// (category IS NULL), oldest first, for the one-off backfill. The body
// head is capped at 2000 chars; the caller enriches and calls
// UpdateEnrichment. The stored List-Unsubscribe columns ride along so
// the backfill preserves them (raw headers are not stored, so it cannot
// recover them for already-synced mail — those fill on next full sync).
func (s *Store) ListUnenriched(limit int) ([]*Message, error) {
	rows, err := s.db.Query(`
		SELECT id, account_id, folder_id, uid, subject, from_name, from_email,
		       date, snippet, substr(body_text, 1, 2000),
		       list_unsubscribe, list_unsubscribe_post
		FROM messages WHERE category IS NULL AND is_hidden = 0
		ORDER BY date ASC LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query unenriched messages: %w", err)
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m := &Message{}
		var dateStr, snippet, bodyText sql.NullString
		var listUnsubscribe sql.NullString
		var uidI64 int64
		if err := rows.Scan(&m.ID, &m.AccountID, &m.FolderID, &uidI64,
			&m.Subject, &m.FromName, &m.FromEmail, &dateStr, &snippet, &bodyText,
			&listUnsubscribe, &m.ListUnsubscribePost); err != nil {
			return nil, fmt.Errorf("failed to scan unenriched message: %w", err)
		}
		m.UID = uint32(uidI64)
		if dateStr.Valid && dateStr.String != "" {
			m.Date = parseTimeString(dateStr.String)
		}
		if snippet.Valid {
			m.Snippet = snippet.String
		}
		if bodyText.Valid {
			m.BodyText = bodyText.String
		}
		if listUnsubscribe.Valid {
			m.ListUnsubscribe = listUnsubscribe.String
		}
		out = append(out, m)
	}
	return out, nil
}

// CountUnenriched returns how many messages still need backfill enrichment.
func (s *Store) CountUnenriched() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM messages WHERE category IS NULL AND is_hidden = 0").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count unenriched messages: %w", err)
	}
	return count, nil
}

// EnrichmentHeader is the stored header data the body-time enrichment
// pass needs (no body fetch required).
type EnrichmentHeader struct {
	FromEmail           string
	FromName            string
	Subject             string
	ListUnsubscribe     string
	ListUnsubscribePost bool
	Category            string
}

// GetHeaderForEnrichment loads the stored header for one message (nil,
// nil when the row is gone).
func (s *Store) GetHeaderForEnrichment(id string) (*EnrichmentHeader, error) {
	h := &EnrichmentHeader{}
	var lu, category sql.NullString
	err := s.db.QueryRow(
		`SELECT from_email, from_name, subject, list_unsubscribe,
		        list_unsubscribe_post, category FROM messages WHERE id = ?`,
		id,
	).Scan(&h.FromEmail, &h.FromName, &h.Subject, &lu, &h.ListUnsubscribePost, &category)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query enrichment header: %w", err)
	}
	if lu.Valid {
		h.ListUnsubscribe = lu.String
	}
	if category.Valid {
		h.Category = category.String
	}
	return h, nil
}

// StoredEnrichment is the enrichment row already stored for a message
// (used to preserve body-time extraction across header re-syncs).
type StoredEnrichment struct {
	ListUnsubscribe     string
	ListUnsubscribePost bool
	Category            string
	SubCategory         string
	ExtractKind         string
	ExtractValue        string
	ExtractRule         string
}

// GetStoredEnrichment loads the enrichment already stored for the message
// at (folderID, uid), or (nil, nil) when there is no row yet.
func (s *Store) GetStoredEnrichment(folderID string, uid uint32) (*StoredEnrichment, error) {
	st := &StoredEnrichment{}
	var lu, category, subCategory, extractKind, extractValue, extractRule sql.NullString
	err := s.db.QueryRow(
		`SELECT list_unsubscribe, list_unsubscribe_post, category, sub_category,
		        extract_kind, extract_value, extract_rule
		 FROM messages WHERE folder_id = ? AND uid = ?`,
		folderID, uid,
	).Scan(&lu, &st.ListUnsubscribePost, &category, &subCategory,
		&extractKind, &extractValue, &extractRule)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query stored enrichment: %w", err)
	}
	if lu.Valid {
		st.ListUnsubscribe = lu.String
	}
	if category.Valid {
		st.Category = category.String
	}
	if subCategory.Valid {
		st.SubCategory = subCategory.String
	}
	if extractKind.Valid {
		st.ExtractKind = extractKind.String
	}
	if extractValue.Valid {
		st.ExtractValue = extractValue.String
	}
	if extractRule.Valid {
		st.ExtractRule = extractRule.String
	}
	return st, nil
}

// ListSenderCategoryOverrides returns every classifier override as
// lowercased sender -> category.
func (s *Store) ListSenderCategoryOverrides() (map[string]string, error) {
	rows, err := s.db.Query("SELECT sender_address, category FROM sender_category_overrides")
	if err != nil {
		return nil, fmt.Errorf("failed to query sender overrides: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var addr, cat string
		if err := rows.Scan(&addr, &cat); err != nil {
			return nil, fmt.Errorf("failed to scan sender override: %w", err)
		}
		out[strings.ToLower(strings.TrimSpace(addr))] = cat
	}
	return out, nil
}

// GetSenderCategoryOverride returns the classifier override for a sender
// (lowercased match), if one exists.
func (s *Store) GetSenderCategoryOverride(email string) (string, bool, error) {
	var category string
	err := s.db.QueryRow(
		"SELECT category FROM sender_category_overrides WHERE sender_address = ?",
		strings.ToLower(strings.TrimSpace(email)),
	).Scan(&category)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("failed to query sender override: %w", err)
	}
	return category, true, nil
}

// SetSenderCategoryOverride records the user's "Always treat mail from
// this sender as ..." choice (Phase E, rule 1 of the classifier). The
// address is stored lower-cased; a later call for the same sender
// replaces the row.
func (s *Store) SetSenderCategoryOverride(email, category string) error {
	switch category {
	case enrich.CategoryPersonal, enrich.CategoryAutomated:
	default:
		return fmt.Errorf("invalid category: %s (must be 'personal' or 'automated')", category)
	}
	_, err := s.db.Exec(
		"INSERT INTO sender_category_overrides (sender_address, category) VALUES (?, ?) "+
			"ON CONFLICT(sender_address) DO UPDATE SET category = excluded.category, created_at = CURRENT_TIMESTAMP",
		strings.ToLower(strings.TrimSpace(email)), category,
	)
	if err != nil {
		return fmt.Errorf("failed to set sender override: %w", err)
	}
	return nil
}

// DeleteSenderCategoryOverride removes a sender override. Future mail
// from the sender is classified by the heuristics again; mail already
// stored keeps its stored category until it is re-enriched.
func (s *Store) DeleteSenderCategoryOverride(email string) error {
	_, err := s.db.Exec(
		"DELETE FROM sender_category_overrides WHERE sender_address = ?",
		strings.ToLower(strings.TrimSpace(email)),
	)
	if err != nil {
		return fmt.Errorf("failed to delete sender override: %w", err)
	}
	return nil
}

// ApplySenderCategoryOverride re-files every stored message from the
// sender under the override category, so the "wrong tab" correction
// takes effect on mail already in the list, not just on future syncs.
// A Personal override also clears the sub-category and any extracted
// code/reference (extraction only exists for Automated mail); an
// Automated override keeps the stored sub-category, if the classifier
// already computed one. Returns the number of messages re-filed.
func (s *Store) ApplySenderCategoryOverride(email, category string) (int64, error) {
	switch category {
	case enrich.CategoryPersonal, enrich.CategoryAutomated:
	default:
		return 0, fmt.Errorf("invalid category: %s (must be 'personal' or 'automated')", category)
	}
	addr := strings.ToLower(strings.TrimSpace(email))
	var res sql.Result
	var err error
	if category == enrich.CategoryPersonal {
		res, err = s.db.Exec(
			`UPDATE messages SET category = ?, sub_category = NULL,
				extract_kind = NULL, extract_value = NULL, extract_rule = NULL
				WHERE lower(from_email) = ?`,
			category, addr,
		)
	} else {
		res, err = s.db.Exec(
			`UPDATE messages SET category = ? WHERE lower(from_email) = ?`,
			category, addr,
		)
	}
	if err != nil {
		return 0, fmt.Errorf("failed to apply sender override: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to count re-filed messages: %w", err)
	}
	return n, nil
}

// IsKnownPersonalSender reports whether the sender is in contacts and has
// emailed as a person before (a collected contact with send history).
func (s *Store) IsKnownPersonalSender(email string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM contact_emails WHERE email = ? AND send_count > 0",
		strings.ToLower(strings.TrimSpace(email)),
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to query known sender: %w", err)
	}
	return count > 0, nil
}

// --- Unsubscribe (Phase C) -------------------------------------------------
//
// Sender addresses are normalised to lower-case on write and on read, so
// "News@Example.com" and "news@example.com" are one sender everywhere.

// completedUnsubscribeMethods are the unsubscribe_actions methods that
// mean the unsubscribe actually happened. A "browser" action only means
// the sender's page was opened — the user may or may not have finished
// there, so the button keeps being offered.
var completedUnsubscribeMethods = map[string]bool{
	"one_click": true,
	"mailto":    true,
}

// RecordUnsubscribeAction records one unsubscribe the user acted on, so
// a re-sync never offers it again and the Subscriptions view can show
// it. Re-recording the same sender+account updates the method and time.
func (s *Store) RecordUnsubscribeAction(senderAddress, accountID, method string) error {
	_, err := s.db.Exec(`
		INSERT INTO unsubscribe_actions (sender_address, account_id, method, acted_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(sender_address, account_id) DO UPDATE SET
			method = excluded.method, acted_at = excluded.acted_at
	`, strings.ToLower(strings.TrimSpace(senderAddress)), accountID, method)
	if err != nil {
		return fmt.Errorf("failed to record unsubscribe action: %w", err)
	}
	return nil
}

// UnsubscribeActionMethod returns the recorded method for a sender on an
// account, if the user has ever acted on that sender.
func (s *Store) UnsubscribeActionMethod(senderAddress, accountID string) (string, bool, error) {
	var method string
	err := s.db.QueryRow(
		"SELECT method FROM unsubscribe_actions WHERE sender_address = ? AND account_id = ?",
		strings.ToLower(strings.TrimSpace(senderAddress)), accountID,
	).Scan(&method)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("failed to query unsubscribe action: %w", err)
	}
	return method, true, nil
}

// HasUnsubscribed reports whether the sender was already unsubscribed
// from by a completed method (one-click POST sent, or unsubscribe email
// sent). A browser-only action does not count.
func (s *Store) HasUnsubscribed(senderAddress, accountID string) (bool, error) {
	method, ok, err := s.UnsubscribeActionMethod(senderAddress, accountID)
	if err != nil {
		return false, err
	}
	return ok && completedUnsubscribeMethods[method], nil
}

// SetSenderRule stores a sender rule (Phase C: action "auto_trash" moves
// future mail from the sender to Trash at sync time). Re-setting the
// same rule is a no-op.
func (s *Store) SetSenderRule(senderAddress, accountID, action string) error {
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO sender_rules (sender_address, account_id, action)
		VALUES (?, ?, ?)
	`, strings.ToLower(strings.TrimSpace(senderAddress)), accountID, action)
	if err != nil {
		return fmt.Errorf("failed to set sender rule: %w", err)
	}
	return nil
}

// DeleteSenderRule removes a sender rule.
func (s *Store) DeleteSenderRule(senderAddress, accountID, action string) error {
	_, err := s.db.Exec(`
		DELETE FROM sender_rules WHERE sender_address = ? AND account_id = ? AND action = ?
	`, strings.ToLower(strings.TrimSpace(senderAddress)), accountID, action)
	if err != nil {
		return fmt.Errorf("failed to delete sender rule: %w", err)
	}
	return nil
}

// HasSenderRule reports whether a sender rule exists.
func (s *Store) HasSenderRule(senderAddress, accountID, action string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM sender_rules WHERE sender_address = ? AND account_id = ? AND action = ?",
		strings.ToLower(strings.TrimSpace(senderAddress)), accountID, action,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to query sender rule: %w", err)
	}
	return count > 0, nil
}

// ListSenderRuleSenders returns the lower-cased sender addresses with a
// given rule action on an account, for sync-time application.
func (s *Store) ListSenderRuleSenders(accountID, action string) ([]string, error) {
	rows, err := s.db.Query(
		"SELECT sender_address FROM sender_rules WHERE account_id = ? AND action = ?",
		accountID, action,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query sender rules: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err != nil {
			return nil, fmt.Errorf("failed to scan sender rule: %w", err)
		}
		out = append(out, addr)
	}
	return out, nil
}

// SubscriptionSender is one row of the Settings → Subscriptions view:
// every sender who ever sent List-Unsubscribe headers, with volume.
type SubscriptionSender struct {
	AccountID       string
	SenderEmail     string
	SenderName      string
	CountAll        int
	Count30Days     int
	LastReceived    time.Time
	OneClick        bool
	ListUnsubscribe string // raw header from the newest message, for the mismatch check
}

// ListSubscriptionSenders returns every sender with List-Unsubscribe
// headers, most messages first. The 30-day count uses the same stored
// date format the store writes, so the comparison is chronological.
func (s *Store) ListSubscriptionSenders() ([]SubscriptionSender, error) {
	cutoff := time.Now().AddDate(0, 0, -30)
	rows, err := s.db.Query(`
		SELECT account_id,
		       lower(from_email) AS email,
		       (SELECT m2.from_name FROM messages m2
		         WHERE m2.account_id = messages.account_id
		           AND lower(m2.from_email) = lower(messages.from_email)
		         ORDER BY m2.date DESC LIMIT 1) AS name,
		       COUNT(*) AS total,
		       SUM(CASE WHEN date >= ? THEN 1 ELSE 0 END) AS recent,
		       MAX(date) AS last_received,
		       MAX(list_unsubscribe_post) AS one_click,
		       (SELECT m2.list_unsubscribe FROM messages m2
		         WHERE m2.account_id = messages.account_id
		           AND lower(m2.from_email) = lower(messages.from_email)
		         ORDER BY m2.date DESC LIMIT 1) AS lu
		FROM messages
		WHERE list_unsubscribe IS NOT NULL AND trim(list_unsubscribe) != ''
		GROUP BY account_id, lower(from_email)
		ORDER BY total DESC
	`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("failed to query subscription senders: %w", err)
	}
	defer rows.Close()
	var out []SubscriptionSender
	for rows.Next() {
		var row SubscriptionSender
		var name, lastReceived sql.NullString
		var lu sql.NullString
		if err := rows.Scan(&row.AccountID, &row.SenderEmail, &name, &row.CountAll,
			&row.Count30Days, &lastReceived, &row.OneClick, &lu); err != nil {
			return nil, fmt.Errorf("failed to scan subscription sender: %w", err)
		}
		if name.Valid {
			row.SenderName = name.String
		}
		if lastReceived.Valid && lastReceived.String != "" {
			row.LastReceived = parseTimeString(lastReceived.String)
		}
		if lu.Valid {
			row.ListUnsubscribe = lu.String
		}
		out = append(out, row)
	}
	return out, nil
}

// GetLatestMessageFromSender returns the newest message from one sender
// (case-insensitive) that carries List-Unsubscribe headers, or nil when
// the sender has none. Used by the Subscriptions view's Unsubscribe
// button, which has no message id of its own.
func (s *Store) GetLatestMessageFromSender(accountID, senderEmail string) (*Message, error) {
	rows, err := s.db.Query(`
		SELECT id FROM messages
		WHERE account_id = ? AND lower(from_email) = ?
		  AND list_unsubscribe IS NOT NULL AND trim(list_unsubscribe) != ''
		ORDER BY date DESC LIMIT 1
	`, accountID, strings.ToLower(strings.TrimSpace(senderEmail)))
	if err != nil {
		return nil, fmt.Errorf("failed to query sender messages: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	var id string
	if err := rows.Scan(&id); err != nil {
		return nil, fmt.Errorf("failed to scan sender message: %w", err)
	}
	return s.Get(id)
}

// ListMessageIDsFromSenderInFolder returns the internal ids of messages
// from one sender (case-insensitive) currently in one folder, oldest
// first. Used by the unsubscribe follow-up ("move existing mail to
// Trash") and by sync-time sender rules.
func (s *Store) ListMessageIDsFromSenderInFolder(accountID, folderID, senderEmail string) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT id FROM messages
		WHERE account_id = ? AND folder_id = ? AND lower(from_email) = ?
		ORDER BY date ASC
	`, accountID, folderID, strings.ToLower(strings.TrimSpace(senderEmail)))
	if err != nil {
		return nil, fmt.Errorf("failed to query sender messages: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan sender message: %w", err)
		}
		out = append(out, id)
	}
	return out, nil
}

// AttentionCandidate is one inbox message that may earn a Needs attention
// bar. Eligibility + expiry are derived at read time (enrich package);
// this is just the row data the derivation needs.
type AttentionCandidate struct {
	Message      *Message
	FolderID     string
	FolderType   string
	AccountID    string
	AccountName  string
	AccountColor string
}

// ListAttentionCandidates returns recent inbox messages that could qualify
// for a Needs attention bar: not hidden, in an inbox folder (or one of the
// given folder IDs), within the last 14 days, and carrying either an
// extracted code or a Travel/Transactions/Shipping sub-category. The
// caller runs enrich.EvaluateAttention per row and filters dismissals.
func (s *Store) ListAttentionCandidates(folderIDs []string, since time.Time) ([]*AttentionCandidate, error) {
	args := []interface{}{since}
	folderFilter := ""
	if len(folderIDs) > 0 {
		placeholders := make([]string, len(folderIDs))
		for i, id := range folderIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		folderFilter = " AND m.folder_id IN (" + strings.Join(placeholders, ", ") + ")"
	}
	query := `
		SELECT m.id, m.account_id, m.folder_id, m.uid, m.message_id,
		       m.subject, m.from_name, m.from_email, m.date,
		       m.snippet, substr(m.body_text, 1, 2000),
		       m.is_read, m.category, m.sub_category,
		       m.extract_kind, m.extract_value, m.extract_rule,
		       f.folder_type, a.name, a.color
		FROM messages m
		INNER JOIN folders f ON m.folder_id = f.id AND f.folder_type = 'inbox'
		INNER JOIN accounts a ON m.account_id = a.id AND a.enabled = 1
		WHERE m.is_hidden = 0 AND m.date >= ?` + folderFilter + `
		  AND (m.extract_kind = 'code' OR m.sub_category IN ('Travel', 'Transactions', 'Shipping'))
		ORDER BY m.date DESC
	`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query attention candidates: %w", err)
	}
	defer rows.Close()
	var out []*AttentionCandidate
	for rows.Next() {
		m := &Message{}
		c := &AttentionCandidate{Message: m}
		var dateStr, snippet, bodyText sql.NullString
		var messageID sql.NullString
		var accountName, accountColor sql.NullString
		var category, subCategory, extractKind, extractValue, extractRule sql.NullString
		var uidI64 int64
		if err := rows.Scan(
			&m.ID, &m.AccountID, &m.FolderID, &uidI64, &messageID,
			&m.Subject, &m.FromName, &m.FromEmail, &dateStr,
			&snippet, &bodyText,
			&m.IsRead, &category, &subCategory,
			&extractKind, &extractValue, &extractRule,
			&c.FolderType, &accountName, &accountColor,
		); err != nil {
			return nil, fmt.Errorf("failed to scan attention candidate: %w", err)
		}
		m.UID = uint32(uidI64)
		c.FolderID = m.FolderID
		c.AccountID = m.AccountID
		if messageID.Valid {
			m.MessageID = messageID.String
		}
		if dateStr.Valid && dateStr.String != "" {
			m.Date = parseTimeString(dateStr.String)
		}
		if snippet.Valid {
			m.Snippet = snippet.String
		}
		if bodyText.Valid {
			m.BodyText = bodyText.String
		}
		// category / sub_category / extract_* are all nullable: a row
		// without an extraction must not fail the whole scan.
		if category.Valid {
			m.Category = category.String
		}
		if subCategory.Valid {
			m.SubCategory = subCategory.String
		}
		if extractKind.Valid {
			m.ExtractKind = extractKind.String
		}
		if extractValue.Valid {
			m.ExtractValue = extractValue.String
		}
		if extractRule.Valid {
			m.ExtractRule = extractRule.String
		}
		if accountName.Valid {
			c.AccountName = accountName.String
		}
		if accountColor.Valid {
			c.AccountColor = accountColor.String
		}
		out = append(out, c)
	}
	return out, nil
}

// DismissAttention records that a message's Needs attention bar was
// dismissed. The mail itself is untouched; the dismissal survives sync
// and restart, and the bar never resurfaces for that message.
func (s *Store) DismissAttention(messageID string) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO attention_dismissals (message_id, dismissed_at) VALUES (?, CURRENT_TIMESTAMP)",
		messageID,
	)
	if err != nil {
		return fmt.Errorf("failed to dismiss attention: %w", err)
	}
	return nil
}

// GetAttentionDismissalIDs returns every dismissed message ID.
func (s *Store) GetAttentionDismissalIDs() (map[string]bool, error) {
	rows, err := s.db.Query("SELECT message_id FROM attention_dismissals")
	if err != nil {
		return nil, fmt.Errorf("failed to query attention dismissals: %w", err)
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan attention dismissal: %w", err)
		}
		out[id] = true
	}
	return out, nil
}

// IsAttentionDismissed reports whether a message's bar was dismissed.
func (s *Store) IsAttentionDismissed(messageID string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM attention_dismissals WHERE message_id = ?",
		messageID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to query attention dismissal: %w", err)
	}
	return count > 0, nil
}

// UpdateBodiesBatch updates body content for multiple messages in a single transaction.
// An update carrying usable content (see BodyUpdate.HasUsableContent) also
// clears body_failed for that row; an empty update leaves the flag alone.
func (s *Store) UpdateBodiesBatch(updates []BodyUpdate) error {
	if len(updates) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`
		UPDATE messages
		SET body_html = ?, body_text = ?, snippet = ?, body_fetched = 1,
		    has_attachments = ?,
		    body_truncated = ?,
		    body_failed = CASE WHEN ? THEN 0 ELSE body_failed END,
		    smime_status = ?, smime_signer_email = ?, smime_signer_subject = ?,
		    smime_raw_body = ?, smime_encrypted = ?,
		    pgp_raw_body = ?, pgp_encrypted = ?
		WHERE id = ?
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	enrichStmt, err := tx.Prepare(`
		UPDATE messages
		SET list_unsubscribe = ?, list_unsubscribe_post = ?,
		    category = ?, sub_category = ?,
		    extract_kind = ?, extract_value = ?, extract_rule = ?
		WHERE id = ?
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare enrichment statement: %w", err)
	}
	defer enrichStmt.Close()

	for _, u := range updates {
		var smimeRawBody interface{}
		if len(u.SMIMERawBody) > 0 {
			smimeRawBody = u.SMIMERawBody
		}
		var pgpRawBody interface{}
		if len(u.PGPRawBody) > 0 {
			pgpRawBody = u.PGPRawBody
		}
		_, err := stmt.Exec(
			nullString(u.BodyHTML), nullString(u.BodyText), nullString(u.Snippet),
			u.HasAttachments,
			u.Truncated,
			u.HasUsableContent(),
			nullString(u.SMIMEStatus), nullString(u.SMIMESignerEmail), nullString(u.SMIMESignerSubject),
			smimeRawBody, u.SMIMEEncrypted,
			pgpRawBody, u.PGPEncrypted,
			u.MessageID,
		)
		if err != nil {
			s.log.Warn().Err(err).Str("messageID", u.MessageID).Msg("Failed to update body in batch")
			// Continue with other updates
		}
		if u.Enrichment != nil {
			// Never log the extracted value (see internal/enrich).
			if _, err := enrichStmt.Exec(
				nullString(u.Enrichment.ListUnsubscribe), u.Enrichment.ListUnsubscribePost,
				nullString(u.Enrichment.Category), nullString(u.Enrichment.SubCategory),
				nullString(u.Enrichment.ExtractKind), nullString(u.Enrichment.ExtractValue),
				nullString(u.Enrichment.ExtractRule),
				u.MessageID,
			); err != nil {
				s.log.Warn().Err(err).Str("messageID", u.MessageID).Msg("Failed to update enrichment in batch")
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// GetSMIMERawBody returns the raw S/MIME body bytes for a message (for on-view decryption/verification)
func (s *Store) GetSMIMERawBody(messageID string) ([]byte, error) {
	var rawBody []byte
	err := s.db.QueryRow("SELECT smime_raw_body FROM messages WHERE id = ?", messageID).Scan(&rawBody)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get S/MIME raw body: %w", err)
	}
	return rawBody, nil
}

// GetPGPRawBody returns the raw PGP body bytes for a message (for on-view decryption/verification)
func (s *Store) GetPGPRawBody(messageID string) ([]byte, error) {
	var rawBody []byte
	err := s.db.QueryRow("SELECT pgp_raw_body FROM messages WHERE id = ?", messageID).Scan(&rawBody)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get PGP raw body: %w", err)
	}
	return rawBody, nil
}

// ClearBodiesForFolder clears body content for all messages in a folder.
// This resets body_html, body_text, snippet to NULL and body_fetched to 0,
// allowing the messages to be re-fetched and re-parsed during the next body sync.
// It also resets body_failed to 0: a message whose body failed once (transient
// timeout, or a parser that has since improved) must be retried by the
// Force Resync, not skipped forever by the flag — otherwise clearing the
// bodies changes nothing for exactly the messages that need it most.
// body_truncated is reset too: the re-fetch re-measures it from scratch.
func (s *Store) ClearBodiesForFolder(folderID string) (int64, error) {
	query := `
		UPDATE messages
		SET body_html = NULL, body_text = NULL, snippet = NULL, body_fetched = 0, body_failed = 0, body_truncated = 0
		WHERE folder_id = ?
	`
	result, err := s.db.Exec(query, folderID)
	if err != nil {
		return 0, fmt.Errorf("failed to clear bodies for folder: %w", err)
	}

	affected, _ := result.RowsAffected()
	s.log.Info().Str("folderID", folderID).Int64("affected", affected).Msg("Cleared bodies for folder")
	return affected, nil
}

// helper to convert empty string to NULL
func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// parseTimeString parses a time string in various formats
func parseTimeString(s string) time.Time {
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02 15:04:05 -0700 MST", // Format used by Go's time.Time.String() when stored in SQLite
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, format := range formats {
		if parsed, err := time.Parse(format, s); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// ListConversationsByFolder returns conversations (grouped by thread) for a folder with pagination
// sortOrder can be "newest" (default) or "oldest"
func (s *Store) ListConversationsByFolder(folderID string, offset, limit int, sortOrder, filter, category, subCategory string) ([]*Conversation, error) {
	// Determine sort direction
	orderClause := "ORDER BY latest_date DESC"
	if sortOrder == "oldest" {
		orderClause = "ORDER BY latest_date ASC"
	}

	// Phase E: category tab filter ("" = All). A conversation belongs to
	// a tab by its latest message's category.
	categoryClause, categoryArgs := categoryFilterClause(category, subCategory, "m.", "folder_id")

	// Pre-fetch folder type so we can pick the right participants
	// aggregation. Sent and Drafts folders should surface the recipient
	// list ("who you wrote to") instead of the sender (always self).
	// Mirrors the inline folder-type lookup in GetConversation. Lookup
	// errors are intentionally swallowed: an empty folderType falls
	// through to the existing sender-based behavior, so the list never
	// breaks on a metadata hiccup.
	var folderType string
	_ = s.db.QueryRow("SELECT folder_type FROM folders WHERE id = ?", folderID).Scan(&folderType)
	useToList := folderType == "sent" || folderType == "drafts"

	// participantsExpr carries date+snippet per sender so Go can order
	// participants latest-first and pick the latest message's snippet
	// (#169). The sent/drafts branch aggregates per-message to_list JSON
	// arrays into a nested array that parseAggregatedToListJSON flattens
	// + dedupes in Go (DISTINCT doesn't work across nested-array values
	// in SQLite).
	participantsExpr := `json_group_array(json_object('name', from_name, 'email', from_email, 'date', date, 'snippet', snippet))`
	if useToList {
		participantsExpr = `json_group_array(json(to_list))`
	}

	// Get conversations grouped by thread_id, ordered by date
	// Use GROUP_CONCAT to get all message IDs in a single query
	query := fmt.Sprintf(`
		SELECT
			COALESCE(thread_id, id) as conv_thread_id,
			MIN(subject) as subject,
			MAX(snippet) as snippet,
			COUNT(*) as message_count,
			SUM(CASE WHEN is_read = 0 THEN 1 ELSE 0 END) as unread_count,
			MAX(CASE WHEN has_attachments = 1 THEN 1 ELSE 0 END) as has_attachments,
			MAX(CASE WHEN is_starred = 1 THEN 1 ELSE 0 END) as is_starred,
			MAX(date) as latest_date,
			GROUP_CONCAT(id) as message_ids,
			MAX(CASE WHEN smime_encrypted = 1 OR pgp_encrypted = 1 THEN 1 ELSE 0 END) as is_encrypted,
			(SELECT m2.extract_kind FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as extract_kind,
			(SELECT m2.extract_value FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as extract_value,
			(SELECT m2.extract_rule FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as extract_rule,
			(SELECT m2.id FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as extract_message_id,
			(SELECT m2.category FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as category,
			(SELECT m2.sub_category FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as sub_category,
			%s as participants_json
		FROM messages m
		WHERE folder_id = ? AND is_hidden = 0`+categoryClause+`
		GROUP BY COALESCE(thread_id, id)`+
		filterHavingClause(filter, "")+`
		`+orderClause+`
		LIMIT ? OFFSET ?
	`, participantsExpr)

	args := append(append([]any{folderID}, categoryArgs...), limit, offset)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query conversations: %w", err)
	}
	defer rows.Close()

	var conversations []*Conversation
	for rows.Next() {
		c := &Conversation{}
		var latestDateStr sql.NullString
		var snippet sql.NullString
		var messageIDsStr sql.NullString
		var participantsJSON sql.NullString

		var extractKind, extractValue, extractRule, extractMessageID sql.NullString
		var categoryVal, subCategoryVal sql.NullString
		err := rows.Scan(
			&c.ThreadID,
			&c.Subject,
			&snippet,
			&c.MessageCount,
			&c.UnreadCount,
			&c.HasAttachments,
			&c.IsStarred,
			&latestDateStr,
			&messageIDsStr,
			&c.IsEncrypted,
			&extractKind, &extractValue, &extractRule, &extractMessageID,
			&categoryVal, &subCategoryVal,
			&participantsJSON,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan conversation: %w", err)
		}
		if extractKind.Valid {
			c.ExtractKind = extractKind.String
		}
		if extractValue.Valid {
			c.ExtractValue = extractValue.String
		}
		if extractRule.Valid {
			c.ExtractRule = extractRule.String
		}
		if extractMessageID.Valid {
			c.ExtractMessageID = extractMessageID.String
		}
		if categoryVal.Valid {
			c.Category = categoryVal.String
		}
		if subCategoryVal.Valid {
			c.SubCategory = subCategoryVal.String
		}

		if snippet.Valid {
			c.Snippet = snippet.String
		}
		if latestDateStr.Valid && latestDateStr.String != "" {
			c.LatestDate = parseTimeString(latestDateStr.String)
		}

		// Parse message IDs from comma-separated string
		if messageIDsStr.Valid && messageIDsStr.String != "" {
			c.MessageIDs = strings.Split(messageIDsStr.String, ",")
		}

		if participantsJSON.Valid {
			if useToList {
				c.Participants = parseAggregatedToListJSON(participantsJSON.String)
			}
			if !useToList {
				var latestSnippet string
				c.Participants, latestSnippet = parseParticipantsJSON(participantsJSON.String)
				if latestSnippet != "" {
					c.Snippet = latestSnippet
				}
			}
		}

		conversations = append(conversations, c)
	}

	return conversations, nil
}

// parseParticipantsJSON parses the per-message sender objects produced by
// json_group_array(json_object('name',…,'email',…,'date',…,'snippet',…)),
// orders them latest-first, and dedupes by lowercased email — so
// Participants[0] is always the LATEST message's sender (#169; SQLite fills
// aggregates in scan order, which is oldest-first). The second return value
// is the newest non-empty snippet (falling back to the newest), so the row
// preview also reflects the latest message; empty when undeterminable.
func parseParticipantsJSON(s string) ([]Address, string) {
	if s == "" || s == "[]" {
		return nil, ""
	}
	var raw []struct {
		Name    string `json:"name"`
		Email   string `json:"email"`
		Date    string `json:"date"`
		Snippet string `json:"snippet"`
	}
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, ""
	}
	// Dates share one storage format (written by the same driver), so
	// lexicographic comparison orders them correctly.
	sort.SliceStable(raw, func(i, j int) bool {
		return raw[i].Date > raw[j].Date
	})
	participants := make([]Address, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	latestSnippet := ""
	for _, r := range raw {
		if latestSnippet == "" && r.Snippet != "" {
			latestSnippet = r.Snippet
		}
		key := strings.ToLower(r.Email)
		if seen[key] {
			continue
		}
		seen[key] = true
		participants = append(participants, Address{Name: r.Name, Email: r.Email})
	}
	return participants, latestSnippet
}

// parseAggregatedToListJSON parses the nested array produced by
// `json_group_array(json(to_list))` — one outer entry per message in the
// conversation, each containing that message's parsed to_list array.
// Flattens to a deduplicated Address slice keyed by lowercased email.
//
// Used by ListConversationsByFolder when the folder type is sent or
// drafts so the row's "who" column reflects recipients rather than the
// always-self sender.
func parseAggregatedToListJSON(s string) []Address {
	if s == "" || s == "[]" {
		return nil
	}
	var nested [][]struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal([]byte(s), &nested); err != nil {
		return nil
	}
	participants := make([]Address, 0)
	seen := make(map[string]bool)
	for _, inner := range nested {
		for _, r := range inner {
			key := strings.ToLower(r.Email)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			participants = append(participants, Address{Name: r.Name, Email: r.Email})
		}
	}
	return participants
}

// CountConversationsByFolder returns the count of conversations in a folder
func (s *Store) CountConversationsByFolder(folderID, filter, category, subCategory string) (int, error) {
	categoryClause, categoryArgs := categoryFilterClause(category, subCategory, "m.", "folder_id")
	query := `
		SELECT COUNT(DISTINCT COALESCE(m.thread_id, m.id))
		FROM messages m
		WHERE m.folder_id = ? AND m.is_hidden = 0
	` + filterWhereClause(filter, "m.") + categoryClause

	args := append([]any{folderID}, categoryArgs...)
	var count int
	err := s.db.QueryRow(query, args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count conversations: %w", err)
	}
	return count, nil
}

// GetCategoryCounts returns the Phase E tab counts for one list scope:
// conversations per category (latest message's category; never-classified
// rows count as Personal) plus, inside Automated, one count per
// sub-category. When unified is true the scope is every inbox of every
// enabled account; otherwise it is the single folderID. Hidden (reaction)
// rows are excluded, matching the list queries.
func (s *Store) GetCategoryCounts(folderID string, unified bool) (*CategoryCounts, error) {
	var query string
	var args []any
	if unified {
		query = `
			SELECT COALESCE(m.category, 'personal') AS category, m.sub_category, COUNT(*)
			FROM (
				SELECT m.category, m.sub_category,
					ROW_NUMBER() OVER (PARTITION BY COALESCE(m.thread_id, m.id), m.account_id ORDER BY m.date DESC) AS rn
				FROM messages m
				INNER JOIN folders f ON m.folder_id = f.id AND f.folder_type = 'inbox'
				INNER JOIN accounts a ON f.account_id = a.id AND a.enabled = 1
				WHERE m.is_hidden = 0
			) m
			WHERE m.rn = 1
			GROUP BY category, m.sub_category
		`
	} else {
		query = `
			SELECT COALESCE(m.category, 'personal') AS category, m.sub_category, COUNT(*)
			FROM (
				SELECT m.category, m.sub_category,
					ROW_NUMBER() OVER (PARTITION BY COALESCE(m.thread_id, m.id), m.account_id ORDER BY m.date DESC) AS rn
				FROM messages m
				WHERE m.folder_id = ? AND m.is_hidden = 0
			) m
			WHERE m.rn = 1
			GROUP BY category, m.sub_category
		`
		args = []any{folderID}
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query category counts: %w", err)
	}
	defer rows.Close()
	counts := &CategoryCounts{SubCategories: map[string]int{}}
	for rows.Next() {
		var category string
		var subCategory sql.NullString
		var n int
		if err := rows.Scan(&category, &subCategory, &n); err != nil {
			return nil, fmt.Errorf("failed to scan category count: %w", err)
		}
		counts.Total += n
		switch category {
		case enrich.CategoryAutomated:
			counts.Automated += n
			if subCategory.Valid && subCategory.String != "" {
				counts.SubCategories[subCategory.String] += n
			}
		default:
			// "personal" and anything unexpected count as Personal —
			// the classifier only ever writes the two values, and an
			// unknown value must not hide mail from the Personal tab.
			counts.Personal += n
		}
	}
	return counts, nil
}

// GetConversation returns messages in a conversation/thread from the specified folder plus Sent and Drafts
func (s *Store) GetConversation(threadID, folderID string) (*Conversation, error) {
	s.log.Debug().
		Str("threadID", threadID).
		Str("folderID", folderID).
		Msg("GetConversation called in store")

	// If the threadID is a UUID (not a Message-ID), resolve it to the actual
	// thread_id from the DB. This handles the case where a message's thread_id
	// was updated by thread reconciliation after initial save.
	if !strings.Contains(threadID, "@") && !strings.HasPrefix(threadID, "<") {
		var actualThreadID sql.NullString
		err := s.db.QueryRow("SELECT thread_id FROM messages WHERE id = ?", threadID).Scan(&actualThreadID)
		if err == nil && actualThreadID.Valid && actualThreadID.String != "" {
			s.log.Debug().Str("uuid", threadID).Str("resolvedThreadID", actualThreadID.String).Msg("Resolved UUID to thread_id")
			threadID = actualThreadID.String
		}
	}

	// First get the account ID and folder type
	var accountID string
	var folderType string
	err := s.db.QueryRow("SELECT account_id, folder_type FROM folders WHERE id = ?", folderID).Scan(&accountID, &folderType)
	if err != nil {
		return nil, fmt.Errorf("failed to get account ID and folder type: %w", err)
	}

	// Normalize the thread ID for comparison
	normalizedThreadID := normalizeMessageID(threadID)
	s.log.Debug().
		Str("normalizedThreadID", normalizedThreadID).
		Str("accountID", accountID).
		Msg("GetConversation normalized")

	// Get conversation summary from current folder + Sent + Drafts
	// This gives full conversation context without cross-folder bleed
	// Exclude messages in Trash folder unless we're viewing Trash
	// Use COALESCE to handle NULL values from aggregate functions when no rows match
	trashFilter := ""
	if folderType != "trash" {
		trashFilter = "AND f.folder_type != 'trash'"
	}

	// Scope to current folder + Sent + Drafts (for full conversation context)
	folderFilter := "AND (m.folder_id = ? OR f.folder_type IN ('sent', 'drafts'))"

	summaryQuery := fmt.Sprintf(`
		SELECT
			COALESCE(MIN(m.subject), '') as subject,
			COALESCE(MAX(m.snippet), '') as snippet,
			COUNT(*) as message_count,
			COALESCE(SUM(CASE WHEN m.is_read = 0 THEN 1 ELSE 0 END), 0) as unread_count,
			COALESCE(MAX(CASE WHEN m.has_attachments = 1 THEN 1 ELSE 0 END), 0) as has_attachments,
			COALESCE(MAX(CASE WHEN m.is_starred = 1 THEN 1 ELSE 0 END), 0) as is_starred,
			MAX(m.date) as latest_date
		FROM messages m
		INNER JOIN folders f ON m.folder_id = f.id
		WHERE m.account_id = ? AND m.is_hidden = 0 AND (
			REPLACE(REPLACE(COALESCE(m.thread_id, m.id), '<', ''), '>', '') = ?
			OR REPLACE(REPLACE(m.message_id, '<', ''), '>', '') = ?
			OR REPLACE(REPLACE(m.in_reply_to, '<', ''), '>', '') = ?
		)
		%s %s
	`, trashFilter, folderFilter)

	c := &Conversation{ThreadID: threadID}
	var latestDateStr sql.NullString

	err = s.db.QueryRow(summaryQuery, accountID, normalizedThreadID, normalizedThreadID, normalizedThreadID, folderID).Scan(
		&c.Subject,
		&c.Snippet,
		&c.MessageCount,
		&c.UnreadCount,
		&c.HasAttachments,
		&c.IsStarred,
		&latestDateStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get conversation summary: %w", err)
	}
	if latestDateStr.Valid && latestDateStr.String != "" {
		c.LatestDate = parseTimeString(latestDateStr.String)
	}

	// Get messages in the thread from current folder + Sent + Drafts
	// This gives full conversation context without cross-folder bleed
	// Exclude messages in Trash folder unless we're viewing Trash
	messagesQuery := fmt.Sprintf(`
		SELECT m.id, m.account_id, m.folder_id, m.uid, m.message_id, m.in_reply_to, m.references_list, m.thread_id,
		       m.subject, m.from_name, m.from_email, m.to_list, m.cc_list, m.bcc_list, m.reply_to, m.date,
		       m.snippet, m.is_read, m.is_starred, m.is_answered, m.is_forwarded, m.is_draft, m.is_deleted,
		       m.size, m.has_attachments, m.body_text, m.body_html, m.body_fetched, m.body_truncated,
		       m.read_receipt_to, m.read_receipt_handled,
		       m.smime_status, m.smime_signer_email, m.smime_signer_subject,
		       m.smime_encrypted, (m.smime_raw_body IS NOT NULL) as has_smime,
		       m.pgp_status, m.pgp_signer_email, m.pgp_signer_key_id,
		       m.pgp_encrypted, (m.pgp_raw_body IS NOT NULL) as has_pgp,
		       m.list_unsubscribe, m.list_unsubscribe_post, m.category, m.sub_category,
		       m.extract_kind, m.extract_value, m.extract_rule,
		       m.received_at
		FROM messages m
		INNER JOIN folders f ON m.folder_id = f.id
		WHERE m.account_id = ? AND m.is_hidden = 0 AND (
			REPLACE(REPLACE(COALESCE(m.thread_id, m.id), '<', ''), '>', '') = ?
			OR REPLACE(REPLACE(m.message_id, '<', ''), '>', '') = ?
			OR REPLACE(REPLACE(m.in_reply_to, '<', ''), '>', '') = ?
		)
		%s %s
		ORDER BY m.date ASC
	`, trashFilter, folderFilter)

	rows, err := s.db.Query(messagesQuery, accountID, normalizedThreadID, normalizedThreadID, normalizedThreadID, folderID)
	if err != nil {
		return nil, fmt.Errorf("failed to query thread messages: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		m := &Message{}
		var messageID, inReplyTo, references, threadIDVal, toList, ccList, bccList, replyTo, snippetVal, bodyText, bodyHTML, readReceiptTo sql.NullString
		var smimeStatus, smimeSignerEmail, smimeSignerSubject sql.NullString
		var pgpStatus, pgpSignerEmail, pgpSignerKeyID sql.NullString
		var listUnsubscribe, category, subCategory, extractKind, extractValue, extractRule sql.NullString
		var dateStr, receivedAtStr sql.NullString
		var uidI64 int64

		err := rows.Scan(
			&m.ID, &m.AccountID, &m.FolderID, &uidI64, &messageID, &inReplyTo, &references, &threadIDVal,
			&m.Subject, &m.FromName, &m.FromEmail, &toList, &ccList, &bccList, &replyTo, &dateStr,
			&snippetVal, &m.IsRead, &m.IsStarred, &m.IsAnswered, &m.IsForwarded, &m.IsDraft, &m.IsDeleted,
			&m.Size, &m.HasAttachments, &bodyText, &bodyHTML, &m.BodyFetched, &m.BodyTruncated,
			&readReceiptTo, &m.ReadReceiptHandled,
			&smimeStatus, &smimeSignerEmail, &smimeSignerSubject,
			&m.SMIMEEncrypted, &m.HasSMIME,
			&pgpStatus, &pgpSignerEmail, &pgpSignerKeyID,
			&m.PGPEncrypted, &m.HasPGP,
			&listUnsubscribe, &m.ListUnsubscribePost, &category, &subCategory,
			&extractKind, &extractValue, &extractRule,
			&receivedAtStr,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		m.UID = uint32(uidI64)

		if messageID.Valid {
			m.MessageID = messageID.String
		}
		if inReplyTo.Valid {
			m.InReplyTo = inReplyTo.String
		}
		if references.Valid {
			m.References = references.String
		}
		if threadIDVal.Valid {
			m.ThreadID = threadIDVal.String
		}
		if toList.Valid {
			m.ToList = toList.String
		}
		if ccList.Valid {
			m.CcList = ccList.String
		}
		if bccList.Valid {
			m.BccList = bccList.String
		}
		if replyTo.Valid {
			m.ReplyTo = replyTo.String
		}
		if dateStr.Valid && dateStr.String != "" {
			m.Date = parseTimeString(dateStr.String)
		}
		if snippetVal.Valid {
			m.Snippet = snippetVal.String
		}
		if bodyText.Valid {
			m.BodyText = bodyText.String
		}
		if bodyHTML.Valid {
			m.BodyHTML = bodyHTML.String
		}
		if readReceiptTo.Valid {
			m.ReadReceiptTo = readReceiptTo.String
		}
		if smimeStatus.Valid {
			m.SMIMEStatus = smimeStatus.String
		}
		if smimeSignerEmail.Valid {
			m.SMIMESignerEmail = smimeSignerEmail.String
		}
		if smimeSignerSubject.Valid {
			m.SMIMESignerSubject = smimeSignerSubject.String
		}
		if pgpStatus.Valid {
			m.PGPStatus = pgpStatus.String
		}
		if pgpSignerEmail.Valid {
			m.PGPSignerEmail = pgpSignerEmail.String
		}
		if pgpSignerKeyID.Valid {
			m.PGPSignerKeyID = pgpSignerKeyID.String
		}
		if listUnsubscribe.Valid {
			m.ListUnsubscribe = listUnsubscribe.String
		}
		if category.Valid {
			m.Category = category.String
		}
		if subCategory.Valid {
			m.SubCategory = subCategory.String
		}
		if extractKind.Valid {
			m.ExtractKind = extractKind.String
		}
		if extractValue.Valid {
			m.ExtractValue = extractValue.String
		}
		if extractRule.Valid {
			m.ExtractRule = extractRule.String
		}
		if receivedAtStr.Valid && receivedAtStr.String != "" {
			m.ReceivedAt = parseTimeString(receivedAtStr.String)
		}

		s.log.Debug().
			Str("id", m.ID).
			Str("messageID", m.MessageID).
			Str("threadID", m.ThreadID).
			Str("subject", m.Subject).
			Int("bodyTextLen", len(m.BodyText)).
			Int("bodyHTMLLen", len(m.BodyHTML)).
			Msg("GetConversation found message")

		c.Messages = append(c.Messages, m)
	}

	// Dedupe copies of the same email (#222): the query spans current
	// folder + Sent + Drafts, so a self-BCC'd send appears as both the
	// Sent copy and the delivered copy. Collapse rows sharing a non-empty
	// Message-ID, preferring current folder > sent > drafts so actions
	// operate on the copy in the folder being viewed. Stable, keeps
	// date-ASC order.
	msgPriority := func(m *Message) int {
		if m.FolderID == folderID {
			return 0
		}
		if m.IsDraft {
			return 2
		}
		return 1
	}
	byMsgID := make(map[string]int) // normalized Message-ID -> index in deduped
	deduped := c.Messages[:0]
	for _, m := range c.Messages {
		id := normalizeMessageID(m.MessageID)
		if id == "" {
			deduped = append(deduped, m)
			continue
		}
		existing, ok := byMsgID[id]
		if !ok {
			byMsgID[id] = len(deduped)
			deduped = append(deduped, m)
			continue
		}
		if msgPriority(m) < msgPriority(deduped[existing]) {
			deduped[existing] = m
		}
	}
	c.Messages = deduped
	// The summary query's COUNT(*) counts duplicate copies too
	c.MessageCount = len(c.Messages)

	s.log.Debug().
		Int("messageCount", len(c.Messages)).
		Str("threadID", threadID).
		Msg("GetConversation returning")

	// The row chip acts on the latest message's extraction (Phase A).
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if m := c.Messages[i]; m.ExtractKind != "" {
			c.ExtractKind = m.ExtractKind
			c.ExtractValue = m.ExtractValue
			c.ExtractRule = m.ExtractRule
			c.ExtractMessageID = m.ID
			break
		}
	}

	// Build participants from already-loaded messages (no extra query needed).
	// Messages are date-ASC; iterate in reverse so Participants[0] is the
	// latest sender, matching the list-query ordering (#169).
	seen := make(map[string]bool)
	for i := len(c.Messages) - 1; i >= 0; i-- {
		msg := c.Messages[i]
		key := strings.ToLower(msg.FromEmail)
		if seen[key] {
			continue
		}
		seen[key] = true
		c.Participants = append(c.Participants, Address{Name: msg.FromName, Email: msg.FromEmail})
	}

	return c, nil
}

// normalizeMessageID strips angle brackets from Message-IDs for consistent comparison.
// It trims every surrounding bracket, matching the SQL-side bracket-stripping
// REPLACE used in the lookups and the recovery path's strings.Trim(..., "<>"),
// so the Go and SQL forms agree even on a malformed multi-bracket value.
func normalizeMessageID(msgID string) string {
	msgID = strings.TrimSpace(msgID)
	msgID = strings.Trim(msgID, "<>")
	return msgID
}

// FindThreadID finds the thread ID for a message based on References and In-Reply-To headers
func (s *Store) FindThreadID(accountID, messageID, inReplyTo string, references []string) (string, error) {
	// Normalize the message ID
	messageID = normalizeMessageID(messageID)
	inReplyTo = normalizeMessageID(inReplyTo)

	// Normalize all references
	normalizedRefs := make([]string, 0, len(references))
	for _, ref := range references {
		if normalized := normalizeMessageID(ref); normalized != "" {
			normalizedRefs = append(normalizedRefs, normalized)
		}
	}

	// Build list of all potential thread roots to check
	allRefs := make([]string, 0)
	if inReplyTo != "" {
		allRefs = append(allRefs, inReplyTo)
	}
	allRefs = append(allRefs, normalizedRefs...)

	if len(allRefs) == 0 {
		// No threading info - this message starts its own thread
		return messageID, nil
	}

	// Check if any of the references match existing messages
	for _, ref := range allRefs {
		// Check with and without angle brackets since DB might have either format
		var existingThreadID sql.NullString

		// Try exact match first
		err := s.db.QueryRow(
			"SELECT COALESCE(thread_id, id) FROM messages WHERE account_id = ? AND (message_id = ? OR message_id = ? OR message_id = ?) LIMIT 1",
			accountID, ref, "<"+ref+">", strings.TrimPrefix(strings.TrimSuffix(ref, ">"), "<"),
		).Scan(&existingThreadID)

		if err == nil && existingThreadID.Valid && existingThreadID.String != "" {
			// Normalize the returned thread ID too
			return normalizeMessageID(existingThreadID.String), nil
		}
	}

	// No existing thread found - use the first reference as thread ID (root message)
	// This is the original message that started the thread
	if len(normalizedRefs) > 0 {
		return normalizedRefs[0], nil
	}
	if inReplyTo != "" {
		return inReplyTo, nil
	}

	return messageID, nil
}

// UpdateThreadID updates the thread_id for a message
func (s *Store) UpdateThreadID(id, threadID string) error {
	_, err := s.db.Exec("UPDATE messages SET thread_id = ? WHERE id = ?", threadID, id)
	if err != nil {
		return fmt.Errorf("failed to update thread_id: %w", err)
	}
	return nil
}

// ReconcileThreads updates thread_ids for messages that reference a newly synced message.
// This ensures that when a new message arrives, any existing messages that reference it
// (via In-Reply-To) get linked to the same thread.
// Returns the number of messages updated.
func (s *Store) ReconcileThreads(accountID, messageID, threadID string) (int, error) {
	// Normalize the message ID for comparison
	normalizedMsgID := normalizeMessageID(messageID)
	if normalizedMsgID == "" {
		return 0, nil
	}

	// Find messages that have in_reply_to pointing to this message's ID
	// and update their thread_id to match
	// We check multiple formats of the message ID (with/without angle brackets)
	query := `
		UPDATE messages 
		SET thread_id = ?
		WHERE account_id = ? 
		AND thread_id != ?
		AND (
			REPLACE(REPLACE(in_reply_to, '<', ''), '>', '') = ?
			OR in_reply_to = ?
			OR in_reply_to = ?
		)
	`

	result, err := s.db.Exec(query,
		threadID,
		accountID,
		threadID,
		normalizedMsgID,
		normalizedMsgID,
		"<"+normalizedMsgID+">",
	)
	if err != nil {
		return 0, fmt.Errorf("failed to reconcile threads: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected > 0 {
		s.log.Info().
			Str("accountID", accountID).
			Str("messageID", messageID).
			Str("threadID", threadID).
			Int64("updated", affected).
			Msg("Reconciled thread IDs for related messages")
	}

	return int(affected), nil
}

// ReconcileThreadsForNewMessage is called after syncing a new message.
// It checks both directions:
// 1. If this message references other messages, update this message's thread_id to match
// 2. If other messages reference this message's ID, update their thread_ids to match this one
func (s *Store) ReconcileThreadsForNewMessage(accountID, messageUUID, messageID, threadID, inReplyTo string) error {
	normalizedMsgID := normalizeMessageID(messageID)
	normalizedThreadID := normalizeMessageID(threadID)
	normalizedInReplyTo := normalizeMessageID(inReplyTo)

	// Direction 1: This message replies to another - find the original and adopt its thread_id
	if normalizedInReplyTo != "" {
		var existingThreadID sql.NullString
		err := s.db.QueryRow(`
			SELECT COALESCE(thread_id, id) FROM messages 
			WHERE account_id = ? 
			AND (
				REPLACE(REPLACE(message_id, '<', ''), '>', '') = ?
				OR message_id = ?
				OR message_id = ?
			)
			LIMIT 1
		`, accountID, normalizedInReplyTo, normalizedInReplyTo, "<"+normalizedInReplyTo+">").Scan(&existingThreadID)

		if err == nil && existingThreadID.Valid && existingThreadID.String != "" {
			existingNormalized := normalizeMessageID(existingThreadID.String)
			if existingNormalized != normalizedThreadID {
				// Update this message's thread_id to match the existing thread
				if err := s.UpdateThreadID(messageUUID, existingNormalized); err != nil {
					s.log.Warn().Err(err).Msg("Failed to update thread_id for reply")
				} else {
					s.log.Debug().
						Str("messageUUID", messageUUID).
						Str("oldThreadID", normalizedThreadID).
						Str("newThreadID", existingNormalized).
						Msg("Updated reply message to join existing thread")
					normalizedThreadID = existingNormalized
				}
			}
		}
	}

	// Direction 2: Other messages may have replied to this one - update their thread_ids
	if normalizedMsgID != "" {
		_, err := s.ReconcileThreads(accountID, normalizedMsgID, normalizedThreadID)
		if err != nil {
			s.log.Warn().Err(err).Msg("Failed to reconcile threads for replies to this message")
		}
	}

	return nil
}

// UpdateFlagsBatch updates flags for multiple messages by their IDs
// Pass nil for flags you don't want to update
func (s *Store) UpdateFlagsBatch(ids []string, isRead, isStarred *bool) error {
	if len(ids) == 0 {
		return nil
	}

	// Build dynamic SET clause based on what's being updated
	var setClauses []string
	var args []interface{}

	if isRead != nil {
		setClauses = append(setClauses, "is_read = ?")
		args = append(args, *isRead)
	}
	if isStarred != nil {
		setClauses = append(setClauses, "is_starred = ?")
		args = append(args, *isStarred)
	}

	if len(setClauses) == 0 {
		return nil
	}

	// Build placeholders for IN clause
	placeholders := make([]string, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}

	query := fmt.Sprintf(
		"UPDATE messages SET %s WHERE id IN (%s)",
		strings.Join(setClauses, ", "),
		strings.Join(placeholders, ", "),
	)

	_, err := s.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("failed to update flags batch: %w", err)
	}
	return nil
}

// MarkBodyFailed flags messages whose body fetch+parse produced no usable content
// so they are excluded from future body-fetch queries (GetMessagesWithoutBody and
// friends). Idempotent: re-flagging an already-flagged row is a no-op. The flag
// is cleared by a Force Resync (ClearBodiesForFolder resets it for the whole
// folder) or by a later body persist that carries usable content
// (UpdateBody/UpdateBodiesBatch) — it is a retry gate, not a permanent verdict.
//
// Without this persistent flag, an unparseable message stays empty in the local
// DB, GetMessagesWithoutBody picks it up next sync, IMAP FETCH runs again, parse
// fails again — forever. See migration v39.
func (s *Store) MarkBodyFailed(messageIDs []string) error {
	if len(messageIDs) == 0 {
		return nil
	}

	placeholders := make([]string, len(messageIDs))
	args := make([]interface{}, len(messageIDs))
	for i, id := range messageIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(
		"UPDATE messages SET body_failed = 1 WHERE id IN (%s)",
		strings.Join(placeholders, ", "),
	)

	if _, err := s.db.Exec(query, args...); err != nil {
		return fmt.Errorf("failed to mark messages body-failed: %w", err)
	}
	return nil
}

// MoveMessages updates the folder_id for multiple messages.
//
// Design note — temp UIDs:
// When a message is moved locally, it lives in the destination folder before
// the IMAP server has assigned a real UID for it there. To bridge that gap
// without using NULL or colliding with real UIDs, this function writes
// `uid = -rowid` — a guaranteed-unique negative value derived from SQLite's
// auto-increment row id. Real IMAP UIDs are positive uint32, so the sign
// distinguishes "temp" from "synced" cleanly:
//
//   - WHERE uid > 0   → only real, server-assigned UIDs
//   - WHERE uid < 0   → only locally-moved rows awaiting reconciliation
//   - DeleteTempUIDs cleans up uid < 0 rows
//   - app/actions.go skips rows where int32(m.UID) < 0 before sending to IMAP
//
// The Go layer reads uid into uint32 (the IMAP type), so a stored int64(-37727)
// becomes uint32(0xFFFF6CE1) = 4_294_929_569 in memory. The int32() recast in
// the skip check correctly identifies these. Scan sites that read the uid
// column must use an int64 intermediary first — scanning a negative int64
// directly into uint32 fails on modernc.org/sqlite's converter (the lower 32
// bits of the int64 are preserved by the explicit uint32 cast).
//
// Potential future refactor: split this into a dedicated `pending_move BOOLEAN`
// (or `sync_state TEXT`) column. The current design works fine; the
// motivation for refactoring would be onboarding clarity — making the
// temp-marker concept explicit in the schema rather than implicit in the sign
// of the uid column.
func (s *Store) MoveMessages(ids []string, newFolderID string) error {
	if len(ids) == 0 {
		return nil
	}

	// Deduplicate message IDs to prevent constraint violations
	seen := make(map[string]bool)
	uniqueIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			uniqueIDs = append(uniqueIDs, id)
		}
	}

	placeholders := make([]string, len(uniqueIDs))
	args := []interface{}{newFolderID}
	for i, id := range uniqueIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}

	query := fmt.Sprintf(
		"UPDATE messages SET folder_id = ?, uid = -rowid WHERE id IN (%s)",
		strings.Join(placeholders, ", "),
	)

	_, err := s.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("failed to move messages: %w", err)
	}
	return nil
}

// DeleteTempUIDs removes messages with temporary negative UIDs in a folder.
// These are left over after MoveMessages assigns -rowid as a placeholder UID.
func (s *Store) DeleteTempUIDs(folderID string) error {
	_, err := s.db.Exec("DELETE FROM messages WHERE folder_id = ? AND uid < 0", folderID)
	if err != nil {
		return fmt.Errorf("failed to delete temp UID messages: %w", err)
	}
	return nil
}

// ExpandToFolderCopies returns ids plus the ids of every other row in the same
// account+folder that shares a non-empty Message-ID with one of them (real
// server rows only, uid > 0). The viewer merges such copies into one message
// (#222), so an action on the merged message must reach all copies or the
// thread never clears (a server holding N delivery copies left N-1 unread and
// N-1 copies behind on delete). Order: input ids first, then discovered copies.
func (s *Store) ExpandToFolderCopies(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return ids, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)*2)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	for _, id := range ids {
		args = append(args, id)
	}
	in := strings.Join(placeholders, ", ")

	query := fmt.Sprintf(`
		SELECT DISTINCT m2.id
		FROM messages m1
		JOIN messages m2
		  ON m2.account_id = m1.account_id
		 AND m2.folder_id = m1.folder_id
		 AND REPLACE(REPLACE(m2.message_id, '<', ''), '>', '') = REPLACE(REPLACE(m1.message_id, '<', ''), '>', '')
		WHERE m1.id IN (%s)
		  AND REPLACE(REPLACE(m1.message_id, '<', ''), '>', '') <> ''
		  AND m2.uid > 0
		  AND m2.id NOT IN (%s)`, in, in)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query folder copies: %w", err)
	}
	defer rows.Close()

	expanded := append([]string(nil), ids...)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan folder copy ID: %w", err)
		}
		expanded = append(expanded, id)
	}
	return expanded, rows.Err()
}

// GetIDsByMessageIDs finds local DB message IDs by RFC822 Message-ID header and folder.
// The comparison ignores surrounding angle brackets: the caller may pass the
// ID as stored on a locally created row (`<uuid@daak>`) while the destination
// row was synced via the envelope path (no brackets), or vice versa.
// It deliberately includes rows with negative placeholder UIDs: MoveMessages
// stamps just-moved rows uid = -rowid until the destination sync replaces
// them with server-assigned UIDs, and this lookup exists for the undo path,
// which must find a message in the seconds after its move — while the toast
// is still up. Filtering uid > 0 here made Undo fail with "messages not
// found in destination folder" whenever the destination sync had not
// finished yet (and always, on accounts whose sync cannot complete).
func (s *Store) GetIDsByMessageIDs(accountID, folderID string, rfc822MessageIDs []string) ([]string, error) {
	if len(rfc822MessageIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(rfc822MessageIDs))
	args := []interface{}{accountID, folderID}
	for i, mid := range rfc822MessageIDs {
		placeholders[i] = "?"
		args = append(args, normalizeMessageID(mid))
	}

	query := fmt.Sprintf(
		"SELECT id FROM messages WHERE account_id = ? AND folder_id = ? AND REPLACE(REPLACE(message_id, '<', ''), '>', '') IN (%s)",
		strings.Join(placeholders, ", "),
	)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan message ID: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeleteBatch deletes multiple messages by their IDs
func (s *Store) DeleteBatch(ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf("DELETE FROM messages WHERE id IN (%s)", strings.Join(placeholders, ", "))
	_, err := s.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("failed to delete messages batch: %w", err)
	}
	return nil
}

// GetByIDs retrieves multiple messages by their IDs
// SpansMultipleAccounts returns true when the given message IDs belong
// to two or more different accounts. Cheap (single SELECT COUNT
// DISTINCT against the indexed account_id column) — used by bulk-action
// callers in app/actions.go to decide between the single-account fast
// path and the cross-account partition+dispatch path. A nil/short input
// returns false without hitting the DB.
func (s *Store) SpansMultipleAccounts(ids []string) (bool, error) {
	if len(ids) < 2 {
		return false, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(
		"SELECT COUNT(DISTINCT account_id) FROM messages WHERE id IN (%s)",
		strings.Join(placeholders, ", "),
	)
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		return false, fmt.Errorf("failed to check account span: %w", err)
	}
	return n > 1, nil
}

func (s *Store) GetByIDs(ids []string) ([]*Message, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(`
		SELECT id, account_id, folder_id, uid, message_id, in_reply_to, references_list, thread_id,
		       subject, from_name, from_email, to_list, cc_list, bcc_list, reply_to, date,
		       snippet, is_read, is_starred, is_answered, is_forwarded, is_draft, is_deleted,
		       size, has_attachments, body_text, body_html, body_fetched, body_truncated,
		       read_receipt_to, read_receipt_handled,
		       smime_status, smime_signer_email, smime_signer_subject,
		       smime_encrypted, (smime_raw_body IS NOT NULL) as has_smime,
		       pgp_status, pgp_signer_email, pgp_signer_key_id,
		       pgp_encrypted, (pgp_raw_body IS NOT NULL) as has_pgp,
		       list_unsubscribe, list_unsubscribe_post, category, sub_category,
		       extract_kind, extract_value, extract_rule,
		       received_at
		FROM messages WHERE id IN (%s)
	`, strings.Join(placeholders, ", "))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()

	var messages []*Message
	for rows.Next() {
		m := &Message{}
		var messageID, inReplyTo, references, threadID, toList, ccList, bccList, replyTo, snippet, bodyText, bodyHTML, readReceiptTo sql.NullString
		var smimeStatus, smimeSignerEmail, smimeSignerSubject sql.NullString
		var pgpStatus, pgpSignerEmail, pgpSignerKeyID sql.NullString
		var listUnsubscribe, category, subCategory, extractKind, extractValue, extractRule sql.NullString
		var dateStr, receivedAtStr sql.NullString
		var uidI64 int64

		err := rows.Scan(
			&m.ID, &m.AccountID, &m.FolderID, &uidI64, &messageID, &inReplyTo, &references, &threadID,
			&m.Subject, &m.FromName, &m.FromEmail, &toList, &ccList, &bccList, &replyTo, &dateStr,
			&snippet, &m.IsRead, &m.IsStarred, &m.IsAnswered, &m.IsForwarded, &m.IsDraft, &m.IsDeleted,
			&m.Size, &m.HasAttachments, &bodyText, &bodyHTML, &m.BodyFetched, &m.BodyTruncated,
			&readReceiptTo, &m.ReadReceiptHandled,
			&smimeStatus, &smimeSignerEmail, &smimeSignerSubject,
			&m.SMIMEEncrypted, &m.HasSMIME,
			&pgpStatus, &pgpSignerEmail, &pgpSignerKeyID,
			&m.PGPEncrypted, &m.HasPGP,
			&listUnsubscribe, &m.ListUnsubscribePost, &category, &subCategory,
			&extractKind, &extractValue, &extractRule,
			&receivedAtStr,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		m.UID = uint32(uidI64)

		if messageID.Valid {
			m.MessageID = messageID.String
		}
		if inReplyTo.Valid {
			m.InReplyTo = inReplyTo.String
		}
		if references.Valid {
			m.References = references.String
		}
		if threadID.Valid {
			m.ThreadID = threadID.String
		}
		if toList.Valid {
			m.ToList = toList.String
		}
		if ccList.Valid {
			m.CcList = ccList.String
		}
		if bccList.Valid {
			m.BccList = bccList.String
		}
		if replyTo.Valid {
			m.ReplyTo = replyTo.String
		}
		if dateStr.Valid && dateStr.String != "" {
			m.Date = parseTimeString(dateStr.String)
		}
		if snippet.Valid {
			m.Snippet = snippet.String
		}
		if bodyText.Valid {
			m.BodyText = bodyText.String
		}
		if bodyHTML.Valid {
			m.BodyHTML = bodyHTML.String
		}
		if readReceiptTo.Valid {
			m.ReadReceiptTo = readReceiptTo.String
		}
		if smimeStatus.Valid {
			m.SMIMEStatus = smimeStatus.String
		}
		if smimeSignerEmail.Valid {
			m.SMIMESignerEmail = smimeSignerEmail.String
		}
		if smimeSignerSubject.Valid {
			m.SMIMESignerSubject = smimeSignerSubject.String
		}
		if pgpStatus.Valid {
			m.PGPStatus = pgpStatus.String
		}
		if pgpSignerEmail.Valid {
			m.PGPSignerEmail = pgpSignerEmail.String
		}
		if pgpSignerKeyID.Valid {
			m.PGPSignerKeyID = pgpSignerKeyID.String
		}
		if listUnsubscribe.Valid {
			m.ListUnsubscribe = listUnsubscribe.String
		}
		if category.Valid {
			m.Category = category.String
		}
		if subCategory.Valid {
			m.SubCategory = subCategory.String
		}
		if extractKind.Valid {
			m.ExtractKind = extractKind.String
		}
		if extractValue.Valid {
			m.ExtractValue = extractValue.String
		}
		if extractRule.Valid {
			m.ExtractRule = extractRule.String
		}
		if receivedAtStr.Valid && receivedAtStr.String != "" {
			m.ReceivedAt = parseTimeString(receivedAtStr.String)
		}

		messages = append(messages, m)
	}

	return messages, nil
}

// SearchConversations searches for conversations in a folder using FTS5
// Returns conversations with highlighted text and the total count
func (s *Store) SearchConversations(folderID, query string, offset, limit int, filter string) ([]*ConversationSearchResult, int, error) {
	if query == "" {
		return nil, 0, nil
	}

	// Prepare the FTS query - escape special characters and add prefix matching
	ftsQuery := prepareFTSQuery(query)

	// First, get the total count
	countQuery := `
		SELECT COUNT(DISTINCT COALESCE(m.thread_id, m.id))
		FROM messages m
		JOIN messages_fts fts ON m.rowid = fts.rowid
		WHERE m.folder_id = ? AND m.is_hidden = 0 AND messages_fts MATCH ?
	` + filterWhereClause(filter, "m.")
	var totalCount int
	err := s.db.QueryRow(countQuery, folderID, ftsQuery).Scan(&totalCount)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count search results: %w", err)
	}

	if totalCount == 0 {
		return nil, 0, nil
	}

	// Get folder info for displaying in results
	var folderName, folderType string
	err = s.db.QueryRow("SELECT name, folder_type FROM folders WHERE id = ?", folderID).Scan(&folderName, &folderType)
	if err != nil {
		folderName = "Unknown"
		folderType = "folder"
	}

	// Get matching conversations with relevance ranking
	searchQuery := `
		SELECT 
			COALESCE(m.thread_id, m.id) as conv_thread_id,
			MIN(m.subject) as subject,
			MAX(m.snippet) as snippet,
			MIN(m.from_name) as from_name,
			COUNT(*) as message_count,
			SUM(CASE WHEN m.is_read = 0 THEN 1 ELSE 0 END) as unread_count,
			MAX(CASE WHEN m.has_attachments = 1 THEN 1 ELSE 0 END) as has_attachments,
			MAX(CASE WHEN m.is_starred = 1 THEN 1 ELSE 0 END) as is_starred,
			MAX(m.date) as latest_date,
			GROUP_CONCAT(m.id) as message_ids,
			MAX(CASE WHEN m.smime_encrypted = 1 OR m.pgp_encrypted = 1 THEN 1 ELSE 0 END) as is_encrypted,
			(SELECT m2.extract_kind FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as extract_kind,
			(SELECT m2.extract_value FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as extract_value,
			(SELECT m2.extract_rule FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as extract_rule,
			(SELECT m2.id FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as extract_message_id,
			json_group_array(json_object('name', m.from_name, 'email', m.from_email, 'date', m.date, 'snippet', m.snippet)) as participants_json
		FROM messages m
		JOIN messages_fts fts ON m.rowid = fts.rowid
		WHERE m.folder_id = ? AND m.is_hidden = 0 AND messages_fts MATCH ?
		GROUP BY COALESCE(m.thread_id, m.id)` +
		filterHavingClause(filter, "m.") + `
		ORDER BY latest_date DESC
		LIMIT ? OFFSET ?
	`

	rows, err := s.db.Query(searchQuery, folderID, ftsQuery, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to search conversations: %w", err)
	}
	defer rows.Close()

	var results []*ConversationSearchResult
	for rows.Next() {
		c := &ConversationSearchResult{}
		var latestDateStr sql.NullString
		var snippet sql.NullString
		var fromName sql.NullString
		var messageIDsStr sql.NullString
		var participantsJSON sql.NullString
		var extractKind, extractValue, extractRule, extractMessageID sql.NullString

		err := rows.Scan(
			&c.ThreadID,
			&c.Subject,
			&snippet,
			&fromName,
			&c.MessageCount,
			&c.UnreadCount,
			&c.HasAttachments,
			&c.IsStarred,
			&latestDateStr,
			&messageIDsStr,
			&c.IsEncrypted,
			&extractKind, &extractValue, &extractRule, &extractMessageID,
			&participantsJSON,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to scan search result: %w", err)
		}
		if extractKind.Valid {
			c.ExtractKind = extractKind.String
		}
		if extractValue.Valid {
			c.ExtractValue = extractValue.String
		}
		if extractRule.Valid {
			c.ExtractRule = extractRule.String
		}
		if extractMessageID.Valid {
			c.ExtractMessageID = extractMessageID.String
		}

		if snippet.Valid {
			c.Snippet = snippet.String
		}
		if latestDateStr.Valid && latestDateStr.String != "" {
			c.LatestDate = parseTimeString(latestDateStr.String)
		}
		if messageIDsStr.Valid && messageIDsStr.String != "" {
			c.MessageIDs = strings.Split(messageIDsStr.String, ",")
		}

		// Set folder info
		c.FolderName = folderName
		c.FolderType = folderType

		// Resolve participants + latest snippet BEFORE highlighting so the
		// highlighted snippet reflects the latest message (#169)
		if participantsJSON.Valid {
			var latestSnippet string
			c.Participants, latestSnippet = parseParticipantsJSON(participantsJSON.String)
			if latestSnippet != "" {
				c.Snippet = latestSnippet
			}
		}

		// Apply highlighting to displayable fields
		c.HighlightedSubject = highlightMatches(c.Subject, query)
		c.HighlightedSnippet = highlightMatches(c.Snippet, query)
		if fromName.Valid {
			c.HighlightedFromName = highlightMatches(fromName.String, query)
		}

		results = append(results, c)
	}

	return results, totalCount, nil
}

// SearchConversationsUnifiedInbox searches across all inbox folders for all accounts
// SearchConversationsUnifiedFolder searches one unified folder view (see
// ListConversationsUnifiedFolder).
func (s *Store) SearchConversationsUnifiedFolder(folderType, query string, offset, limit int, filter string) ([]*ConversationSearchResult, int, error) {
	if query == "" {
		return nil, 0, nil
	}

	folderJoin, folderArgs, err := unifiedFolderJoin(folderType)
	if err != nil {
		return nil, 0, err
	}

	ftsQuery := prepareFTSQuery(query)

	// Count total results across all folders of the unified type
	countQuery := `
		SELECT COUNT(DISTINCT COALESCE(m.thread_id, m.id) || '-' || a.id)
		FROM messages m
		JOIN messages_fts fts ON m.rowid = fts.rowid
		` + folderJoin + `
		INNER JOIN accounts a ON f.account_id = a.id AND a.enabled = 1
		WHERE m.is_hidden = 0 AND messages_fts MATCH ?` + unifiedFolderStarredWhere(folderType) + `
	` + filterWhereClause(filter, "m.")
	var totalCount int
	countArgs := append(append([]any{}, folderArgs...), ftsQuery)
	err = s.db.QueryRow(countQuery, countArgs...).Scan(&totalCount)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count unified search results: %w", err)
	}

	if totalCount == 0 {
		return nil, 0, nil
	}

	// Search across all inbox folders with account info
	searchQuery := `
		SELECT 
			COALESCE(m.thread_id, m.id) as conv_thread_id,
			MIN(m.subject) as subject,
			MAX(m.snippet) as snippet,
			MIN(m.from_name) as from_name,
			COUNT(*) as message_count,
			SUM(CASE WHEN m.is_read = 0 THEN 1 ELSE 0 END) as unread_count,
			MAX(CASE WHEN m.has_attachments = 1 THEN 1 ELSE 0 END) as has_attachments,
			MAX(CASE WHEN m.is_starred = 1 THEN 1 ELSE 0 END) as is_starred,
			MAX(m.date) as latest_date,
			GROUP_CONCAT(m.id) as message_ids,
			MAX(CASE WHEN m.smime_encrypted = 1 OR m.pgp_encrypted = 1 THEN 1 ELSE 0 END) as is_encrypted,
			(SELECT m2.extract_kind FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as extract_kind,
			(SELECT m2.extract_value FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as extract_value,
			(SELECT m2.extract_rule FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as extract_rule,
			(SELECT m2.id FROM messages m2 WHERE COALESCE(m2.thread_id, m2.id) = COALESCE(m.thread_id, m.id) AND m2.account_id = m.account_id ORDER BY m2.date DESC LIMIT 1) as extract_message_id,
			a.id as account_id,
			a.name as account_name,
			a.color as account_color,
			f.id as folder_id,
			f.name as folder_name,
			f.folder_type as folder_type,
			json_group_array(json_object('name', m.from_name, 'email', m.from_email, 'date', m.date, 'snippet', m.snippet)) as participants_json
		FROM messages m
		JOIN messages_fts fts ON m.rowid = fts.rowid
		` + folderJoin + `
		INNER JOIN accounts a ON f.account_id = a.id AND a.enabled = 1
		WHERE m.is_hidden = 0 AND messages_fts MATCH ?` + unifiedFolderStarredWhere(folderType) + `
		GROUP BY COALESCE(m.thread_id, m.id), a.id` +
		filterHavingClause(filter, "m.") + `
		ORDER BY latest_date DESC
		LIMIT ? OFFSET ?
	`

	searchArgs := append(append([]any{}, folderArgs...), ftsQuery, limit, offset)
	rows, err := s.db.Query(searchQuery, searchArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to search unified folder: %w", err)
	}
	defer rows.Close()

	var results []*ConversationSearchResult
	for rows.Next() {
		c := &ConversationSearchResult{}
		var latestDateStr sql.NullString
		var snippet sql.NullString
		var fromName sql.NullString
		var messageIDsStr sql.NullString
		var participantsJSON sql.NullString
		var extractKind, extractValue, extractRule, extractMessageID sql.NullString

		err := rows.Scan(
			&c.ThreadID,
			&c.Subject,
			&snippet,
			&fromName,
			&c.MessageCount,
			&c.UnreadCount,
			&c.HasAttachments,
			&c.IsStarred,
			&latestDateStr,
			&messageIDsStr,
			&c.IsEncrypted,
			&extractKind, &extractValue, &extractRule, &extractMessageID,
			&c.AccountID,
			&c.AccountName,
			&c.AccountColor,
			&c.FolderID,
			&c.FolderName,
			&c.FolderType,
			&participantsJSON,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to scan unified search result: %w", err)
		}
		if extractKind.Valid {
			c.ExtractKind = extractKind.String
		}
		if extractValue.Valid {
			c.ExtractValue = extractValue.String
		}
		if extractRule.Valid {
			c.ExtractRule = extractRule.String
		}
		if extractMessageID.Valid {
			c.ExtractMessageID = extractMessageID.String
		}

		if snippet.Valid {
			c.Snippet = snippet.String
		}
		if latestDateStr.Valid && latestDateStr.String != "" {
			c.LatestDate = parseTimeString(latestDateStr.String)
		}
		if messageIDsStr.Valid && messageIDsStr.String != "" {
			c.MessageIDs = strings.Split(messageIDsStr.String, ",")
		}

		// Resolve participants + latest snippet BEFORE highlighting so the
		// highlighted snippet reflects the latest message (#169)
		if participantsJSON.Valid {
			var latestSnippet string
			c.Participants, latestSnippet = parseParticipantsJSON(participantsJSON.String)
			if latestSnippet != "" {
				c.Snippet = latestSnippet
			}
		}

		// Apply highlighting
		c.HighlightedSubject = highlightMatches(c.Subject, query)
		c.HighlightedSnippet = highlightMatches(c.Snippet, query)
		if fromName.Valid {
			c.HighlightedFromName = highlightMatches(fromName.String, query)
		}

		results = append(results, c)
	}

	return results, totalCount, nil
}

// SearchConversationsUnifiedInbox searches across all inbox folders for all accounts
func (s *Store) SearchConversationsUnifiedInbox(query string, offset, limit int, filter string) ([]*ConversationSearchResult, int, error) {
	return s.SearchConversationsUnifiedFolder("inbox", query, offset, limit, filter)
}

// prepareFTSQuery prepares a user query for FTS5
// Handles special characters and adds prefix matching for better UX
func prepareFTSQuery(query string) string {
	// Trim whitespace
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}

	// Split into words
	words := strings.Fields(query)
	var processedWords []string

	for _, word := range words {
		// Escape special FTS5 characters
		// FTS5 special chars: " ' ( ) * : ^
		escaped := word
		escaped = strings.ReplaceAll(escaped, "\"", "\"\"")

		// Add prefix matching (word*) for partial matches
		// But only if the word doesn't already end with *
		if !strings.HasSuffix(escaped, "*") && len(escaped) > 0 {
			escaped = "\"" + escaped + "\"*"
		}

		processedWords = append(processedWords, escaped)
	}

	// Join with spaces - FTS5 will AND them together by default
	return strings.Join(processedWords, " ")
}

// highlightMatches wraps matching terms in <mark> tags for highlighting
// The text is HTML-escaped to prevent XSS
func highlightMatches(text, query string) string {
	if text == "" || query == "" {
		return html.EscapeString(text)
	}

	// First, escape the text to prevent XSS
	escapedText := html.EscapeString(text)

	// Get individual search terms
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return escapedText
	}

	// Build a regex pattern that matches any of the search terms
	// Use word boundaries for better matching
	var patterns []string
	for _, term := range terms {
		// Escape regex special characters in the term
		escaped := regexp.QuoteMeta(term)
		patterns = append(patterns, escaped)
	}

	// Create a case-insensitive pattern
	pattern := "(?i)(" + strings.Join(patterns, "|") + ")"
	re, err := regexp.Compile(pattern)
	if err != nil {
		return escapedText
	}

	// Replace matches with highlighted version
	highlighted := re.ReplaceAllStringFunc(escapedText, func(match string) string {
		return "<mark>" + match + "</mark>"
	})

	return highlighted
}
