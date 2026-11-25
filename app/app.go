package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	goSync "sync"
	"time"

	extaiwritingbe "github.com/absonsnet/daak/extensions/ai-writing/backend"
	extcalendarbe "github.com/absonsnet/daak/extensions/calendar/backend"
	extcontactsbe "github.com/absonsnet/daak/extensions/contacts/backend"
	extmailmergebe "github.com/absonsnet/daak/extensions/mail-merge/backend"
	"github.com/absonsnet/daak/internal/account"
	"github.com/absonsnet/daak/internal/appstate"
	"github.com/absonsnet/daak/internal/carddav"
	"github.com/absonsnet/daak/internal/certificate"
	"github.com/absonsnet/daak/internal/contact"
	coreapi "github.com/absonsnet/daak/internal/core/api/v1"
	"github.com/absonsnet/daak/internal/credentials"
	"github.com/absonsnet/daak/internal/database"
	"github.com/absonsnet/daak/internal/draft"
	extauth "github.com/absonsnet/daak/internal/extensions/auth"
	extcompose "github.com/absonsnet/daak/internal/extensions/compose"
	extmail "github.com/absonsnet/daak/internal/extensions/mail"
	extui "github.com/absonsnet/daak/internal/extensions/ui"
	"github.com/absonsnet/daak/internal/folder"
	"github.com/absonsnet/daak/internal/imap"
	"github.com/absonsnet/daak/internal/ipc"
	"github.com/absonsnet/daak/internal/kit/davutil"
	"github.com/absonsnet/daak/internal/logging"
	"github.com/absonsnet/daak/internal/message"
	"github.com/absonsnet/daak/internal/notification"
	"github.com/absonsnet/daak/internal/oauth2"
	"github.com/absonsnet/daak/internal/pgp"
	"github.com/absonsnet/daak/internal/platform"
	"github.com/absonsnet/daak/internal/reaction"
	"github.com/absonsnet/daak/internal/settings"
	"github.com/absonsnet/daak/internal/smime"
	"github.com/absonsnet/daak/internal/sync"
	"github.com/absonsnet/daak/internal/tray"
	"github.com/absonsnet/daak/internal/undo"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// MailtoData holds parsed mailto: URL data
type MailtoData struct {
	To      []string `json:"to"`
	Cc      []string `json:"cc"`
	Bcc     []string `json:"bcc"`
	Subject string   `json:"subject"`
	Body    string   `json:"body"`
}

// maxMailtoURLLength is the maximum allowed length for a mailto URL (2KB)
const maxMailtoURLLength = 2048

// maxEmailLength is the maximum allowed length for an email address (RFC 5321)
const maxEmailLength = 254

// maxSubjectLength is the maximum allowed length for a subject (RFC 5322 line length)
const maxSubjectLength = 998

// maxBodyLength is the maximum allowed body length (64KB)
const maxBodyLength = 64 * 1024

// ParseMailtoURL parses a mailto: URL and extracts email data with input validation.
// Returns nil if the URL is invalid or doesn't start with "mailto:".
func ParseMailtoURL(rawURL string) *MailtoData {
	if len(rawURL) > maxMailtoURLLength {
		return nil
	}
	if !strings.HasPrefix(strings.ToLower(rawURL), "mailto:") {
		return nil
	}

	data := &MailtoData{}

	// Remove mailto: prefix
	rest := rawURL[7:]

	// Split into address part and query part
	addrPart := rest
	queryPart := ""
	if queryStart := strings.Index(rest, "?"); queryStart != -1 {
		addrPart = rest[:queryStart]
		queryPart = rest[queryStart+1:]
	}

	// Parse To addresses (comma-separated, URL-encoded)
	if addrPart != "" {
		decoded, err := url.QueryUnescape(addrPart)
		if err == nil {
			addrPart = decoded
		}
		for _, addr := range strings.Split(addrPart, ",") {
			addr = sanitizeField(strings.TrimSpace(addr))
			if addr == "" || !isValidEmail(addr) {
				continue
			}
			data.To = append(data.To, addr)
		}
	}

	// Parse query parameters
	if queryPart == "" {
		return data
	}

	params, err := url.ParseQuery(queryPart)
	if err != nil {
		return data
	}

	if subject := params.Get("subject"); subject != "" {
		subject = sanitizeField(subject)
		if len(subject) > maxSubjectLength {
			subject = subject[:maxSubjectLength]
		}
		data.Subject = subject
	}
	if body := params.Get("body"); body != "" {
		body = sanitizeField(body)
		if len(body) > maxBodyLength {
			body = body[:maxBodyLength]
		}
		data.Body = body
	}
	if cc := params.Get("cc"); cc != "" {
		for _, addr := range strings.Split(cc, ",") {
			addr = sanitizeField(strings.TrimSpace(addr))
			if addr == "" || !isValidEmail(addr) {
				continue
			}
			data.Cc = append(data.Cc, addr)
		}
	}
	if bcc := params.Get("bcc"); bcc != "" {
		for _, addr := range strings.Split(bcc, ",") {
			addr = sanitizeField(strings.TrimSpace(addr))
			if addr == "" || !isValidEmail(addr) {
				continue
			}
			data.Bcc = append(data.Bcc, addr)
		}
	}

	return data
}

// sanitizeField strips CR and LF characters to prevent header injection.
func sanitizeField(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return s
}

// isValidEmail performs basic email validation: must contain @, non-empty local-part
// and domain, and total length ≤ 254 chars (RFC 5321).
func isValidEmail(email string) bool {
	if len(email) > maxEmailLength {
		return false
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return false
	}
	domain := email[at+1:]
	return len(domain) > 0
}

