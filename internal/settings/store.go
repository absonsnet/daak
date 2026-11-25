// Package settings provides global application settings storage
package settings

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/absonsnet/daak/internal/database"
	"github.com/absonsnet/daak/internal/logging"
	"github.com/absonsnet/daak/internal/translate"
	"github.com/rs/zerolog"
	_ "modernc.org/sqlite"
)

// Known setting keys
const (
	KeyReadReceiptResponsePolicy       = "read_receipt_response_policy"
	KeyMarkAsReadDelay                 = "mark_as_read_delay"
	KeyMessageListDensity              = "message_list_density"
	KeyMessageListSortOrder            = "message_list_sort_order"
	KeyThemeMode                       = "theme_mode"
	KeyShowTitleBar                    = "show_title_bar"
	KeyTermsAccepted                   = "terms_accepted"
	KeyRunBackground                   = "run_background"
	KeyStartHidden                     = "start_hidden"
	KeyShowUnreadBadge                 = "show_unread_badge"                   // unread count badge on the Dock/taskbar icon and the tray icon (default on)
	KeySaveRecipientsToAccountContacts = "save_recipients_to_account_contacts" // save new send recipients to the sending account's own contacts (default on)
	KeyAutostart                       = "autostart"
	KeyLanguage                        = "language"
	KeyComposerMode                    = "composer_mode"
	KeyMailtoMode                      = "mailto_mode"
	KeyComposerFormat                  = "composer_format"
	KeyNativeTitleBar                  = "native_titlebar"
	KeyWindowControlsSide              = "window_controls_side" // "auto" | "left" | "right"; manual override for the title-bar button side
	KeyAlwaysLoadImages                = "always_load_images"
	KeyDarkMailContent                 = "dark_mail_content"
	KeyDarkComposerBody                = "dark_composer_body"
	KeyAccentBarUnread                 = "accent_bar_unread"
	KeyAccentUnreadStyle               = "accent_unread_style" // "dot" | "glowdot" | "bar"; applies when accent_bar_unread is on
	KeyShowMessageListCircles          = "show_message_list_circles"
	KeyShowMessageListProfilePics      = "show_message_list_profile_pics" // render contact photos in the message-list avatar slot (default off)
	KeyAlwaysShowMessageCheckbox       = "always_show_message_checkbox"   // reserve a fixed checkbox column instead of the hover/swipe slide-reveal (default off)
	KeyShowViewerCircles               = "show_viewer_circles"
	KeyShowMessagePreviewPane          = "show_message_preview_pane"        // show the conversation viewer pane beside the message list (default on)
	KeyMessageOpenMode                 = "message_open_mode"                // how a message opens when the preview pane is off: "tab" | "dialog" | "popup" (default "tab")
	KeyInboxTabDefault                 = "inbox_tab_default"                // default category tab in Inbox / Unified Inbox: "all" | "personal" | "automated" (default "all")
	KeyLastSeenVersion                 = "last_seen_version"                // for "What's new in this version" launch dialog
	KeyOAuthWarningDisabled            = "oauth_warning_disabled"           // user toggled "Don't show again" on the missing-OAuth-creds launch warning
	KeyNotificationDeniedNoticeShown   = "notification_denied_notice_shown" // one-time "notifications are blocked" notice already shown (R28)
	KeySpellcheckEnabled               = "spellcheck_enabled"               // composer spellcheck master toggle (defaults on)
	KeyCodeChipClickMode               = "code_chip_click_mode"             // "copy_mark_read_delete" | "copy_mark_read" | "copy_only"
	KeyAttentionEnabled                = "attention_enabled"                // Needs attention strip master toggle (default on)
	KeyAttentionTypeCode               = "attention_type_verification_code"
	KeyAttentionTypeFlight             = "attention_type_flight_changed"
	KeyAttentionTypePayment            = "attention_type_payment_failed"
	KeyAttentionTypeDelivery           = "attention_type_delivery_problem"
	KeyOtpClipboardClearSeconds        = "otp_clipboard_clear_seconds" // seconds until Daak clears a copied code (0 = never)
	// Undo Send: how long a sent message waits in the Outbox before
	// the worker dispatches it. The only allowed values are the ones
	// the Settings UI offers (5/10/20/30).
	KeyUndoSendDelay         = "undo_send_delay"
	KeySpellcheckLanguages   = "spellcheck_languages"    // JSON array of enabled dictionary codes, e.g. ["en","de"]
	KeySpellcheckCustomWords = "spellcheck_custom_words" // JSON array of user-added dictionary words

	// Translation (Phase D). The mode ladder is: OS engine (Tier 1)
	// where one exists, Bergamot WASM (Tier 2), online (Tier 3,
	// consent-gated). The default stays offline.
	KeyTranslateEnabled        = "translate_enabled"         // master toggle (default on)
	KeyTranslateMode           = "translate_mode"            // "prefer_offline" | "offline_only" | "online" (default "prefer_offline")
	KeyTranslateTargetLanguage = "translate_target_language" // "system" (default) or an ISO 639-1 code
	KeyTranslateOnlineProvider = "translate_online_provider" // "google" (default) | "deepl"
	KeyTranslateOnlineConsent  = "translate_online_consent"  // JSON object: provider -> true when the user consented
)

// Extension enable/disable keys. Format: extension_<name>_enabled.
// All extensions default to disabled — minimalists see no UI changes until
// they explicitly opt in. Phase 1 reserves keys only for confirmed first-
// party extensions (Calendar, Contacts, AI Writing).
const (
	KeyExtensionCalendarEnabled  = "extension_calendar_enabled"
	KeyExtensionContactsEnabled  = "extension_contacts_enabled"
	KeyExtensionAIWritingEnabled = "extension_ai_writing_enabled"
	KeyExtensionMailMergeEnabled = "extension_mail-merge_enabled"
)

// AllExtensionKeys is the list of all known first-party extension names. Add
// a new extension's name here when its enable/disable key is reserved above.
// IsExtensionEnabled / SetExtensionEnabled work on names from this list.
var AllExtensionKeys = []string{
	"calendar",
	"contacts",
	"ai-writing",
	"mail-merge",
}

// Density values for message list
const (
	DensityMicro    = "micro"
	DensityCompact  = "compact"
	DensityStandard = "standard"
	DensityLarge    = "large"
)

// DefaultMessageListDensity is the default density
const DefaultMessageListDensity = DensityStandard

// Sort order values for message list
const (
	SortOrderNewest = "newest"
	SortOrderOldest = "oldest"
)

// DefaultMessageListSortOrder is the default sort order
const DefaultMessageListSortOrder = SortOrderNewest