// App struct holds the application state and dependencies
type App struct {
	// Embedded extension bridges. Each extension contributes its Wails-bound
	// surface via a Bridge struct embedded here; Go's method promotion makes
	// those bridge methods appear on App so Wails reflection picks them up.
	// All extension logic lives in extensions/<name>/backend/bridge.go; the
	// only host-side touch is this field + ~10 LOC of wiring in
	// app/extension_<name>.go.
	//
	// Convention: bridge methods MUST be named with the extension's prefix
	// (`Contacts_`, `Calendar_`, etc.) so embedded methods can never collide
	// across extensions. See docs/EXTENSIONS.md.
	//
	// Each extension's bridge struct must use a DISTINCT type name (e.g.,
	// `Bridge`, `CalendarBridge`) so the anonymous-embed field names are
	// unique — Go derives the field name from the type's last identifier,
	// and two fields named `Bridge` would collide.
	*extcontactsbe.ContactsBridge
	*extcalendarbe.CalendarBridge
	*extaiwritingbe.AIWritingBridge
	*extmailmergebe.MailMergeBridge

	ctx context.Context

	// ready is the backend-up signal the frontend polls before mounting the
	// main app. False until Startup completes. The boot splash in
	// index.html stays visible while ready is false; flipping it true is
	// what lets main.ts proceed to mount(App). See IsReady().
	ready bool

	// Paths
	paths *platform.Paths

	// Database
	db *database.DB

	// Stores
	accountStore        *account.Store
	folderStore         *folder.Store
	messageStore        *message.Store
	attachmentStore     *message.AttachmentStore
	reactionStore       *reaction.Store
	contactStore        *contact.Store
	draftStore          *draft.Store
	settingsStore       *settings.Store
	appStateStore       *appstate.Store
	imageAllowlistStore *settings.ImageAllowlistStore

	// IMAP
	imapPool   *imap.Pool
	syncEngine *sync.Engine

	// Background sync (polling + IDLE)
	syncScheduler *sync.Scheduler
	idleManager   *imap.IdleManager

	// Credentials (keyring with fallback)
	credStore *credentials.Store

	// Certificate trust store (TOFU)
	certStore *certificate.Store

	// CardDAV
	carddavStore     *carddav.Store
	carddavSyncer    *carddav.Syncer
	carddavScheduler *carddav.Scheduler

	// Extension system. Each extension's Wails-bound surface is embedded
	// into App via its Bridge struct (declared at the top of this struct
	// definition); the *Extension field below is the lightweight lifecycle
	// handle the host's knownExtensions Register loop iterates.
	authBroker      *extauth.Broker           // coreapi.Auth impl for extensions
	mailAPI         *extmail.API              // coreapi.Mail impl wrapping core stores
	composerAPI     *extcompose.API           // coreapi.Composer impl wrapping OpenComposerWindow
	uiRegistry      *extui.Registry           // coreapi.UI impl: rail tabs, account-setup hooks, ...
	contactsExt     *extcontactsbe.Extension  // Contacts lifecycle handle (manifest + Register only)
	calendarExt     *extcalendarbe.Extension  // Calendar lifecycle handle (manifest + Register only)
	aiWritingExt    *extaiwritingbe.Extension // AI Writing lifecycle handle (manifest + Register only)
	mailMergeExt    *extmailmergebe.Extension // Mail Merge lifecycle handle (manifest + Register only)
	knownExtensions []coreapi.Extension       // all first-party extensions, iterated by ListExtensions
	extensionUnregs []coreapi.Unregister      // teardown funcs returned from each Extension.Register

	// coreapi.EventBus implementation, lazily constructed on first
	// Core.Events() call (via eventBusInitOnce). Extensions consume via
	// core.Events().Publish / .Subscribe; the bus also tees to Wails
	// frontend events. System-event wire-up (sleep/network → bus) inside
	// the bus is also lazy — first system:* Subscribe triggers it.
	eventBus         *eventBusCoreImpl
	eventBusInitOnce goSync.Once

	// windowControlsLayout is the detected window-controls layout (side,
	// order, style), computed once on first use — the desktop-environment
	// probe runs subprocesses, so it is cached rather than re-run on
	// every call.
	windowControlsLayout     platform.WindowControlsLayout
	windowControlsLayoutOnce goSync.Once

	// S/MIME
	smimeStore     *smime.Store
	smimeSigner    *smime.Signer
	smimeVerifier  *smime.Verifier
	smimeEncryptor *smime.Encryptor
	smimeDecryptor *smime.Decryptor

	// PGP
	pgpStore     *pgp.Store
	pgpSigner    *pgp.Signer
	pgpVerifier  *pgp.Verifier
	pgpEncryptor *pgp.Encryptor
	pgpDecryptor *pgp.Decryptor

	// Shared draft operations (used by both App and ComposerApp)
	draftOps draftOps

	// Undo Send: background send + persistent Outbox (main process
	// only runs the worker; see app/sendqueue.go).
	sendQueue *sendQueueService

	// Shared compose operations (used by both App and ComposerApp)
	composeOps composeOps

	// Undo system
	undoStack *undo.Stack

	// IPC for multi-window support (composer windows)
	ipcServer   ipc.Server
	ipcTokenMgr *ipc.TokenManager

	// OAuth2 manager
	oauth2Manager *oauth2.Manager

	// Temporary OAuth token storage (for pending account creation)
	pendingOAuthTokens *oauth2.TokenResponse
	pendingOAuthEmail  string

	// Pending custom ("bring your own app") OAuth provider config, set by
	// StartCustomOAuthFlow and consumed by CompleteCustomOAuthAccountSetup so the
	// endpoints/creds can be persisted per account for later refresh/reauth.
	pendingCustomProvider *oauth2.ProviderConfig

	// Temporary OAuth token storage (for pending contact source creation)
	pendingContactSourceOAuthTokens   *oauth2.TokenResponse
	pendingContactSourceOAuthEmail    string
	pendingContactSourceOAuthProvider string

	// Google Contacts API client (for OAuth accounts)
	googleContactsClient *contact.GoogleContactsClient

	// Pending mailto: URL data (from command line)
	PendingMailto *MailtoData

	// Full-text search indexer
	ftsIndexer *message.FTSIndexer

	// Sync management - tracks active syncs per account for cancel-and-restart
	syncContexts    map[string]context.CancelFunc // keyed by "accountID:folderID"
	syncLastRequest map[string]time.Time          // last sync request time for debounce
	syncCancelled   bool                          // set by CancelAllSyncs to stop SyncAllComplete loop
	wakeSyncing     bool                          // guards syncAfterWake against concurrent calls
	syncMu          goSync.Mutex                  // protects sync maps

	// Suppresses the IDLE echo of our own flag writes: when Daak STOREs a flag
	// change it stamps the account here, so the incoming IDLE FETCH echo of that
	// same change doesn't trigger a re-sync (only other clients' changes do).
	ownFlagMu       goSync.Mutex
	ownFlagChangeAt map[string]time.Time // accountID -> last own flag STORE time
	ownExpungeAt    map[string]time.Time // accountID -> last own move/delete IMAP op time

	// Draft IMAP sync goroutine tracking — cancel in-flight syncDraftToIMAP
	draftSyncContexts map[string]context.CancelFunc // keyed by draft ID
	draftSyncDone     map[string]chan struct{}      // closed when goroutine exits

	// Sleep/wake detection for auto-sync on wake
	sleepWakeMonitor platform.SleepWakeMonitor

	// Network connectivity monitoring (event-driven, zero polling)
	networkMonitor platform.NetworkMonitor

	// System theme detection (XDG Settings Portal on Linux)
	themeMonitor platform.ThemeMonitor

	// Desktop notifications with click handling
	notifier notification.Notifier

	// UseDirectDBus forces direct D-Bus notifications instead of portal (Linux only)
	useDirectDBus bool

	// Single-instance lock (set by main before wails.Run)
	SingleInstanceLock platform.SingleInstanceLock

	// Autostart manager
	autostartMgr platform.AutostartManager

	// Window hidden state (background mode)
	windowHidden bool

	// Last maximized state observed while the window was visible. The
	// live maximized query is unreliable once the window is hidden or
	// minimized (minimizing clears WS_MAXIMIZE on Windows), so
	// saveWindowGeometry trusts this cache instead of the live query in
	// those states. Seeded from the stored geometry at startup.
	lastWindowMaximized      bool
	lastWindowMaximizedKnown bool

	// pendingShow remembers a single-instance "show" command that arrived
	// before Startup set the runtime context (see
	// HandleSingleInstanceCommand). showMu guards it: the lock's
	// accept-loop goroutine and Startup race on it.
	showMu      goSync.Mutex
	pendingShow bool

	// System tray icon (shown whenever Run in Background is on, so a
	// hidden window can be reopened). Icon bytes set by main before
	// wails.Run; the Tray itself is created by NewApp.
	TrayIconICO []byte // Windows .ico bytes
	TrayIconPNG []byte // PNG bytes for macOS/Linux
	tray        *tray.Tray
	// trayStarted reports whether the tray icon started this session.
	// It is set once by startTray and never cleared — it must NOT be
	// read as "the tray is up right now": a tray that dies mid-session
	// leaves it stale. canHideOnClose and GetStartHiddenActive
	// re-check liveness through tray.Alive() before trusting it.
	trayStarted bool

	// Paths the user chose in a save dialog this session; the post-save
	// toast's Open file / Open folder actions may open them (#474).
	recentSaveMu    goSync.Mutex
	recentSavePaths map[string]struct{}
}

// NewApp creates a new App application struct
func NewApp(useDirectDBus bool) *App {
	return &App{
		useDirectDBus: useDirectDBus,
		tray:          tray.New(),
	}
}

// StartupDialogInfo holds the user-facing dialog content for a startup
// failure: title, body, and an optional URL the dialog should expose
// behind an action button. main.go's preflight glue inspects the URL
// field to decide between ShowDialog and ShowDialogWithLink.
type StartupDialogInfo struct {
	Title       string
	Text        string
	ActionLabel string // button text; empty means no action button
	ActionURL   string // URL opened when the action button is clicked
}

// StartupDialogInfoFor returns the user-facing dialog content for a startup
// error returned by App.Preflight. Known sentinel types get a tailored
// message + action URL; everything else falls back to a generic message.
//
// URLs in the returned Text are rendered as clickable links by dialog
// backends that support markup (currently zenity on Linux via Pango).
// Other backends show the URL as selectable plain text and use the
// ActionURL field to drive an "Open Docs" button.
func StartupDialogInfoFor(err error) StartupDialogInfo {
	const docsRollbackURL = "https://github.com/absonsnet/daak/blob/main/docs/SQL_ROLLBACK.md"

	var schemaTooNew *database.ErrSchemaTooNew
	if errors.As(err, &schemaTooNew) {
		text := fmt.Sprintf(
			platform.AppName+" cannot open your database because its schema (version %d) is newer "+
				"than this build of "+platform.AppName+" supports (max version %d).\n\n"+
				"This usually means you downgraded "+platform.AppName+". To recover, either reinstall "+
				"the newer version, or follow the rollback instructions to bring your "+
				"database back to version %d:\n\n"+
				"%s",
			schemaTooNew.DBVersion, schemaTooNew.BuildVersion, schemaTooNew.BuildVersion,
			docsRollbackURL,
		)
		return StartupDialogInfo{
			Title:       platform.AppName + " could not start",
			Text:        text,
			ActionLabel: "Open Docs",
			ActionURL:   docsRollbackURL,
		}
	}
	return StartupDialogInfo{
		Title: platform.AppName + " could not start",
		Text:  fmt.Sprintf(platform.AppName+" could not start.\n\nDetails: %v", err),
	}
}

// Preflight performs the early-startup steps that must succeed BEFORE the
// Wails window is shown: platform paths, directory creation, database open
// + migration, credential store init, and OAuth override wiring. Logging is
// already initialised by main.go (from --log-level / --log-file) by the
// time this runs. Returns an error on any failure; main.go is responsible for
// surfacing the failure to the user (via StartupDialogInfoFor + a native
// dialog) and exiting before wails.Run is called.
//
// Splitting these steps out of Startup is intentional: Wails calls Startup
// AFTER it has already created the OS window, so a failure inside Startup
// would briefly flash a half-rendered app window before the error dialog
// appears. Preflight runs in main.go before wails.Run so the user only
// ever sees the error dialog.
func (a *App) Preflight() error {
	log := logging.WithComponent("app")

	paths, err := platform.GetPaths()
	if err != nil {
		return fmt.Errorf("get platform paths: %w", err)
	}
	a.paths = paths

	if err := paths.EnsureDirectories(); err != nil {
		return fmt.Errorf("create directories: %w", err)
	}
	log.Info().
		Str("config", paths.Config).
		Str("data", paths.Data).
		Str("cache", paths.Cache).
		Msg("Initialized paths")

	db, err := database.Open(paths.DatabasePath())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	a.db = db
	log.Info().Str("path", paths.DatabasePath()).Msg("Opened database")

	if err := db.Migrate(); err != nil {
		return err
	}
	log.Info().Msg("Database migrations complete")

	credStore, err := credentials.NewStore(db.DB, paths.Data)
	if err != nil {
		return fmt.Errorf("init credential store: %w", err)
	}
	a.credStore = credStore

	// Wire user-supplied OAuth client credentials override into the oauth2
	// resolver chain. When the user has saved their own client_id + secret
	// for a given config id via Settings → OAuth Credentials, those values
	// take priority over any shipped (build-time) defaults.
	oauth2.UserOverrideLookup = func(configID string) (oauth2.ClientCredentials, bool) {
		clientID, clientSecret, ok, err := credStore.GetUserClientCreds(configID)
		if err != nil || !ok {
			return oauth2.ClientCredentials{}, false
		}
		return oauth2.ClientCredentials{ClientID: clientID, ClientSecret: clientSecret}, true
	}

	// Wire user-picked slot aliases into the oauth2 resolver chain. When the
	// user has chosen a non-default shipped option for a given config id
	// (e.g., contacts settings → "Daak mail client" reroutes google-contacts
	// onto google-mail), the resolver consults this hook after the user-
	// override step and before the provider chain.
	oauth2.SlotAliasLookup = func(configID string) (string, bool) {
		target, ok, err := credStore.GetOAuthSlotAlias(configID)
		if err != nil || !ok {
			return "", false
		}
		return target, true
	}

	// Wire the user's explicit picker choice ("custom" / "daak-shipped"
	// / "daak-mail") into the resolver. When recorded, this routes the
	// resolver by choice rather than by row presence — so switching the
	// picker between options no longer requires destroying stored values
	// to make the new option take effect.
	oauth2.ActiveChoiceLookup = func(configID string) (string, bool) {
		choice, err := credStore.GetOAuthActiveChoice(configID)
		if err != nil || choice == "" {
			return "", false
		}
		return choice, true
	}

	return nil
}

// shuttingDown tracks if shutdown has been initiated to prevent multiple triggers
var shuttingDown bool