// Theme mode values
const (
	ThemeModeSystem              = "system"
	ThemeModeLight               = "light"                // Default light purple
	ThemeModeLightBlue           = "light-blue"           // New
	ThemeModeLightOrange         = "light-orange"         // New
	ThemeModeLightBalanced       = "light-balanced"       // New
	ThemeModeAdwaitaLight        = "adwaita-light"        // Adwaita Light
	ThemeModeBreezeLight         = "breeze-light"         // Breeze Light
	ThemeModeDark                = "dark"                 // Default dark purple
	ThemeModeDarkGray            = "dark-gray"            // New
	ThemeModeDarkBalanced        = "dark-balanced"        // New
	ThemeModeAdwaitaDark         = "adwaita-dark"         // Adwaita Dark
	ThemeModeBreezeDark          = "breeze-dark"          // Breeze Dark
	ThemeModeCatppuccinLatte     = "catppuccin-latte"     // Catppuccin Latte
	ThemeModeCatppuccinFrappe    = "catppuccin-frappe"    // Catppuccin Frappé
	ThemeModeCatppuccinMacchiato = "catppuccin-macchiato" // Catppuccin Macchiato
	ThemeModeCatppuccinMocha     = "catppuccin-mocha"     // Catppuccin Mocha
	ThemeModeDracula             = "dracula"              // Dracula
	ThemeModeGithubLight         = "github-light"         // GitHub Light
	ThemeModeGithubDark          = "github-dark"          // GitHub Dark
	ThemeModeGithubSoftDark      = "github-soft-dark"     // GitHub Soft Dark
	ThemeModeTokyoNight          = "tokyo-night"          // Tokyo Night
	ThemeModeNordLight           = "nord-light"           // Nord Light
	ThemeModeNordDark            = "nord-dark"            // Nord Dark
	ThemeModePopLight            = "pop-light"            // Pop! Light
	ThemeModePopDark             = "pop-dark"             // Pop! Dark
	ThemeModeYaruLight           = "yaru-light"           // Yaru Light
	ThemeModeYaruDark            = "yaru-dark"            // Yaru Dark
	ThemeModeVSCodeLight         = "vs-code-light"        // VS Code Light
	ThemeModeVSCodeDark          = "vs-code-dark"         // VS Code Dark
)

// DefaultThemeMode is the default theme mode
const DefaultThemeMode = ThemeModeSystem

// Message open mode values (how a message opens when the preview pane is off)
const (
	MessageOpenModeTab    = "tab"
	MessageOpenModeDialog = "dialog"
	MessageOpenModePopup  = "popup"
)

// DefaultMessageOpenMode is the default message open mode
const DefaultMessageOpenMode = MessageOpenModeTab

// DefaultShowMessagePreviewPane is the default preview-pane visibility
const DefaultShowMessagePreviewPane = true

// DefaultShowUnreadBadge is the default unread-badge visibility
const DefaultShowUnreadBadge = true

// DefaultSaveRecipientsToAccountContacts is the default for saving new send
// recipients to the sending account's own contacts
const DefaultSaveRecipientsToAccountContacts = true

// Composer mode values
const (
	ComposerModeInline   = "inline"
	ComposerModeDetached = "detached"
)

// DefaultComposerMode is the default compose mode
const DefaultComposerMode = ComposerModeInline

// Composer format values
const (
	ComposerFormatRich  = "rich"
	ComposerFormatPlain = "plain"
)

// DefaultComposerFormat is the default composer format
const DefaultComposerFormat = ComposerFormatRich

// Policy values for read receipts
const (
	PolicyNever  = "never"
	PolicyAsk    = "ask"
	PolicyAlways = "always"
)

// Default mark as read delay in milliseconds (1 second)
const DefaultMarkAsReadDelay = 1000

// Store provides settings persistence operations
type Store struct {
	db  *database.DB
	log zerolog.Logger
}

// NewStore creates a new settings store
func NewStore(db *database.DB) *Store {
	return &Store{
		db:  db,
		log: logging.WithComponent("settings-store"),
	}
}

// Get retrieves a setting value by key
func (s *Store) Get(key string) (string, error) {
	var value string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get setting %s: %w", key, err)
	}
	return value, nil
}