// Startup is called when the app starts
func (a *App) Startup(ctx context.Context) {
	// The window is created hidden (main.go always passes StartHidden,
	// and the frontend shows it once ready), so record that: ShowWindow's
	// macOS guard relies on this flag to know a show is needed, and it
	// must not start out false while the window is still hidden.
	a.windowHidden = true

	// Guarded by showMu: the single-instance callback (registered in
	// runMainMode, before wails.Run) reads ctx to decide whether a "show"
	// can be honoured yet or must be remembered for later.
	a.showMu.Lock()
	a.ctx = ctx
	a.showMu.Unlock()

	// Quit cleanly on SIGTERM/SIGINT/SIGHUP even in background mode (#460).
	a.watchTerminationSignals()

	// Let the Windows installer ask a running Daak to quit before an update
	// or uninstall, even in background mode. No-op outside Windows.
	platform.StartQuitEventWatcher(a.QuitApp)

	// Single-instance activation is registered in runMainMode, before
	// wails.Run (see HandleSingleInstanceCommand) — the lock accepts from
	// the moment TryLock succeeds, so registering here was too late: a
	// second launch during startup had its "show" silently dropped.

	// Logging, paths, db open, migrations, and credential store are all
	// initialized in Preflight (called from main.go before wails.Run). By
	// the time Startup runs, a.paths, a.db, and a.credStore are non-nil.
	log := logging.WithComponent("app")
	db := a.db

	// Initialize stores
	a.accountStore = account.NewStore(db)
	a.folderStore = folder.NewStore(db)
	a.messageStore = message.NewStore(db)
	a.attachmentStore = message.NewAttachmentStore(db)
	a.reactionStore = reaction.NewStore(db)
	a.contactStore = contact.NewStore(db.DB)
	a.draftStore = draft.NewStore(db)
	a.settingsStore = settings.NewStore(db)
	a.appStateStore = appstate.NewStore(db.DB)
	a.imageAllowlistStore = settings.NewImageAllowlistStore(db)

	// System tray icon: shown whenever Run in Background is on, so a
	// window hidden with CloseWindow/BeforeClose can be reopened from
	// the tray — including start-hidden launches, where there is no
	// window at all until the user asks for one.
	a.startTrayIfBackground()

	// Unread badge on the Dock/taskbar icon (and the tray icon, when it
	// is up): set it from the current count once at startup. Later
	// refreshes come from emitFolderCountsChanged.
	a.refreshBadge()

	// Restore the persisted window position (size/maximized are applied via
	// wails.Run options in main.go; position can only be set post-creation).
	// The window is still hidden at this point, so no visible jump.
	a.restoreWindowPosition()

	// A second instance may have asked us to show the window while we
	// were still starting up (see HandleSingleInstanceCommand). Honour it
	// now that the context exists and the window position is restored.
	// (If start-hidden is active the frontend may hide the window again
	// at mount — the tray icon remains the way back in that case.)
	a.showMu.Lock()
	pending := a.pendingShow
	a.pendingShow = false
	a.showMu.Unlock()
	if pending {
		a.ShowWindow()
	}

	// Scale database connection pool based on number of accounts
	a.updateDBConnectionPool()

	// Initialize vCard scanner for contact autocomplete
	// Scans known Linux paths for .vcf files with 20 minute cache TTL
	vcardScanner := contact.NewVCardScanner(contact.DefaultVCardPaths(), 20*time.Minute)
	a.contactStore.SetVCardScanner(vcardScanner)
	// Trigger initial scan in background
	go func() {
		if _, err := vcardScanner.Scan(); err != nil {
			log.Debug().Err(err).Msg("vCard scan failed")
		}
	}()

	// Initialize CardDAV support (will be fully set up after credStore is initialized)
	a.carddavStore = carddav.NewStore(db.DB)

	// Initialize certificate trust store (TOFU)
	a.certStore = certificate.NewStore(db.DB)

	// Initialize S/MIME support
	a.smimeStore = smime.NewStore(db.DB, log)
	a.smimeSigner = smime.NewSigner(a.smimeStore, a.credStore, log)
	a.smimeVerifier = smime.NewVerifier(a.smimeStore, log)
	a.smimeEncryptor = smime.NewEncryptor(a.smimeStore, a.credStore, log)
	a.smimeDecryptor = smime.NewDecryptor(a.smimeStore, a.credStore, log)

	// Initialize PGP support
	a.pgpStore = pgp.NewStore(db.DB, log)
	a.pgpSigner = pgp.NewSigner(a.pgpStore, a.credStore, log)
	a.pgpVerifier = pgp.NewVerifier(a.pgpStore, log)
	a.pgpEncryptor = pgp.NewEncryptor(a.pgpStore, a.credStore, log)
	a.pgpDecryptor = pgp.NewDecryptor(a.pgpStore, a.credStore, log)

	// Initialize IMAP connection pool
	poolConfig := imap.DefaultPoolConfig()
	a.imapPool = imap.NewPool(poolConfig, a.getIMAPCredentials)
	// Learn-once: a server that rejected LOGIN until an IMAP ID was sent
	// (Coremail/NetEase, #457) marks the account so later connections
	// identify up front instead of failing once per login.
	a.imapPool.SetIDRequiredCallback(func(accountID string) {
		log := logging.WithComponent("app")
		if err := a.accountStore.SetIMAPRequireID(accountID, true); err != nil {
			log.Warn().Err(err).Str("accountID", accountID).Msg("Failed to record IMAP ID requirement")
			return
		}
		log.Info().Str("accountID", accountID).Msg("Account marked as requiring IMAP ID before login")
	})

	// Initialize shared draft operations (used by both App and ComposerApp)
	a.draftOps = draftOps{
		accountStore:   a.accountStore,
		folderStore:    a.folderStore,
		messageStore:   a.messageStore,
		draftStore:     a.draftStore,
		imapPool:       a.imapPool,
		smimeSigner:    a.smimeSigner,
		smimeEncryptor: a.smimeEncryptor,
		smimeDecryptor: a.smimeDecryptor,
		pgpSigner:      a.pgpSigner,
		pgpEncryptor:   a.pgpEncryptor,
		pgpDecryptor:   a.pgpDecryptor,
	}

	// Initialize sync engine
	a.syncEngine = sync.NewEngine(a.imapPool, a.accountStore, a.folderStore, a.messageStore, a.attachmentStore)

	// Wire S/MIME and PGP verifiers into sync engine for signature verification during body parsing
	a.syncEngine.SetSMIMEVerifier(a.smimeVerifier)
	a.syncEngine.SetPGPVerifier(a.pgpVerifier)
	a.syncEngine.SetReactionStore(a.reactionStore)

	// Set up sync progress callback to emit events to frontend
	a.syncEngine.SetProgressCallback(func(progress sync.SyncProgress) {
		wailsRuntime.EventsEmit(ctx, "sync:progress", map[string]interface{}{
			"accountId": progress.AccountID,
			"folderId":  progress.FolderID,
			"fetched":   progress.Fetched,
			"total":     progress.Total,
			"phase":     progress.Phase,
		})
	})

	// Folder-list sync pushes changed unread counts for folders that are not
	// message-synced (those emit their own counts from SyncFolder); without
	// this a scheduled pass never refreshes their sidebar badges.
	a.syncEngine.SetFolderCountsCallback(func(_ string, counts map[string]int) {
		a.emitFolderCountsChanged(counts)
	})

	// One-off enrichment backfill for mail synced before migration 46
	// (Phase A). Runs once in the background; a failed run retries on
	// the next launch. Silent when there is nothing to do.
	go func() {
		defer recoverPanic("app.backfill", "enrichment backfill")
		if n, err := a.RunEnrichmentBackfill(); err != nil {
			log.Warn().Err(err).Msg("Enrichment backfill failed (will retry next launch)")
		} else if n > 0 {
			log.Info().Int("enriched", n).Msg("Enrichment backfill complete")
		}
	}()

	// Start connection pool cleanup routine
	a.imapPool.StartCleanupRoutine(ctx)

	// Start periodic WAL checkpoint routine to prevent WAL file from growing too large
	go a.db.StartCheckpointRoutine(ctx)

	// Initialize CardDAV syncer and scheduler
	a.carddavSyncer = carddav.NewSyncer(a.carddavStore, a.credStore)
	a.carddavScheduler = carddav.NewScheduler(a.carddavSyncer, a.carddavStore)

	// Initialize the OAuth2 manager BEFORE constructing the Auth Broker — the
	// broker captures a.oauth2Manager into bearerRefreshTransport at construction
	// time, so a later assignment would leave every extension's HTTP client with
	// a nil oauthManager and SIGSEGV on the first 401-triggered token refresh.
	if a.oauth2Manager == nil {
		a.oauth2Manager = oauth2.NewManager()
	}

	// Extension system (Phase 1 infrastructure). Per the lightweight-by-default
	// invariant, NO extension stores are opened here. Each extension's Bridge
	// lazy-initializes its stores + per-extension SQLite + API on the first
	// enabled method call. See extensions/<name>/backend/bridge.go.
	// Route all WebDAV (CardDAV/CalDAV) traffic through one cert-aware transport
	// so it honors the same trust-on-first-use certificate store as IMAP/SMTP.
	// Host-agnostic (verifies per-connection server name) since the transport is
	// shared across every DAV source host. Installed once, before any sync runs.
	davTransport := http.DefaultTransport.(*http.Transport).Clone()
	davTransport.TLSClientConfig = certificate.BuildTLSConfigDynamic(a.certStore)
	davutil.SetDefaultBaseTransport(davTransport)

	a.authBroker = extauth.NewBroker(a.credStore, a.oauth2Manager, davTransport)
	a.mailAPI = extmail.NewAPI(a.messageStore, a.folderStore)
	a.composerAPI = extcompose.NewAPI(a)
	a.uiRegistry = extui.NewRegistry()

	// Construct first-party extensions and call their lifecycle Register().
	// Register is descriptive — it wires UI surfaces (rail tabs, hooks) that
	// persist across enable/disable cycles regardless of enabled state. The
	// frontend filters by enabled state at render time. Extension structs
	// are intentionally tiny (manifest + Register only); the Wails-bound
	// surface lives on each extension's Bridge struct, embedded into App.
	a.contactsExt = extcontactsbe.NewExtension()
	a.calendarExt = extcalendarbe.NewExtension()
	a.aiWritingExt = extaiwritingbe.NewExtension()
	a.mailMergeExt = extmailmergebe.NewExtension()
	a.knownExtensions = []coreapi.Extension{a.contactsExt, a.calendarExt, a.aiWritingExt, a.mailMergeExt}

	// Wire the Contacts extension's Bridge into App (embedded). Bridge
	// methods become Wails-bindable via Go's method-promotion on the
	// embedded field, so the frontend can call `Contacts_*` methods
	// directly. Bridge state is lazy — no stores open here.
	a.initContactsExtension()
	a.initCalendarExtension()
	a.initAIWritingExtension()
	a.initMailMergeExtension()

	// Construct one Core per known extension. The Core's Auth surface is
	// scoped to that extension's identity, so HTTPClient calls route via
	// the extension's manifest-declared client config (or via mail OAuth
	// for scopes listed in first_party_uses_core_for_scopes).
	for _, ext := range a.knownExtensions {
		extCore := newCoreForExtension(a, ext)
		unreg, err := ext.Register(extCore)
		if err != nil {
			log.Warn().Err(err).Str("extension", ext.Manifest().ID).Msg("Failed to register extension")
			continue
		}
		a.extensionUnregs = append(a.extensionUnregs, unreg)
	}

	// Start the Mail Merge outbox queue runner. MAIN PROCESS ONLY:
	// the detached composer hosts the same extension (shared SQLite)
	// and must never start a second runner — see
	// extensions/mail-merge/backend/queue.go. StartQueueRunner is
	// idempotent and gates on the enabled flag each tick, so starting
	// it here while the extension is disabled is harmless until the
	// user enables it.
	a.MailMergeBridge.StartQueueRunner()

	// Wire up network connectivity check so CardDAV scheduler skips ticks when offline
	if a.networkMonitor != nil {
		a.carddavScheduler.SetConnectivityCheck(a.networkMonitor.IsConnected)
	}

	// Set up access token getters for OAuth contact sources
	a.carddavSyncer.SetAccessTokenGetters(
		// Account token getter - for sources linked to email accounts
		func(accountID string) (string, error) {
			tokens, err := a.getValidOAuthToken(accountID)
			if err != nil {
				return "", err
			}
			return tokens.AccessToken, nil
		},
		// Source token getter - for standalone contact sources
		func(sourceID string) (string, error) {
			return a.getValidContactSourceOAuthToken(sourceID)
		},
	)

	// Removed in 2b.2.a: contactStore.Search now natively walks both local and
	// carddav contacts via the unified contact_records schema. The bridge
	// function is no longer needed.

	// Start CardDAV background sync scheduler
	a.carddavScheduler.Start(ctx)

	// Initialize undo stack (max 50 commands, 30 second timeout)
	a.undoStack = undo.NewStack(50, 30*time.Second)

	// OAuth2 manager was constructed earlier (before the Auth Broker, which
	// captures it). See the earlier guarded init above for the rationale.

	// Initialize shared compose operations (used by both App and ComposerApp)
	a.composeOps = composeOps{
		accountStore:   a.accountStore,
		folderStore:    a.folderStore,
		credStore:      a.credStore,
		certStore:      a.certStore,
		contactStore:   a.contactStore,
		oauth2Manager:  a.oauth2Manager,
		smimeStore:     a.smimeStore,
		smimeSigner:    a.smimeSigner,
		smimeEncryptor: a.smimeEncryptor,
		pgpStore:       a.pgpStore,
		pgpSigner:      a.pgpSigner,
		pgpEncryptor:   a.pgpEncryptor,
		draftOps:       &a.draftOps,
		// Save-new-recipients support (see app/account_contacts.go). The
		// Google client is constructed fresh here rather than reusing
		// a.googleContactsClient — that field is assigned after this
		// literal (line ~877), and Exists/Create do not use its cache.
		settingsStore:           a.settingsStore,
		carddavStore:            a.carddavStore,
		carddavSyncer:           a.carddavSyncer,
		googleContactsClient:    contact.NewGoogleContactsClient(),
		microsoftContactsSyncer: contact.NewMicrosoftContactsSyncer(),
		getContactSourceToken:   a.getValidContactSourceOAuthToken,
		msContactsSourceCache:   make(map[string]string),
	}

	// Undo Send: build the queue service now that composeOps exists,
	// and start its worker. MAIN PROCESS ONLY — the detached composer
	// shares this queue and must never start a second worker (see
	// app/sendqueue.go). The worker's first tick is immediate, so
	// messages queued while the app was closed (quit mid-countdown)
	// start sending on launch.
	a.sendQueue = a.newSendQueueService()
	a.sendQueue.StartWorker()

	// Initialize Google Contacts client for OAuth account contact search
	a.googleContactsClient = contact.NewGoogleContactsClient()

	// Initialize IPC for multi-window support
	a.initIPC(ctx)

	// Initialize network connectivity monitor (event-driven, zero polling).
	// Must be initialized before background sync so scheduler and IDLE
	// can use it to skip operations when offline.
	a.initNetworkMonitor(ctx)

	// Initialize and start background email sync (polling + IDLE)
	a.initBackgroundSync(ctx)

	// Sync any pending drafts from previous sessions
	go a.syncAllPendingDrafts()

	// Initialize FTS indexer for full-text search
	a.ftsIndexer = message.NewFTSIndexer(db.DB)

	// Initialize sync context tracking for cancel-and-restart
	a.syncContexts = make(map[string]context.CancelFunc)
	a.syncLastRequest = make(map[string]time.Time)
	a.ownFlagChangeAt = make(map[string]time.Time)
	a.ownExpungeAt = make(map[string]time.Time)
	a.draftSyncContexts = make(map[string]context.CancelFunc)
	a.draftSyncDone = make(map[string]chan struct{})

	// IMPORTANT: backend-ready signal. The frontend's main.ts waits for the
	// "app:ready" event (with IsReady() as a one-shot fallback) and will NOT
	// mount the main app until that event fires. If you remove, reorder, or
	// skip these two lines, the UI will never load — the boot splash will
	// stay visible forever.
	//
	// Placement: BEFORE the D-Bus desktop-integration inits below
	// (initNotifications, initSleepWakeMonitor, initThemeMonitor). Those
	// calls can block for many seconds on systems where xdg-desktop-portal
	// isn't running — they're best-effort system integration, NOT prerequisites
	// for the frontend. At this point the frontend has everything it needs:
	// stores constructed, migrations applied, extensions registered, network
	// monitor up, IPC server running, background sync started.
	//
	// We do NOT poll IsReady from the frontend — Wails' IPC bridge saturates
	// under high call rates on Linux/webkit2gtk and Flatpak. So: event for
	// the normal case, IsReady for the "event fired before listener
	// registered" race.
	a.ready = true
	wailsRuntime.EventsEmit(a.ctx, "app:ready")

	// Initialize desktop notifications with click handling
	a.initNotifications(ctx)

	// Initialize sleep/wake monitor for auto-sync on wake
	a.initSleepWakeMonitor(ctx)

	// Initialize system theme monitor (XDG Settings Portal on Linux)
	a.initThemeMonitor(ctx)

	// Set up FTS progress callback to emit events to frontend
	a.ftsIndexer.SetProgressCallback(func(folderID string, indexed, total int) {
		percentage := 0
		if total > 0 {
			percentage = (indexed * 100) / total
		}
		wailsRuntime.EventsEmit(ctx, "fts:progress", map[string]interface{}{
			"folderId":   folderID,
			"indexed":    indexed,
			"total":      total,
			"percentage": percentage,
		})
	})

	a.ftsIndexer.SetCompleteCallback(func(folderID string) {
		wailsRuntime.EventsEmit(ctx, "fts:complete", map[string]interface{}{
			"folderId": folderID,
		})
	})

	// Start background FTS indexing after a short delay to let initial sync complete
	go func() {
		defer recoverPanic("app", "FTS indexing")
		time.Sleep(5 * time.Second)
		log.Info().Msg("Starting background FTS indexing")
		wailsRuntime.EventsEmit(ctx, "fts:indexing", map[string]interface{}{
			"status": "started",
		})
		if err := a.ftsIndexer.IndexAllFolders(ctx); err != nil {
			log.Error().Err(err).Msg("Background FTS indexing failed")
		} else {
			log.Info().Msg("Background FTS indexing completed")
			wailsRuntime.EventsEmit(ctx, "fts:indexing", map[string]interface{}{
				"status": "completed",
			})
		}
	}()

	// Initialize autostart manager
	a.autostartMgr = platform.NewAutostartManager()

	log.Info().Msg(platform.AppName + " started successfully")
}

// IsReady reports whether Startup has fully completed. The frontend calls
// this ONCE at boot as a safety net for the "Go emitted app:ready before
// the frontend listener registered" race. Always safe to call: reads a
// bool field, no nil dereference possible, fires regardless of which
// stores are or aren't initialized.
//
// IMPORTANT: do NOT call this in a polling loop — it saturates the Wails
// IPC bridge. The frontend should use EventsOn('app:ready', ...) for the
// normal path; IsReady() is a one-shot check only.
func (a *App) IsReady() bool {
	return a.ready
}

// BeforeClose is called when the window is about to close (e.g., OS close signal)
func (a *App) BeforeClose(ctx context.Context) bool {
	if shuttingDown {
		return false
	}

	// Capture geometry while the window is still live so the next launch
	// restores the last size/position
	a.saveWindowGeometry()

	// Background mode: hide window instead of quitting — but only
	// when the tray icon is actually up. Hiding with no tray leaves
	// no visible window and no way back, so fall through to the real
	// quit below instead.
	if a.canHideOnClose() {
		log := logging.WithComponent("app")
		log.Info().Msg("Window close requested, hiding to background")
		wailsRuntime.WindowHide(a.ctx)
		a.windowHidden = true
		return true
	}

	// Normal shutdown flow
	log := logging.WithComponent("app")
	log.Info().Msg("Window close requested, showing shutdown overlay")

	shuttingDown = true

	// Emit event to show shutdown overlay
	wailsRuntime.EventsEmit(a.ctx, "app:shutting-down")

	// Schedule actual quit after UI has time to render
	go func() {
		defer recoverPanic("app", "shutdown")
		time.Sleep(150 * time.Millisecond)
		wailsRuntime.Quit(a.ctx)
	}()

	// Prevent immediate close
	return true
}