// setKeys writes several settings in one transaction: either all of
// them land or none does (fourth-audit finding 57). Paired settings
// used to be saved in two steps, so a failure on the second write
// left the first already stored — half-applied state the UI then
// reported as the whole change.
func (s *Store) setKeys(pairs ...[2]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin settings transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, p := range pairs {
		if _, err := tx.Exec(`
			INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value
		`, p[0], p[1]); err != nil {
			return fmt.Errorf("failed to set setting %s: %w", p[0], err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit settings transaction: %w", err)
	}
	for _, p := range pairs {
		s.log.Debug().Str("key", p[0]).Str("value", p[1]).Msg("Setting updated")
	}
	return nil
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// SetRunBackgroundAndStartHidden persists run_background and
// start_hidden together: disabling background mode also
// force-disables start_hidden, and the two must never land
// separately.
func (s *Store) SetRunBackgroundAndStartHidden(runBackground, startHidden bool) error {
	return s.setKeys(
		[2]string{KeyRunBackground, boolString(runBackground)},
		[2]string{KeyStartHidden, boolString(startHidden)},
	)
}

// SetComposerModeAndMailtoMode persists composer_mode and mailto_mode
// together: setting the composer to detached also auto-sets mailto
// mode to detached, and the two must never land separately.
func (s *Store) SetComposerModeAndMailtoMode(mode, mailtoMode string) error {
	if mode != ComposerModeInline && mode != ComposerModeDetached {
		return fmt.Errorf("invalid composer mode: %s (must be 'inline' or 'detached')", mode)
	}
	if mailtoMode != ComposerModeInline && mailtoMode != ComposerModeDetached {
		return fmt.Errorf("invalid mailto mode: %s (must be 'inline' or 'detached')", mailtoMode)
	}
	return s.setKeys(
		[2]string{KeyComposerMode, mode},
		[2]string{KeyMailtoMode, mailtoMode},
	)
}

// Set sets a setting value
func (s *Store) Set(key, value string) error {
	_, err := s.db.Exec(`
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, key, value)
	if err != nil {
		return fmt.Errorf("failed to set setting %s: %w", key, err)
	}

	s.log.Debug().Str("key", key).Str("value", value).Msg("Setting updated")
	return nil
}

// IsExtensionEnabled returns whether the given first-party extension is
// enabled. Unknown / not-yet-set extensions return (false, nil) — the
// app should treat "not present in settings" as disabled.
func (s *Store) IsExtensionEnabled(name string) (bool, error) {
	value, err := s.Get("extension_" + name + "_enabled")
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

// SetExtensionEnabled writes the enable/disable flag for the given first-party
// extension. The Wails-bound App method that wraps this is the only entry
// point the frontend uses to toggle extensions on/off.
func (s *Store) SetExtensionEnabled(name string, enabled bool) error {
	v := "false"
	if enabled {
		v = "true"
	}
	return s.Set("extension_"+name+"_enabled", v)
}

// Per-account default address kinds (#341): composer prefills for BCC and
// Reply-To. Keys follow the dynamic default_<kind>_<accountID> /
// default_<kind>_enabled_<accountID> format. The kind whitelist keeps the
// Wails-exposed setters from writing arbitrary settings keys.
const (
	DefaultAddressCc      = "cc"
	DefaultAddressBcc     = "bcc"
	DefaultAddressReplyTo = "replyto"
)

func validateDefaultAddressKind(kind string) error {
	switch kind {
	case DefaultAddressCc, DefaultAddressBcc, DefaultAddressReplyTo:
		return nil
	}
	return fmt.Errorf("invalid default address kind: %s", kind)
}

// GetDefaultAddressEnabled returns whether the per-account default address
// of the given kind is enabled. Not-yet-set accounts default to disabled.
func (s *Store) GetDefaultAddressEnabled(kind, accountID string) (bool, error) {
	if err := validateDefaultAddressKind(kind); err != nil {
		return false, err
	}
	value, err := s.Get("default_" + kind + "_enabled_" + accountID)
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

// SetDefaultAddressEnabled writes the per-account toggle. The saved address
// value is kept when disabling so re-enabling restores it.
func (s *Store) SetDefaultAddressEnabled(kind, accountID string, enabled bool) error {
	if err := validateDefaultAddressKind(kind); err != nil {
		return err
	}
	v := "false"
	if enabled {
		v = "true"
	}
	return s.Set("default_"+kind+"_enabled_"+accountID, v)
}

// GetDefaultAddress returns the account's default address list (comma/
// semicolon-separated, as entered) — or "" when the account's toggle is off,
// so callers (the composer) need no separate enabled check.
func (s *Store) GetDefaultAddress(kind, accountID string) (string, error) {
	enabled, err := s.GetDefaultAddressEnabled(kind, accountID)
	if err != nil {
		return "", err
	}
	if !enabled {
		return "", nil
	}
	value, err := s.Get("default_" + kind + "_" + accountID)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

// SetDefaultAddress stores the account's default address list.
func (s *Store) SetDefaultAddress(kind, accountID, value string) error {
	if err := validateDefaultAddressKind(kind); err != nil {
		return err
	}
	return s.Set("default_"+kind+"_"+accountID, strings.TrimSpace(value))
}

// GetReadReceiptResponsePolicy returns the current read receipt response policy
func (s *Store) GetReadReceiptResponsePolicy() (string, error) {
	value, err := s.Get(KeyReadReceiptResponsePolicy)
	if err != nil {
		return PolicyAsk, err
	}
	if value == "" {
		return PolicyAsk, nil // Default
	}
	return value, nil
}

// SetReadReceiptResponsePolicy sets the read receipt response policy
func (s *Store) SetReadReceiptResponsePolicy(policy string) error {
	// Validate policy
	if policy != PolicyNever && policy != PolicyAsk && policy != PolicyAlways {
		return fmt.Errorf("invalid policy: %s (must be 'never', 'ask', or 'always')", policy)
	}
	return s.Set(KeyReadReceiptResponsePolicy, policy)
}

// GetMarkAsReadDelay returns the delay before marking messages as read (in milliseconds)
// Returns: -1 = manual only, 0 = immediate, >0 = delay in ms
func (s *Store) GetMarkAsReadDelay() (int, error) {
	value, err := s.Get(KeyMarkAsReadDelay)
	if err != nil {
		return DefaultMarkAsReadDelay, err
	}
	if value == "" {
		return DefaultMarkAsReadDelay, nil
	}
	delay, err := strconv.Atoi(value)
	if err != nil {
		return DefaultMarkAsReadDelay, nil
	}
	return delay, nil
}

// SetMarkAsReadDelay sets the delay before marking messages as read (in milliseconds)
// Valid values: -1 (manual only), 0 (immediate), or 100-5000 (delay in ms)
func (s *Store) SetMarkAsReadDelay(delayMs int) error {
	// Validate delay
	if delayMs < -1 {
		return fmt.Errorf("invalid delay: %d (must be -1, 0, or 100-5000)", delayMs)
	}
	if delayMs > 0 && delayMs < 100 {
		return fmt.Errorf("invalid delay: %d (minimum non-zero delay is 100ms)", delayMs)
	}
	if delayMs > 5000 {
		return fmt.Errorf("invalid delay: %d (maximum delay is 5000ms)", delayMs)
	}
	return s.Set(KeyMarkAsReadDelay, strconv.Itoa(delayMs))
}

// GetMessageListDensity returns the current message list density setting
func (s *Store) GetMessageListDensity() (string, error) {
	value, err := s.Get(KeyMessageListDensity)
	if err != nil {
		return DefaultMessageListDensity, err
	}
	if value == "" {
		return DefaultMessageListDensity, nil
	}
	return value, nil
}

// SetMessageListDensity sets the message list density
func (s *Store) SetMessageListDensity(density string) error {
	if density != DensityMicro && density != DensityCompact && density != DensityStandard && density != DensityLarge {
		return fmt.Errorf("invalid density: %s (must be 'micro', 'compact', 'standard', or 'large')", density)
	}
	return s.Set(KeyMessageListDensity, density)
}

// GetAccentBarUnread returns whether the accent bar for unread messages is enabled
func (s *Store) GetAccentBarUnread() (bool, error) {
	value, err := s.Get(KeyAccentBarUnread)
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

// SetAccentBarUnread enables or disables the accent bar for unread messages
func (s *Store) SetAccentBarUnread(enabled bool) error {
	v := "false"
	if enabled {
		v = "true"
	}
	return s.Set(KeyAccentBarUnread, v)
}

// GetAccentUnreadStyle returns the unread accent style ("dot", "glowdot" or
// "bar"). When never set, users who already had the accent enabled keep the
// legacy full bar; everyone else defaults to the dot.
func (s *Store) GetAccentUnreadStyle() (string, error) {
	value, err := s.Get(KeyAccentUnreadStyle)
	if err != nil {
		return "dot", err
	}
	if value != "" {
		return value, nil
	}
	enabled, err := s.GetAccentBarUnread()
	if err != nil {
		return "dot", err
	}
	if enabled {
		return "bar", nil
	}
	return "dot", nil
}

// SetAccentUnreadStyle sets the unread accent style
func (s *Store) SetAccentUnreadStyle(style string) error {
	switch style {
	case "dot", "glowdot", "bar":
		return s.Set(KeyAccentUnreadStyle, style)
	}
	return fmt.Errorf("invalid accent unread style: %s (must be 'dot', 'glowdot' or 'bar')", style)
}

// GetShowMessagePreviewPane returns whether the conversation viewer pane
// is shown beside the message list. Default: true.
func (s *Store) GetShowMessagePreviewPane() (bool, error) {
	value, err := s.Get(KeyShowMessagePreviewPane)
	if err != nil {
		return DefaultShowMessagePreviewPane, err
	}
	if value == "" {
		return DefaultShowMessagePreviewPane, nil
	}
	return value == "true", nil
}

// SetShowMessagePreviewPane shows or hides the conversation viewer pane
func (s *Store) SetShowMessagePreviewPane(enabled bool) error {
	v := "false"
	if enabled {
		v = "true"
	}
	return s.Set(KeyShowMessagePreviewPane, v)
}

// GetMessageOpenMode returns how a message opens when the preview pane is
// off ("tab", "dialog" or "popup"). Default: "tab". An unknown stored value
// falls back to "tab" rather than failing the settings load.
func (s *Store) GetMessageOpenMode() (string, error) {
	value, err := s.Get(KeyMessageOpenMode)
	if err != nil {
		return DefaultMessageOpenMode, err
	}
	switch value {
	case "", DefaultMessageOpenMode:
		return DefaultMessageOpenMode, nil
	case MessageOpenModeDialog, MessageOpenModePopup:
		return value, nil
	default:
		return DefaultMessageOpenMode, nil
	}
}

// SetMessageOpenMode sets how a message opens when the preview pane is off
func (s *Store) SetMessageOpenMode(mode string) error {
	switch mode {
	case MessageOpenModeTab, MessageOpenModeDialog, MessageOpenModePopup:
		return s.Set(KeyMessageOpenMode, mode)
	default:
		return fmt.Errorf("invalid message open mode: %s (must be 'tab', 'dialog' or 'popup')", mode)
	}
}

// Inbox tab values (Phase E). "all" is the default: Daak must not hide
// mail behind a classifier on day one; the user picks their own default
// in Settings → Categories once they trust it.
const (
	InboxTabAll       = "all"
	InboxTabPersonal  = "personal"
	InboxTabAutomated = "automated"
)

// GetInboxTabDefault returns the default category tab for Inbox /
// Unified Inbox. Default: "all". An unknown stored value falls back to
// "all" rather than failing the settings load.
func (s *Store) GetInboxTabDefault() (string, error) {
	value, err := s.Get(KeyInboxTabDefault)
	if err != nil {
		return InboxTabAll, err
	}
	switch value {
	case "", InboxTabAll, InboxTabPersonal, InboxTabAutomated:
		if value == "" {
			return InboxTabAll, nil
		}
		return value, nil
	default:
		return InboxTabAll, nil
	}
}

// SetInboxTabDefault sets the default category tab for Inbox / Unified Inbox.
func (s *Store) SetInboxTabDefault(tab string) error {
	switch tab {
	case InboxTabAll, InboxTabPersonal, InboxTabAutomated:
		return s.Set(KeyInboxTabDefault, tab)
	default:
		return fmt.Errorf("invalid inbox tab: %s (must be 'all', 'personal' or 'automated')", tab)
	}
}

// GetShowMessageListCircles returns whether colored sender circles
// are shown in the message list. Default: true.
func (s *Store) GetShowMessageListCircles() (bool, error) {
	value, err := s.Get(KeyShowMessageListCircles)
	if err != nil {
		return true, err
	}
	if value == "" {
		return true, nil
	}
	return value == "true", nil
}

// SetShowMessageListCircles enables or disables colored sender circles in the message list
func (s *Store) SetShowMessageListCircles(enabled bool) error {
	v := "false"
	if enabled {
		v = "true"
	}
	return s.Set(KeyShowMessageListCircles, v)
}

// GetShowMessageListProfilePics returns whether contact profile pictures are
// shown in the message-list avatar slot (in place of the colored circle).
// Default: false.
func (s *Store) GetShowMessageListProfilePics() (bool, error) {
	value, err := s.Get(KeyShowMessageListProfilePics)
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

// SetShowMessageListProfilePics enables or disables contact profile pictures in the message list
func (s *Store) SetShowMessageListProfilePics(enabled bool) error {
	v := "false"
	if enabled {
		v = "true"
	}
	return s.Set(KeyShowMessageListProfilePics, v)
}

// GetAlwaysShowMessageCheckbox returns whether the message list reserves a
// fixed checkbox column (legacy layout) instead of the hover/swipe
// slide-reveal. Default: false.
func (s *Store) GetAlwaysShowMessageCheckbox() (bool, error) {
	value, err := s.Get(KeyAlwaysShowMessageCheckbox)
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

// SetAlwaysShowMessageCheckbox enables or disables the always-reserved checkbox column
func (s *Store) SetAlwaysShowMessageCheckbox(enabled bool) error {
	v := "false"
	if enabled {
		v = "true"
	}
	return s.Set(KeyAlwaysShowMessageCheckbox, v)
}

// GetShowViewerCircles returns whether colored sender circles
// are shown in the conversation viewer. Default: true.
func (s *Store) GetShowViewerCircles() (bool, error) {
	value, err := s.Get(KeyShowViewerCircles)
	if err != nil {
		return true, err
	}
	if value == "" {
		return true, nil
	}
	return value == "true", nil
}

// SetShowViewerCircles enables or disables colored sender circles in the conversation viewer
func (s *Store) SetShowViewerCircles(enabled bool) error {
	v := "false"
	if enabled {
		v = "true"
	}
	return s.Set(KeyShowViewerCircles, v)
}

// GetMessageListSortOrder returns the current message list sort order
func (s *Store) GetMessageListSortOrder() (string, error) {
	value, err := s.Get(KeyMessageListSortOrder)
	if err != nil {
		return DefaultMessageListSortOrder, err
	}
	if value == "" {
		return DefaultMessageListSortOrder, nil
	}
	return value, nil
}

// SetMessageListSortOrder sets the message list sort order
func (s *Store) SetMessageListSortOrder(sortOrder string) error {
	if sortOrder != SortOrderNewest && sortOrder != SortOrderOldest {
		return fmt.Errorf("invalid sort order: %s (must be 'newest' or 'oldest')", sortOrder)
	}
	return s.Set(KeyMessageListSortOrder, sortOrder)
}

// GetThemeMode returns the current theme mode setting
func (s *Store) GetThemeMode() (string, error) {
	value, err := s.Get(KeyThemeMode)
	if err != nil {
		return DefaultThemeMode, err
	}
	if value == "" {
		return DefaultThemeMode, nil
	}
	return value, nil
}

// SetThemeMode sets the theme mode
func (s *Store) SetThemeMode(mode string) error {
	switch mode {
	case ThemeModeSystem, ThemeModeLight, ThemeModeLightBlue, ThemeModeLightOrange, ThemeModeLightBalanced,
		ThemeModeAdwaitaLight, ThemeModeBreezeLight,
		ThemeModeDark, ThemeModeDarkGray, ThemeModeDarkBalanced, ThemeModeAdwaitaDark, ThemeModeBreezeDark,
		ThemeModeCatppuccinLatte, ThemeModeCatppuccinFrappe, ThemeModeCatppuccinMacchiato, ThemeModeCatppuccinMocha,
		ThemeModeDracula, ThemeModeGithubLight, ThemeModeGithubDark, ThemeModeGithubSoftDark, ThemeModeTokyoNight,
		ThemeModeNordLight, ThemeModeNordDark, ThemeModePopLight, ThemeModePopDark,
		ThemeModeVSCodeLight, ThemeModeVSCodeDark, ThemeModeYaruLight, ThemeModeYaruDark:
		return s.Set(KeyThemeMode, mode)
	default:
		return fmt.Errorf("invalid theme mode: %s (must be 'system', 'light', 'light-blue', 'light-orange', 'light-balanced', 'adwaita-light', 'breeze-light', 'dark', 'dark-gray', 'dark-balanced', 'adwaita-dark', 'breeze-dark', 'catppuccin-latte', 'catppuccin-frappe', 'catppuccin-macchiato', 'catppuccin-mocha', 'dracula', 'github-light', 'github-dark', 'github-soft-dark', 'tokyo-night', 'nord-light', 'nord-dark', 'pop-light', 'pop-dark', 'vs-code-light', 'vs-code-dark', 'yaru-light', or 'yaru-dark')", mode)
	}
}

// GetShowTitleBar returns whether the title bar should be shown
func (s *Store) GetShowTitleBar() (bool, error) {
	value, err := s.Get(KeyShowTitleBar)
	if err != nil {
		return true, err // Default to true (shown)
	}
	if value == "" {
		return true, nil // Default to true (shown)
	}
	return value == "true", nil
}

// SetShowTitleBar sets whether the title bar should be shown
func (s *Store) SetShowTitleBar(show bool) error {
	value := "false"
	if show {
		value = "true"
	}
	return s.Set(KeyShowTitleBar, value)
}

// GetTermsAccepted returns whether the user has accepted the terms of service
func (s *Store) GetTermsAccepted() (bool, error) {
	value, err := s.Get(KeyTermsAccepted)
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

// SetTermsAccepted sets whether the user has accepted the terms of service
func (s *Store) SetTermsAccepted(accepted bool) error {
	value := "false"
	if accepted {
		value = "true"
	}
	return s.Set(KeyTermsAccepted, value)
}

// GetLastSeenVersion returns the Daak version that was running the last time
// the "What's new in this version" dialog was acknowledged with OK. Empty
// string means it's never been acknowledged (e.g. fresh install).
func (s *Store) GetLastSeenVersion() (string, error) {
	return s.Get(KeyLastSeenVersion)
}

// SetLastSeenVersion records the current Daak version as acknowledged so the
// What's New dialog doesn't fire again until the next version upgrade.
func (s *Store) SetLastSeenVersion(version string) error {
	return s.Set(KeyLastSeenVersion, version)
}

// GetOAuthWarningDisabled returns whether the user has opted out of the
// missing-OAuth-creds launch warning via the dialog's "Don't show again"
// toggle. Defaults to false on first launch (key unset).
func (s *Store) GetOAuthWarningDisabled() (bool, error) {
	value, err := s.Get(KeyOAuthWarningDisabled)
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

// SetOAuthWarningDisabled persists the user's "Don't show again" choice from
// the OAuth-credentials-missing launch warning.
func (s *Store) SetOAuthWarningDisabled(disabled bool) error {
	value := "false"
	if disabled {
		value = "true"
	}
	return s.Set(KeyOAuthWarningDisabled, value)
}

// GetNotificationDeniedNoticeShown returns whether the one-time
// "notifications are blocked" notice has already been shown. Defaults
// to false on first launch (key unset).
func (s *Store) GetNotificationDeniedNoticeShown() (bool, error) {
	value, err := s.Get(KeyNotificationDeniedNoticeShown)
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

// SetNotificationDeniedNoticeShown records that the one-time
// "notifications are blocked" notice was shown, so it never fires again.
func (s *Store) SetNotificationDeniedNoticeShown(shown bool) error {
	value := "false"
	if shown {
		value = "true"
	}
	return s.Set(KeyNotificationDeniedNoticeShown, value)
}

// GetRunBackground returns whether Daak should keep running when the window is closed
func (s *Store) GetRunBackground() (bool, error) {
	value, err := s.Get(KeyRunBackground)
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

// SetRunBackground sets whether Daak should keep running when the window is closed
func (s *Store) SetRunBackground(enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	return s.Set(KeyRunBackground, value)
}

// GetShowUnreadBadge returns whether the unread count badge is shown on the
// Dock/taskbar icon and the tray icon. Default: true.
func (s *Store) GetShowUnreadBadge() (bool, error) {
	value, err := s.Get(KeyShowUnreadBadge)
	if err != nil {
		return DefaultShowUnreadBadge, err
	}
	if value == "" {
		return DefaultShowUnreadBadge, nil
	}
	return value == "true", nil
}

// SetShowUnreadBadge sets whether the unread count badge is shown on the
// Dock/taskbar icon and the tray icon.
func (s *Store) SetShowUnreadBadge(enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	return s.Set(KeyShowUnreadBadge, value)
}

// GetSaveRecipientsToAccountContacts returns whether new send recipients are
// saved to the sending account's own contacts (Google Contacts, Microsoft
// contacts, or the account's CardDAV address book) in addition to the local
// collected contacts. Default: true.
func (s *Store) GetSaveRecipientsToAccountContacts() (bool, error) {
	value, err := s.Get(KeySaveRecipientsToAccountContacts)
	if err != nil {
		return DefaultSaveRecipientsToAccountContacts, err
	}
	if value == "" {
		return DefaultSaveRecipientsToAccountContacts, nil
	}
	return value == "true", nil
}

// SetSaveRecipientsToAccountContacts sets whether new send recipients are
// saved to the sending account's own contacts.
func (s *Store) SetSaveRecipientsToAccountContacts(enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	return s.Set(KeySaveRecipientsToAccountContacts, value)
}

// GetStartHidden returns whether Daak should start with the window hidden
func (s *Store) GetStartHidden() (bool, error) {
	value, err := s.Get(KeyStartHidden)
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

// SetStartHidden sets whether Daak should start with the window hidden
func (s *Store) SetStartHidden(enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	return s.Set(KeyStartHidden, value)
}

// GetAutostart returns whether Daak should start on login
func (s *Store) GetAutostart() (bool, error) {
	value, err := s.Get(KeyAutostart)
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

// SetAutostart sets whether Daak should start on login
func (s *Store) SetAutostart(enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	return s.Set(KeyAutostart, value)
}

// GetSpellcheckEnabled returns whether composer spellcheck is on. Defaults to
// true (only an explicit "false" disables it) so it's on out of the box.
func (s *Store) GetSpellcheckEnabled() (bool, error) {
	value, err := s.Get(KeySpellcheckEnabled)
	if err != nil {
		return true, err
	}
	return value != "false", nil
}

// SetSpellcheckEnabled sets the composer spellcheck master toggle
func (s *Store) SetSpellcheckEnabled(enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	return s.Set(KeySpellcheckEnabled, value)
}

// GetSpellcheckLanguages returns the enabled dictionary codes. Empty (unset)
// lets the frontend fall back to the UI language + English.
func (s *Store) GetSpellcheckLanguages() ([]string, error) {
	value, err := s.Get(KeySpellcheckLanguages)
	if err != nil {
		return nil, err
	}
	if value == "" {
		return []string{}, nil
	}
	var langs []string
	if err := json.Unmarshal([]byte(value), &langs); err != nil {
		return []string{}, nil // tolerate a corrupt value rather than fail settings load
	}
	return langs, nil
}

// SetSpellcheckLanguages stores the enabled dictionary codes as a JSON array
func (s *Store) SetSpellcheckLanguages(langs []string) error {
	if langs == nil {
		langs = []string{}
	}
	data, err := json.Marshal(langs)
	if err != nil {
		return err
	}
	return s.Set(KeySpellcheckLanguages, string(data))
}

// GetSpellcheckCustomWords returns the user-added dictionary words
func (s *Store) GetSpellcheckCustomWords() ([]string, error) {
	value, err := s.Get(KeySpellcheckCustomWords)
	if err != nil {
		return nil, err
	}
	if value == "" {
		return []string{}, nil
	}
	var words []string
	if err := json.Unmarshal([]byte(value), &words); err != nil {
		return []string{}, nil
	}
	return words, nil
}

// AddSpellcheckCustomWord appends a word to the user dictionary (no-op if already present)
func (s *Store) AddSpellcheckCustomWord(word string) error {
	words, err := s.GetSpellcheckCustomWords()
	if err != nil {
		return err
	}
	for _, w := range words {
		if w == word {
			return nil
		}
	}
	data, err := json.Marshal(append(words, word))
	if err != nil {
		return err
	}
	return s.Set(KeySpellcheckCustomWords, string(data))
}

// RemoveSpellcheckCustomWord removes a word from the user dictionary (no-op if absent)
func (s *Store) RemoveSpellcheckCustomWord(word string) error {
	words, err := s.GetSpellcheckCustomWords()
	if err != nil {
		return err
	}
	kept := make([]string, 0, len(words))
	for _, w := range words {
		if w != word {
			kept = append(kept, w)
		}
	}
	data, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	return s.Set(KeySpellcheckCustomWords, string(data))
}

// Code chip click modes (Settings -> Messages -> "Code chip click").
const (
	CodeChipClickCopyMarkReadDelete = "copy_mark_read_delete" // copy, mark read, move to Trash (default)
	CodeChipClickCopyMarkRead       = "copy_mark_read"        // copy + mark read
	CodeChipClickCopyOnly           = "copy_only"             // copy only
)

// DefaultCodeChipClickMode is the default code-chip click behaviour.
const DefaultCodeChipClickMode = CodeChipClickCopyMarkReadDelete

// DefaultOtpClipboardClearSeconds is the default clipboard hygiene delay.
const DefaultOtpClipboardClearSeconds = 60

// GetCodeChipClickMode returns the code-chip click mode. Unknown stored
// values fall back to the default rather than failing the settings load.
func (s *Store) GetCodeChipClickMode() (string, error) {
	value, err := s.Get(KeyCodeChipClickMode)
	if err != nil {
		return DefaultCodeChipClickMode, err
	}
	switch value {
	case "", CodeChipClickCopyMarkReadDelete, CodeChipClickCopyMarkRead, CodeChipClickCopyOnly:
		if value == "" {
			return DefaultCodeChipClickMode, nil
		}
		return value, nil
	default:
		return DefaultCodeChipClickMode, nil
	}
}

// SetCodeChipClickMode sets the code-chip click mode.
func (s *Store) SetCodeChipClickMode(mode string) error {
	switch mode {
	case CodeChipClickCopyMarkReadDelete, CodeChipClickCopyMarkRead, CodeChipClickCopyOnly:
		return s.Set(KeyCodeChipClickMode, mode)
	default:
		return fmt.Errorf("invalid code chip click mode: %s (must be 'copy_mark_read_delete', 'copy_mark_read' or 'copy_only')", mode)
	}
}

// GetAttentionEnabled returns whether the Needs attention strip is on.
// Default: true (only an explicit "false" disables it).
func (s *Store) GetAttentionEnabled() (bool, error) {
	value, err := s.Get(KeyAttentionEnabled)
	if err != nil {
		return true, err
	}
	return value != "false", nil
}

// SetAttentionEnabled enables or disables the Needs attention strip.
func (s *Store) SetAttentionEnabled(enabled bool) error {
	v := "false"
	if enabled {
		v = "true"
	}
	return s.Set(KeyAttentionEnabled, v)
}

// attentionTypeKey maps an attention type to its per-type toggle key.
func attentionTypeKey(attentionType string) (string, error) {
	switch attentionType {
	case "verification_code":
		return KeyAttentionTypeCode, nil
	case "flight_changed":
		return KeyAttentionTypeFlight, nil
	case "payment_failed":
		return KeyAttentionTypePayment, nil
	case "delivery_problem":
		return KeyAttentionTypeDelivery, nil
	default:
		return "", fmt.Errorf("unknown attention type: %s", attentionType)
	}
}

// GetAttentionTypeEnabled returns whether one attention type is on.
// Default: true for every type.
func (s *Store) GetAttentionTypeEnabled(attentionType string) (bool, error) {
	key, err := attentionTypeKey(attentionType)
	if err != nil {
		return false, err
	}
	value, err := s.Get(key)
	if err != nil {
		return true, err
	}
	return value != "false", nil
}

// SetAttentionTypeEnabled enables or disables one attention type.
func (s *Store) SetAttentionTypeEnabled(attentionType string, enabled bool) error {
	key, err := attentionTypeKey(attentionType)
	if err != nil {
		return err
	}
	v := "false"
	if enabled {
		v = "true"
	}
	return s.Set(key, v)
}

// GetOtpClipboardClearSeconds returns how long Daak waits before clearing
// a copied one-time code from the clipboard (0 = never). Default 60.
func (s *Store) GetOtpClipboardClearSeconds() (int, error) {
	value, err := s.Get(KeyOtpClipboardClearSeconds)
	if err != nil {
		return DefaultOtpClipboardClearSeconds, err
	}
	if value == "" {
		return DefaultOtpClipboardClearSeconds, nil
	}
	secs, err := strconv.Atoi(value)
	if err != nil || secs < 0 {
		return DefaultOtpClipboardClearSeconds, nil
	}
	return secs, nil
}

// SetOtpClipboardClearSeconds sets the clipboard-clear delay (0 = never).
func (s *Store) SetOtpClipboardClearSeconds(seconds int) error {
	if seconds < 0 || seconds > 3600 {
		return fmt.Errorf("invalid clipboard clear delay: %d (must be 0-3600)", seconds)
	}
	return s.Set(KeyOtpClipboardClearSeconds, strconv.Itoa(seconds))
}

// DefaultUndoSendDelay is the Outbox wait before a queued send is
// dispatched: 5 seconds, long enough to hit Undo, short enough that
// sending still feels immediate.
const DefaultUndoSendDelay = 5

// AllowedUndoSendDelays is the full set the Settings UI offers.
var AllowedUndoSendDelays = []int{5, 10, 20, 30}

// GetUndoSendDelay returns the Outbox wait in seconds. Default 5; a
// missing or out-of-set stored value also reads as the default.
func (s *Store) GetUndoSendDelay() (int, error) {
	value, err := s.Get(KeyUndoSendDelay)
	if err != nil {
		return DefaultUndoSendDelay, err
	}
	if value == "" {
		return DefaultUndoSendDelay, nil
	}
	secs, err := strconv.Atoi(value)
	if err != nil {
		return DefaultUndoSendDelay, nil
	}
	for _, allowed := range AllowedUndoSendDelays {
		if secs == allowed {
			return secs, nil
		}
	}
	return DefaultUndoSendDelay, nil
}

// SetUndoSendDelay sets the Outbox wait in seconds. Only 5, 10, 20
// and 30 are accepted — the values the Settings UI offers.
func (s *Store) SetUndoSendDelay(seconds int) error {
	for _, allowed := range AllowedUndoSendDelays {
		if seconds == allowed {
			return s.Set(KeyUndoSendDelay, strconv.Itoa(seconds))
		}
	}
	return fmt.Errorf("invalid undo-send delay: %d (must be one of 5, 10, 20, 30)", seconds)
}

// Translation settings (Phase D).

// Translation mode values.
const (
	TranslateModePreferOffline = "prefer_offline" // offline tiers first; online only via an explicit per-message button
	TranslateModeOfflineOnly   = "offline_only"   // Tiers 1-2 only, ever; text never leaves the machine
	TranslateModeOnline        = "online"         // straight to the online provider (consent still applies)
)

// DefaultTranslateMode is the default translation mode.
const DefaultTranslateMode = TranslateModePreferOffline

// DefaultTranslateTargetLanguage is the default target language.
const DefaultTranslateTargetLanguage = "system"

// DefaultTranslateOnlineProvider is the default online provider.
const DefaultTranslateOnlineProvider = "google"

// GetTranslateEnabled returns the translation master toggle.
// Default: true (only an explicit "false" disables it).
func (s *Store) GetTranslateEnabled() (bool, error) {
	value, err := s.Get(KeyTranslateEnabled)
	if err != nil {
		return true, err
	}
	return value != "false", nil
}

// SetTranslateEnabled enables or disables translation offers.
func (s *Store) SetTranslateEnabled(enabled bool) error {
	v := "false"
	if enabled {
		v = "true"
	}
	return s.Set(KeyTranslateEnabled, v)
}

// GetTranslateMode returns the translation mode. Unknown stored values
// fall back to the default rather than failing the settings load.
func (s *Store) GetTranslateMode() (string, error) {
	value, err := s.Get(KeyTranslateMode)
	if err != nil {
		return DefaultTranslateMode, err
	}
	switch value {
	case "", DefaultTranslateMode:
		return DefaultTranslateMode, nil
	case TranslateModeOfflineOnly, TranslateModeOnline:
		return value, nil
	default:
		return DefaultTranslateMode, nil
	}
}

// SetTranslateMode sets the translation mode. It takes effect
// immediately for the next Translate press; no restart or re-sync.
func (s *Store) SetTranslateMode(mode string) error {
	switch mode {
	case TranslateModePreferOffline, TranslateModeOfflineOnly, TranslateModeOnline:
		return s.Set(KeyTranslateMode, mode)
	default:
		return fmt.Errorf("invalid translation mode: %s (must be 'prefer_offline', 'offline_only' or 'online')", mode)
	}
}

// GetTranslateTargetLanguage returns the stored target language:
// "system" (default) or an ISO 639-1 code.
func (s *Store) GetTranslateTargetLanguage() (string, error) {
	value, err := s.Get(KeyTranslateTargetLanguage)
	if err != nil {
		return DefaultTranslateTargetLanguage, err
	}
	if value == "" {
		return DefaultTranslateTargetLanguage, nil
	}
	return value, nil
}

// SetTranslateTargetLanguage sets the target language ("system" or a
// code from the picker list in internal/translate).
func (s *Store) SetTranslateTargetLanguage(language string) error {
	if language == DefaultTranslateTargetLanguage || translate.ValidTargetLanguage(language) {
		return s.Set(KeyTranslateTargetLanguage, language)
	}
	return fmt.Errorf("invalid translation target language: %s", language)
}

// GetTranslateOnlineProvider returns the online provider ("google" or
// "deepl"). Default: "google".
func (s *Store) GetTranslateOnlineProvider() (string, error) {
	value, err := s.Get(KeyTranslateOnlineProvider)
	if err != nil {
		return DefaultTranslateOnlineProvider, err
	}
	switch value {
	case "", DefaultTranslateOnlineProvider:
		return DefaultTranslateOnlineProvider, nil
	case "deepl":
		return value, nil
	default:
		return DefaultTranslateOnlineProvider, nil
	}
}

// SetTranslateOnlineProvider sets the online provider.
func (s *Store) SetTranslateOnlineProvider(provider string) error {
	switch provider {
	case "google", "deepl":
		return s.Set(KeyTranslateOnlineProvider, provider)
	default:
		return fmt.Errorf("invalid online translation provider: %s (must be 'google' or 'deepl')", provider)
	}
}

// GetTranslateOnlineConsent returns whether the user consented to
// sending message text to the given online provider. Consent is
// per-provider and revocable; never assumed.
func (s *Store) GetTranslateOnlineConsent(provider string) (bool, error) {
	value, err := s.Get(KeyTranslateOnlineConsent)
	if err != nil {
		return false, err
	}
	if value == "" {
		return false, nil
	}
	var consents map[string]bool
	if err := json.Unmarshal([]byte(value), &consents); err != nil {
		return false, nil // tolerate a corrupt value rather than fail settings load
	}
	return consents[provider], nil
}

// SetTranslateOnlineConsent records or revokes consent for one online
// provider. Switching provider clears no cache.
func (s *Store) SetTranslateOnlineConsent(provider string, granted bool) error {
	switch provider {
	case "google", "deepl":
	default:
		return fmt.Errorf("invalid online translation provider: %s", provider)
	}
	value, err := s.Get(KeyTranslateOnlineConsent)
	if err != nil {
		return err
	}
	consents := map[string]bool{}
	if value != "" {
		_ = json.Unmarshal([]byte(value), &consents)
	}
	if granted {
		consents[provider] = true
	} else {
		delete(consents, provider)
	}
	data, err := json.Marshal(consents)
	if err != nil {
		return err
	}
	return s.Set(KeyTranslateOnlineConsent, string(data))
}

// GetLanguage returns the saved language preference (locale code)
// Returns empty string if not set (frontend uses system detection)
func (s *Store) GetLanguage() (string, error) {
	return s.Get(KeyLanguage)
}

// SetLanguage sets the language preference (locale code, e.g. "en", "zh-TW", "zh-CN")
func (s *Store) SetLanguage(language string) error {
	return s.Set(KeyLanguage, language)
}

// GetComposerMode returns the default compose mode ("inline" or "detached")
func (s *Store) GetComposerMode() (string, error) {
	value, err := s.Get(KeyComposerMode)
	if err != nil {
		return DefaultComposerMode, err
	}
	if value == "" {
		return DefaultComposerMode, nil
	}
	return value, nil
}

// SetComposerMode sets the default compose mode
func (s *Store) SetComposerMode(mode string) error {
	if mode != ComposerModeInline && mode != ComposerModeDetached {
		return fmt.Errorf("invalid composer mode: %s (must be 'inline' or 'detached')", mode)
	}
	return s.Set(KeyComposerMode, mode)
}

// GetMailtoMode returns the external mailto link handling mode ("inline" or "detached")
func (s *Store) GetMailtoMode() (string, error) {
	value, err := s.Get(KeyMailtoMode)
	if err != nil {
		return DefaultComposerMode, err
	}
	if value == "" {
		return DefaultComposerMode, nil
	}
	return value, nil
}

// SetMailtoMode sets the external mailto link handling mode
func (s *Store) SetMailtoMode(mode string) error {
	if mode != ComposerModeInline && mode != ComposerModeDetached {
		return fmt.Errorf("invalid mailto mode: %s (must be 'inline' or 'detached')", mode)
	}
	return s.Set(KeyMailtoMode, mode)
}

// GetComposerFormat returns the default composer format ("rich" or "plain")
func (s *Store) GetComposerFormat() (string, error) {
	value, err := s.Get(KeyComposerFormat)
	if err != nil {
		return DefaultComposerFormat, err
	}
	if value == "" {
		return DefaultComposerFormat, nil
	}
	return value, nil
}

// SetComposerFormat sets the default composer format
func (s *Store) SetComposerFormat(format string) error {
	if format != ComposerFormatRich && format != ComposerFormatPlain {
		return fmt.Errorf("invalid composer format: %s (must be 'rich' or 'plain')", format)
	}
	return s.Set(KeyComposerFormat, format)
}

// WindowControlsSide values for the title-bar button-side override.
const (
	WindowControlsSideAuto  = "auto"  // Follow the detected OS / desktop-environment layout
	WindowControlsSideLeft  = "left"  // Force the buttons to the left
	WindowControlsSideRight = "right" // Force the buttons to the right
)

// GetWindowControlsSide returns the manual title-bar button-side override.
// Defaults to "auto" (follow the detected layout). An unknown stored value
// also falls back to "auto" rather than failing the settings load.
func (s *Store) GetWindowControlsSide() (string, error) {
	value, err := s.Get(KeyWindowControlsSide)
	if err != nil {
		return WindowControlsSideAuto, err
	}
	switch value {
	case "", WindowControlsSideAuto:
		return WindowControlsSideAuto, nil
	case WindowControlsSideLeft, WindowControlsSideRight:
		return value, nil
	default:
		return WindowControlsSideAuto, nil
	}
}

// SetWindowControlsSide sets the manual title-bar button-side override.
func (s *Store) SetWindowControlsSide(side string) error {
	switch side {
	case WindowControlsSideAuto, WindowControlsSideLeft, WindowControlsSideRight:
		return s.Set(KeyWindowControlsSide, side)
	default:
		return fmt.Errorf("invalid window controls side: %s (must be 'auto', 'left', or 'right')", side)
	}
}

// GetNativeTitleBar returns whether the native OS title bar should be used
func (s *Store) GetNativeTitleBar() (bool, error) {
	value, err := s.Get(KeyNativeTitleBar)
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

// SetNativeTitleBar sets whether the native OS title bar should be used
func (s *Store) SetNativeTitleBar(enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	return s.Set(KeyNativeTitleBar, value)
}

// GetAlwaysLoadImages returns whether remote images should always be loaded
func (s *Store) GetAlwaysLoadImages() (bool, error) {
	value, err := s.Get(KeyAlwaysLoadImages)
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

// SetAlwaysLoadImages sets whether remote images should always be loaded
func (s *Store) SetAlwaysLoadImages(enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	return s.Set(KeyAlwaysLoadImages, value)
}

// GetDarkMailContent returns whether email content should be visually darkened
// while Daak is in dark mode. Off by default.
func (s *Store) GetDarkMailContent() (bool, error) {
	value, err := s.Get(KeyDarkMailContent)
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

// SetDarkMailContent persists the dark-mail-content toggle.
func (s *Store) SetDarkMailContent(enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	return s.Set(KeyDarkMailContent, value)
}

// GetDarkComposerBody returns whether the composer message body should use a
// dark background while Daak is in dark mode. Off by default (white body).
func (s *Store) GetDarkComposerBody() (bool, error) {
	value, err := s.Get(KeyDarkComposerBody)
	if err != nil {
		return false, err
	}
	return value == "true", nil
}

// SetDarkComposerBody persists the dark-composer-body toggle.
func (s *Store) SetDarkComposerBody(enabled bool) error {
	value := "false"
	if enabled {
		value = "true"
	}
	return s.Set(KeyDarkComposerBody, value)
}

// ReadNativeTitleBar opens the database directly to read the native_titlebar setting.
// Used in main.go before wails.Run() when the full DB isn't initialized yet.
// Returns false on any error (first run, missing DB, etc.).
func ReadNativeTitleBar(dbPath string) bool {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return false
	}
	defer db.Close()

	var value string
	err = db.QueryRow("SELECT value FROM settings WHERE key = ?", KeyNativeTitleBar).Scan(&value)
	if err != nil {
		return false
	}
	return value == "true"
}