// NotifyStartupComplete signals the desktop environment that startup is done.
// Called from the frontend after WindowShow() so KDE/Plasma sees the placeholder
// → real window handoff cleanly (avoiding the taskbar-icon flash from #154).
// It also re-applies the saved maximized state after the window has been
// shown: main.go requests Maximised at creation, but the startup show path
// (StartHidden + the frontend's WindowShow calls) can lose it, so the flag
// is enforced once here, after the window is visible. Skipped when starting
// hidden — the window must stay hidden, and the creation-time state already
// applies when it is first shown from the tray or single-instance activation.
func (a *App) NotifyStartupComplete() {
	platform.NotifyStartupComplete()
	// Restore Undo countdowns for sends queued before a restart.
	// The frontend is mounted and listening by now (it calls this),
	// so re-emitted sendqueue:queued events reach the toast store.
	// Fires even when starting hidden: the window's frontend mounts
	// either way, and the toast is waiting when the window is first
	// shown from the tray.
	if a.sendQueue != nil {
		a.sendQueue.ReemitPendingCountdowns()
	}
	if a.appStateStore == nil || a.GetStartHiddenActive() {
		return
	}
	// The frontend has shown the window by now (App.svelte calls
	// WindowShow before this), so a later ShowWindow knows the window is
	// already visible. When start-hidden is active we return above and
	// the flag stays true.
	a.windowHidden = false
	if geo, ok := a.appStateStore.GetWindowGeometry(); ok {
		a.lastWindowMaximized = geo.Maximized
		a.lastWindowMaximizedKnown = true
		if geo.Maximized && !wailsRuntime.WindowIsMaximised(a.ctx) {
			wailsRuntime.WindowMaximise(a.ctx)
		}
	}
}

// canHideOnClose reports whether closing the window may hide it in
// the background: background mode is on AND the tray icon is
// actually up this session. A failed tray start is NOT retried here:
// on Windows the start waits up to 5s for the tray menu, which
// would stall the close. Callers fall back to a real quit — never a
// hidden window with no tray icon to bring it back.
//
// trayStarted alone is not trusted: it is set once at startup and
// never cleared, so a tray that dies mid-session would still claim
// to be up. Liveness is re-checked here through the tray itself
// (Alive reports the run loop's exit), which is a single
// non-blocking channel read — no polling, no restart attempt. A dead
// tray is not restarted: the library's Quit is once-only and its
// Windows window class cannot be re-registered, so the safe fallback
// is a real quit, and the user restarts the app to get the tray back.
func (a *App) canHideOnClose() bool {
	if a.settingsStore == nil {
		return false
	}
	runBg, _ := a.settingsStore.GetRunBackground()
	if !runBg || !a.trayStarted {
		return false
	}
	// A nil tray only exists in tests (NewApp always sets one); there
	// is no liveness to check there, so the started flag stands.
	return a.tray == nil || a.tray.Alive()
}

// startTrayIfBackground shows the system tray icon when Run in
// Background is on. Idempotent: starting twice is a no-op. It returns
// false when the tray is not up — the caller (GetStartHiddenActive)
// must then not start hidden, or the user is left with a hidden window
// and no way back.
func (a *App) startTrayIfBackground() bool {
	if a.settingsStore == nil {
		return false
	}
	runBg, _ := a.settingsStore.GetRunBackground()
	if !runBg {
		return false
	}
	return a.startTray()
}

// startTray shows the system tray icon (once per session).
func (a *App) startTray() bool {
	if a.tray == nil {
		return false
	}
	ok := a.tray.Start(a.TrayIconICO, a.TrayIconPNG, a.ShowWindow, a.QuitApp, a.trayCompose, a.trayCheckMail, a.trayOpenSettings)
	a.trayStarted = ok
	if !ok {
		log := logging.WithComponent("app")
		log.Warn().Msg("System tray did not start; the window will stay visible so the app can always be reopened")
		return ok
	}
	// The tray just started with the plain icon; push the badged one
	// (and the Dock/taskbar badge) now that the tray is up. Covers the
	// mid-session enable via SetRunBackground as well as startup.
	a.refreshBadge()
	return ok
}

// trayCompose shows the window, then asks the frontend to open the
// composer. ShowWindow runs first so the composer never opens into a
// hidden window; the event itself is delivered by the same runtime,
// so it lands after the show.
func (a *App) trayCompose() {
	a.ShowWindow()
	wailsRuntime.EventsEmit(a.ctx, "tray:compose")
}

// trayCheckMail syncs all accounts in the background (the same complete
// sync the refresh control runs). It runs on its own goroutine so a long
// sync never blocks the tray menu, and it does not open the window —
// new mail is visible when the user opens Daak.
func (a *App) trayCheckMail() {
	go func() {
		defer recoverPanic("app", "tray-check-mail")
		if err := a.SyncAllComplete(); err != nil {
			log := logging.WithComponent("app")
			log.Error().Err(err).Msg("Tray check mail failed")
		}
	}()
}

// trayOpenSettings shows the window, then asks the frontend to open the
// Settings dialog. Same show-first ordering as trayCompose.
func (a *App) trayOpenSettings() {
	a.ShowWindow()
	wailsRuntime.EventsEmit(a.ctx, "tray:settings")
}

// stopTray ends the tray loop. App shutdown only — turning background
// mode off does not hide the tray (see package tray): the library cannot
// show it again afterwards, and a hidden window with no tray icon is the
// exact failure this tray exists to fix.
func (a *App) stopTray() {
	if a.tray == nil {
		return
	}
	a.tray.Stop()
}

// HandleSingleInstanceCommand is the single-instance activation callback
// (darwin Unix socket, Windows named pipe, Linux socket — the locks share
// the platform.SingleInstanceLock interface). "show" brings the window
// forward; a "mailto:..." URL opens the composer.
//
// It is registered in runMainMode, before wails.Run — the lock starts
// accepting the moment TryLock succeeds, and a second launch in the first
// moments of startup used to have its command silently dropped because
// SetOnShow was only registered in Startup (the lock drops the command
// when no callback is set). That is what happened in the Nov 20 Terminal
// log: a launch logged "Activated existing instance command=show" yet no
// window appeared — the first instance was still starting up, and by the
// time its Startup ran it was already aborting in tray startup.
//
// Commands that arrive before Startup has a runtime context are
// remembered instead of dropped: a "show" is honoured once Startup sets
// the context, and a mailto: URL is stashed in PendingMailto for the
// frontend to pick up (the same field the command-line mailto path uses).
func (a *App) HandleSingleInstanceCommand(data string) {
	if strings.HasPrefix(data, "mailto:") {
		a.showMu.Lock()
		ready := a.ctx != nil && a.settingsStore != nil
		a.showMu.Unlock()
		if !ready {
			if mailtoData := ParseMailtoURL(data); mailtoData != nil {
				a.PendingMailto = mailtoData
			}
			return
		}
		a.handleExternalMailto(data)
		return
	}

	a.showMu.Lock()
	if a.ctx == nil {
		a.pendingShow = true
		a.showMu.Unlock()
		return
	}
	a.showMu.Unlock()
	a.ShowWindow()
}

// ShowWindow brings the window to the foreground from hidden/minimized state.
// Used by single-instance activation, notification clicks, tray menu items, etc.
//
// Safe to call from any goroutine on every platform: Wails dispatches the
// native calls itself (darwin: ON_MAIN_THREAD dispatch_async; windows:
// Invoke to the UI thread; linux: ExecuteOnMainThread to the GTK main
// thread), so this must NOT add its own main-thread dispatch.
//
// v0.7.25 got this wrong and it crashed: it dispatched the whole show to
// the macOS main thread and skipped the native show when the window was
// already visible — yet "Open Daak" still segfaulted at the same libobjc
// PC. The native show was never the crasher. The crasher is the
// "window:show" event: EventsEmit -> Wails ExecJS, whose C wrapper
// (wails v2.16.0, Application.m) builds the script string with safeInit —
// [NSString stringWithUTF8String:], autoreleased — and then releases it
// explicitly inside the ON_MAIN_THREAD block. Called from the main thread
// that is a double release: the run loop's autorelease pool drains and
// messages the freed string, segfaulting in objc_msgSend (same PC in
// both crashes, wild fault addresses). Called from a Go worker goroutine
// there is no autorelease pool, so the explicit release simply balances
// the creation and nothing ever messages the freed string — which is why
// thousands of worker-thread emits in normal use never crash. So on
// darwin the emit goes out on a fresh worker goroutine, never on the
// main thread. The native show is issued unconditionally: a redundant
// show of a visible window is a routine AppKit call, and the earlier
// "already visible" guard only weakened bring-to-front (and left a
// Cmd+H edge). windowHidden is maintained by Startup (the window is
// created hidden), NotifyStartupComplete (cleared once the frontend
// shows it), CloseWindow and ShowWindow itself; it is also read by
// saveWindowGeometry and watchTerminationSignals.
func (a *App) ShowWindow() {
	log := logging.WithComponent("app")
	log.Info().Msg("Showing window")

	wailsRuntime.WindowUnminimise(a.ctx)
	wailsRuntime.WindowShow(a.ctx)
	a.windowHidden = false

	// Emit event so the frontend can also attempt to focus. On macOS
	// this must run on a worker goroutine, never the main thread (see
	// above); on other platforms the emit is direct.
	if runtime.GOOS == "darwin" {
		go wailsRuntime.EventsEmit(a.ctx, "window:show")
	} else {
		wailsRuntime.EventsEmit(a.ctx, "window:show")
	}
}

// CloseWindow handles the window close button click.
// If background mode is enabled and the tray icon is actually up,
// hides the window instead of quitting. Otherwise quits for real:
// a hidden window with no tray icon has no way back.
// Called by the frontend title bar close button.
func (a *App) CloseWindow() {
	a.saveWindowGeometry()
	if a.canHideOnClose() {
		log := logging.WithComponent("app")
		log.Info().Msg("Window close requested, hiding to background")
		wailsRuntime.WindowHide(a.ctx)
		a.windowHidden = true
		return
	}

	// Normal shutdown flow
	if shuttingDown {
		return
	}
	shuttingDown = true

	log := logging.WithComponent("app")
	log.Info().Msg("Window close requested, shutting down")
	wailsRuntime.EventsEmit(a.ctx, "app:shutting-down")
	go func() {
		defer recoverPanic("app", "shutdown")
		time.Sleep(150 * time.Millisecond)
		wailsRuntime.Quit(a.ctx)
	}()
}

// QuitApp forces a real quit, bypassing background mode.
// Used by frontend or future tray menu to actually exit.
func (a *App) QuitApp() {
	if shuttingDown {
		return
	}
	shuttingDown = true
	a.saveWindowGeometry()

	log := logging.WithComponent("app")
	log.Info().Msg("Quit requested")
	wailsRuntime.EventsEmit(a.ctx, "app:shutting-down")
	go func() {
		defer recoverPanic("app", "shutdown")
		time.Sleep(150 * time.Millisecond)
		wailsRuntime.Quit(a.ctx)
	}()
}

// GetStartHiddenActive returns true if the window should remain hidden on startup.
// True when both start_hidden and run_background settings are enabled —
// and the tray icon is actually up. If the tray could not be started,
// starting hidden would leave a hidden window with no way back, so the
// window is shown instead. (If Startup has not run yet, trayStarted is
// false and this fails open the same way.) Liveness is re-checked as
// well as the started flag, so a tray that died between Startup and
// this check does not strand a hidden window either.
func (a *App) GetStartHiddenActive() bool {
	startHidden, _ := a.settingsStore.GetStartHidden()
	if !startHidden {
		return false
	}
	runBg, _ := a.settingsStore.GetRunBackground()
	if !runBg {
		return false
	}
	if !a.trayStarted {
		return false
	}
	// A nil tray only exists in tests (NewApp always sets one).
	return a.tray == nil || a.tray.Alive()
}

// InitiateShutdown triggers the application quit (called from frontend)
func (a *App) InitiateShutdown() {
	if shuttingDown {
		return
	}
	shuttingDown = true
	a.saveWindowGeometry()

	log := logging.WithComponent("app")
	log.Info().Msg("Initiating shutdown")
	wailsRuntime.Quit(a.ctx)
}

// Shutdown is called when the app is closing
func (a *App) Shutdown(ctx context.Context) {
	log := logging.WithComponent("app")

	// Remove the tray icon first, so it never outlives the app.
	a.stopTray()

	// Broadcast shutdown to all composer windows
	if a.ipcServer != nil {
		clients := a.ipcServer.Clients()
		if len(clients) > 0 {
			log.Info().Int("count", len(clients)).Msg("Notifying composer windows of shutdown")
			msg, _ := ipc.NewMessage(ipc.TypeShutdown, ipc.ShutdownPayload{
				Reason: "main window closing",
			})
			if err := a.ipcServer.Broadcast(msg); err != nil {
				log.Debug().Err(err).Msg("Failed to broadcast shutdown to composer windows")
			}
			// Give composers a moment to save drafts
			time.Sleep(500 * time.Millisecond)
		}
		_ = a.ipcServer.Stop()
		log.Info().Msg("IPC server stopped")
	}

	// Stop email sync scheduler
	if a.syncScheduler != nil {
		a.syncScheduler.Stop()
		log.Info().Msg("Email sync scheduler stopped")
	}

	// Stop IDLE manager
	if a.idleManager != nil {
		a.idleManager.Stop()
		log.Info().Msg("IDLE manager stopped")
	}

	// Stop sleep/wake monitor
	if a.sleepWakeMonitor != nil {
		_ = a.sleepWakeMonitor.Stop()
		log.Info().Msg("Sleep/wake monitor stopped")
	}

	// Stop network monitor
	if a.networkMonitor != nil {
		_ = a.networkMonitor.Stop()
		log.Info().Msg("Network monitor stopped")
	}

	// Stop theme monitor
	if a.themeMonitor != nil {
		_ = a.themeMonitor.Stop()
		log.Info().Msg("Theme monitor stopped")
	}

	// Stop notification listener
	if a.notifier != nil {
		a.notifier.Stop()
		log.Info().Msg("Notification listener stopped")
	}

	// Stop CardDAV scheduler
	if a.carddavScheduler != nil {
		a.carddavScheduler.Stop()
		log.Info().Msg("CardDAV scheduler stopped")
	}

	// Close all IMAP connections
	if a.imapPool != nil {
		a.imapPool.CloseAll()
		log.Info().Msg("IMAP connections closed")
	}

	if a.db != nil {
		a.db.Close()
		log.Info().Msg("Database closed")
	}

	log.Info().Msg(platform.AppName + " shutdown complete")
}

// updateDBConnectionPool scales the database connection pool based on account count.
// This should be called at startup and whenever accounts are added or removed.
func (a *App) updateDBConnectionPool() {
	accounts, err := a.accountStore.List()
	if err != nil {
		// On error, use a reasonable default
		a.db.UpdateIdleConns(0)
		return
	}
	a.db.UpdateIdleConns(len(accounts))
}

// getIMAPCredentials returns IMAP credentials for an account.
// Handles both password and OAuth2 authentication.
func (a *App) getIMAPCredentials(accountID string) (*imap.ClientConfig, error) {
	return a.composeOps.getIMAPCredentials(a.ctx, accountID)
}

// getValidOAuthToken returns a valid OAuth token, refreshing if needed.
// If refresh fails, emits an event for the frontend to prompt re-authorization.
func (a *App) getValidOAuthToken(accountID string) (*credentials.OAuthTokens, error) {
	return a.composeOps.getValidOAuthToken(a.ctx, accountID)
}

// GetContext returns the app context
func (a *App) GetContext() context.Context {
	return a.ctx
}

// getValidContactSourceOAuthToken returns a valid OAuth token for a standalone contact source
func (a *App) getValidContactSourceOAuthToken(sourceID string) (string, error) {
	log := logging.WithComponent("app")

	tokens, err := a.credStore.GetContactSourceOAuthTokens(sourceID)
	if err != nil {
		return "", fmt.Errorf("failed to get contact source OAuth tokens: %w", err)
	}

	// Check if token expires within 5 minutes
	if tokens.IsExpiringSoon(5 * time.Minute) {
		log.Info().
			Str("source_id", sourceID).
			Time("expires_at", tokens.ExpiresAt).
			Msg("Contact source OAuth token expiring soon, refreshing")

		// Refresh the token
		newTokenResp, err := a.oauth2Manager.RefreshToken(tokens.Provider, tokens.RefreshToken)
		if err != nil {
			log.Error().Err(err).
				Str("source_id", sourceID).
				Msg("Contact source OAuth token refresh failed")

			// Error is persisted to the contact_sources table by the sync caller;
			// sidebar red dot picks it up on next contactSourcesStore.load() (app start
			// or Contacts settings open). Real-time notification deferred — not worth
			// the listener wiring for a non-time-sensitive failure mode.
			return "", fmt.Errorf("contact source OAuth token refresh failed: %w", err)
		}

		// Calculate new expiry time
		expiresAt := time.Now().Add(time.Duration(newTokenResp.ExpiresIn) * time.Second)

		// Update tokens in store
		tokens.AccessToken = newTokenResp.AccessToken
		tokens.ExpiresAt = expiresAt
		if newTokenResp.RefreshToken != "" {
			tokens.RefreshToken = newTokenResp.RefreshToken
		}

		if err := a.credStore.SetContactSourceOAuthTokens(sourceID, tokens); err != nil {
			log.Warn().Err(err).Msg("Failed to save refreshed contact source OAuth tokens")
		}

		log.Info().
			Str("source_id", sourceID).
			Time("new_expires_at", expiresAt).
			Msg("Contact source OAuth token refreshed successfully")
	}

	return tokens.AccessToken, nil
}

// OpenURL opens a URL in the system browser with proper shell escaping
// This bypasses Wails' BrowserOpenURL which has strict validation against shell metacharacters
func (a *App) OpenURL(url string) error {
	log := logging.WithComponent("app")
	log.Debug().Str("url", url).Msg("Opening URL in system browser")

	// Validate URL and check protocol for security
	// This prevents file:// URLs and other potentially dangerous schemes
	if url == "" {
		return fmt.Errorf("empty URL")
	}

	// Allow common safe protocols
	// Note: We're being permissive here to allow legitimate email links
	// The main security comes from using exec.Command properly
	if !isAllowedProtocol(url) {
		log.Warn().Str("url", url).Msg("Rejecting URL with disallowed protocol")
		return fmt.Errorf("URL protocol not allowed for security reasons")
	}

	// On Linux, try the OpenURI portal first — works in Flatpak (where xdg-open
	// can't reach host browsers) and triggers the host's URL-handler
	// notification on Wayland DEs. Fall through to xdg-open on portal error.
	if runtime.GOOS == "linux" {
		perr := platform.PortalOpenURI(url)
		if perr == nil {
			return nil
		}
		log.Debug().Err(perr).Msg("Portal OpenURI failed, falling back to xdg-open")
	}

	var cmd *exec.Cmd

	// Determine the command based on the operating system
	switch runtime.GOOS {
	case "linux":
		// Use xdg-open on Linux
		// exec.Command properly escapes the URL argument, preventing shell injection
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		// Use open on macOS
		cmd = exec.Command("open", url)
	case "windows":
		// Use ShellExecute, not `cmd /c start`: cmd treats `&` as a command
		// separator and truncates URLs with multiple query params (issue #261).
		return platform.OpenURLWindows(url)
	default:
		return fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}

	// Start the command without waiting for it to complete
	// Browser opening should be async - we don't need to wait
	if err := cmd.Start(); err != nil {
		log.Error().Err(err).Str("url", url).Msg("Failed to open URL in browser")
		return fmt.Errorf("failed to open URL: %w", err)
	}

	log.Debug().Str("url", url).Msg("Successfully started browser process")
	return nil
}

// isAllowedProtocol checks if a URL uses an allowed protocol
// Prevents file:// URLs and other potentially dangerous schemes
func isAllowedProtocol(url string) bool {
	// Common safe protocols for an email client
	allowedPrefixes := []string{
		"http://",
		"https://",
		"mailto:",
		// Note: We could add more if needed, but being conservative
	}

	for _, prefix := range allowedPrefixes {
		if len(url) >= len(prefix) && url[:len(prefix)] == prefix {
			return true
		}
	}

	return false
}
