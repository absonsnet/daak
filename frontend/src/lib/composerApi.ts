/**
 * Composer API Abstraction Layer
 *
 * Provides a unified interface for composer operations that works in both:
 * - Main window (modal/inline composer) - uses App bindings
 * - Detached composer window - uses ComposerApp bindings
 *
 * The API is injected via Svelte context to allow different implementations
 * depending on the window type.
 */

// @ts-ignore - Wails generated imports
import { smtp, account, contact, app, smime, pgp, backend } from '../../wailsjs/go/models'
import type {
  MailMergeContactsResult,
  MailMergeParsedFile,
  MailMergeParsedFileFull,
  MailMergeRenderedMessage,
  MailMergeRun,
  MailMergeRunRequest,
  MailMergeRunSummary,
  MailMergeSettings,
  MailMergeSigningInfo,
  MailMergeTemplate,
  MailMergeValidationResult,
  Row,
} from '$extensions/mail-merge/frontend/lib/mailmerge'

/**
 * A composer-toolbar registration (extensionId + label/icon/component).
 * Mirrors coreapi.ComposerToolbarRequest and the structural
 * ComposerToolbarItem in Composer.svelte.
 */
export interface ComposerToolbarItem {
  extensionId: string
  label: string
  icon: string
  component: string
  order?: number
}

/**
 * Interface for composer API operations.
 * Both App and ComposerApp implement these methods with the same signatures.
 */
export interface ComposerApi {
  /**
   * Send a composed email directly: transmits immediately, with NO Undo
   * window and NO Outbox row (fourth-audit finding 39). The composer UI
   * uses queueSend; this stays for callers that need a synchronous send.
   * Direct sends still count toward the Mail Merge hourly budget when
   * the count-normal-sends opt-in is on.
   */
  sendMessage: (accountId: string, message: smtp.ComposeMessage) => Promise<void>

  /** Queue a composed email for background send after the undo
   * window. Returns the queue id, the draft to reopen on Undo, and
   * the countdown in seconds. The composer closes right after this
   * resolves; the worker dispatches the message. */
  queueSend: (accountId: string, message: smtp.ComposeMessage, draftId: string) => Promise<app.QueueSendResult>

  /** Cancel a queued send that has not dispatched yet. Returns the
   * draft to reopen in the composer. Fails once the message is sent. */
  undoSend: (queueId: string) => Promise<app.UndoSendResult>

  /** Search contacts for autocomplete */
  searchContacts: (query: string, limit: number) => Promise<contact.Contact[]>

  /** Get identities for an account */
  getIdentities: (accountId: string) => Promise<account.Identity[]>

  /** Get the account's default address list for a kind ('bcc' | 'replyto'); "" when disabled */
  getDefaultAddress: (kind: string, accountId: string) => Promise<string>

  /** Save a draft (creates new or updates existing if draftId provided) */
  saveDraft: (accountId: string, message: smtp.ComposeMessage, draftId: string) => Promise<{ id: string; syncStatus: string }>

  /** Delete a draft */
  deleteDraft: (draftId: string) => Promise<void>

  /** Pick attachment files via native file picker */
  pickAttachmentFiles: () => Promise<app.ComposerAttachment[]>

  /** Get account details */
  getAccount: (accountId: string) => Promise<account.Account>

  /** Check if account has a valid default S/MIME certificate */
  hasSMIMECertificate: (accountId: string) => Promise<boolean>

  /** Get the S/MIME certificate matching a specific email (for identity-aware bar visibility) */
  getSMIMECertificateForEmail: (accountId: string, email: string) => Promise<smime.Certificate | null>

  /** Get the S/MIME signing policy for an account */
  getSMIMESignPolicy: (accountId: string) => Promise<string>

  /** Get the S/MIME encryption policy for an account */
  getSMIMEEncryptPolicy: (accountId: string) => Promise<string>

  /** Check which recipients have S/MIME certificates available */
  checkRecipientCerts: (emails: string[]) => Promise<Record<string, boolean>>

  /** Open a file picker for recipient certificate files */
  pickRecipientCertFile: () => Promise<string>

  /** Import a recipient's public certificate from a file */
  importRecipientCert: (email: string, filePath: string) => Promise<void>

  /** Check if account has a valid default PGP key */
  hasPGPKey: (accountId: string) => Promise<boolean>

  /** Get the PGP key matching a specific email (for identity-aware bar visibility) */
  getPGPKeyForEmail: (accountId: string, email: string) => Promise<pgp.Key | null>

  /** Get the PGP signing policy for an account */
  getPGPSignPolicy: (accountId: string) => Promise<string>

  /** Get the PGP encryption policy for an account */
  getPGPEncryptPolicy: (accountId: string) => Promise<string>

  /** Check which recipients have PGP public keys available */
  checkRecipientPGPKeys: (emails: string[]) => Promise<Record<string, boolean>>

  /** Open a file picker for recipient PGP public key files */
  pickRecipientPGPKeyFile: () => Promise<string>

  /** Import a recipient's PGP public key from a file */
  importRecipientPGPKey: (email: string, filePath: string) => Promise<void>

  /** Perform a WKD lookup for a recipient's PGP key */
  lookupWKD: (email: string) => Promise<string>

  /** Perform an HKP key server lookup for a recipient's PGP key */
  lookupHKP: (email: string) => Promise<string>

  /** Perform a unified WKD+HKP lookup for a recipient's PGP key */
  lookupPGPKey: (email: string) => Promise<string>

  /** Read a file from a filesystem path as an attachment */
  readFileAsAttachment: (filePath: string) => Promise<app.ComposerAttachment | null>

  /** Check if running inside a Flatpak sandbox */
  isFlatpak: () => Promise<boolean>

  /**
   * Get all accounts with their identities (only available in main window).
   * Returns undefined in detached composer windows.
   */
  getAllAccountIdentities?: () => Promise<app.AccountIdentityGroup[]>

  /**
   * Open a detached composer window (only available in main window).
   * Returns undefined in detached composer windows.
   */
  openComposerWindow?: (accountId: string, mode: string, messageId: string, draftId: string, mailtoURL?: string) => Promise<void>

  /**
   * List composer-toolbar registrations for enabled extensions (e.g. ai-writing).
   * Main window reads the main App registry; detached windows read the
   * composer process's own registry (see app/detached_composer.go).
   */
  listComposerToolbarItems: () => Promise<ComposerToolbarItem[]>

  /** AI Writing: list the available rewrite actions. */
  aiWritingListActions: () => Promise<backend.ActionInfo[]>

  /** AI Writing: rewrite the draft with the chosen action. */
  aiWritingImprove: (req: backend.ImproveRequest) => Promise<backend.ImproveResult>

  /** AI Writing: per-action prompt overrides. */
  aiWritingGetActionPrompts: () => Promise<backend.ActionPromptInfo[]>

  /** AI Writing: models the user can pick from. */
  aiWritingListModels: () => Promise<backend.ModelInfo[]>

  /** AI Writing: current settings. */
  aiWritingGetSettings: () => Promise<backend.Settings>

  /** AI Writing: save settings. */
  aiWritingSaveSettings: (input: backend.SettingsInput) => Promise<void>

  /** AI Writing: enabled state + API-key presence. */
  aiWritingGetStatus: () => Promise<backend.Status>

  // --- Mail Merge (extensions/mail-merge/backend/bridge.go) ---
  // Mirrors the aiWriting* methods above: same signatures on App and
  // ComposerApp, so the merge dialog works in both composer windows.
  // NOTE: GetSettings/SaveSettings use the local MailMergeSettings type,
  // not the generated backend.Settings — the generator kept only the
  // ai-writing Settings shape for that name (see mailmerge.ts header).

  /** Mail Merge: parse a CSV/JSON/XLSX source file (columns + row count + first 3 rows). */
  mailMergeParseFile: (path: string, kind: string) => Promise<MailMergeParsedFile>

  /** Mail Merge: parse a source file with ALL rows (the XLSX path — the frontend has no XLSX parser). sheet: "" = first sheet. */
  mailMergeParseFileFull: (path: string, kind: string, sheet: string) => Promise<MailMergeParsedFileFull>

  /** Mail Merge: all unified contacts as merge rows + columns. */
  mailMergeGetContacts: () => Promise<MailMergeContactsResult>

  /** Mail Merge: render the template for the given row indices (empty = first 3). */
  mailMergePreview: (template: MailMergeTemplate, rows: Row[], indices: number[]) => Promise<MailMergeRenderedMessage[]>

  /** Mail Merge: validate template + rows (unresolved variables block the run). */
  mailMergeValidate: (template: MailMergeTemplate, rows: Row[]) => Promise<MailMergeValidationResult>

  /** Mail Merge: start a run asynchronously; returns the run ID. */
  mailMergeStartRun: (req: MailMergeRunRequest) => Promise<string>

  /** Mail Merge: one run with its per-row log. */
  mailMergeGetRun: (runId: string) => Promise<MailMergeRun>

  /** Mail Merge: run summaries, newest first. */
  mailMergeListRuns: () => Promise<MailMergeRunSummary[]>

  /** Mail Merge: cancel a running run (stops after the current message). */
  mailMergeCancelRun: (runId: string) => Promise<void>

  /** Mail Merge: re-run the failed rows only; returns the new run ID. */
  mailMergeResumeRun: (runId: string) => Promise<string>

  /** Mail Merge: send the next CapLimit held rows of a run. */
  mailMergeSendNextBatch: (runId: string) => Promise<void>

  /** Mail Merge: send all held rows of a run, ignoring the cap. */
  mailMergeSendAllRemaining: (runId: string) => Promise<void>

  /** Mail Merge: discard all held rows of a run. */
  mailMergeCancelRemaining: (runId: string) => Promise<void>

  /** Mail Merge: one account's settings over the defaults. */
  mailMergeGetSettings: (accountId: string) => Promise<MailMergeSettings>

  /** Mail Merge: save one account's settings. */
  mailMergeSaveSettings: (accountId: string, settings: MailMergeSettings) => Promise<void>

  /** Mail Merge: the account's signing setup (S/MIME vs PGP, default) for the dialog's signing control. */
  mailMergeGetSigningInfo: (accountId: string, fromEmail: string) => Promise<MailMergeSigningInfo>

  /** Mail Merge: pick a CSV/JSON/XLSX source file; "" when cancelled. */
  mailMergePickFile: (kind: string) => Promise<string>

  /** Mail Merge: pick the folder per-row attachment filenames resolve against. */
  mailMergePickAttachmentDir: () => Promise<string>
}

/**
 * Context key for accessing the composer API.
 * Use with getContext/setContext.
 */
export const COMPOSER_API_KEY = 'composer-api'

/**
 * Creates the composer API implementation for the main window.
 * Uses App bindings.
 */
export function createMainWindowApi(): ComposerApi {
  // Dynamic import to avoid bundling issues
  // These will be resolved at runtime based on which entry point is used
  return {
    sendMessage: async (accountId: string, message: smtp.ComposeMessage) => {
      const { SendMessage } = await import('../../wailsjs/go/app/App.js')
      return SendMessage(accountId, message)
    },

    queueSend: async (accountId: string, message: smtp.ComposeMessage, draftId: string) => {
      const { QueueSend } = await import('../../wailsjs/go/app/App.js')
      return QueueSend(accountId, message, draftId)
    },

    undoSend: async (queueId: string) => {
      const { UndoSend } = await import('../../wailsjs/go/app/App.js')
      return UndoSend(queueId)
    },

    searchContacts: async (query: string, limit: number) => {
      const { SearchContacts } = await import('../../wailsjs/go/app/App.js')
      return SearchContacts(query, limit) || []
    },

    getIdentities: async (accountId: string) => {
      const { GetIdentities } = await import('../../wailsjs/go/app/App.js')
      return GetIdentities(accountId)
    },

    saveDraft: async (accountId: string, message: smtp.ComposeMessage, draftId: string) => {
      const { SaveDraft } = await import('../../wailsjs/go/app/App.js')
      const result = await SaveDraft(accountId, message, draftId)
      return { id: result?.draft?.id || '', syncStatus: result?.draft?.syncStatus || 'pending' }
    },

    deleteDraft: async (draftId: string) => {
      const { DeleteDraft } = await import('../../wailsjs/go/app/App.js')
      return DeleteDraft(draftId)
    },

    pickAttachmentFiles: async () => {
      const { PickAttachmentFiles } = await import('../../wailsjs/go/app/App.js')
      return PickAttachmentFiles()
    },

    getAccount: async (accountId: string) => {
      const { GetAccount } = await import('../../wailsjs/go/app/App.js')
      return GetAccount(accountId)
    },

    getDefaultAddress: async (kind: string, accountId: string) => {
      const { GetDefaultAddress } = await import('../../wailsjs/go/app/App.js')
      return GetDefaultAddress(kind, accountId)
    },

    hasSMIMECertificate: async (accountId: string) => {
      const { HasSMIMECertificate } = await import('../../wailsjs/go/app/App.js')
      return HasSMIMECertificate(accountId)
    },

    getSMIMECertificateForEmail: async (accountId: string, email: string) => {
      const { GetSMIMECertificateForEmail } = await import('../../wailsjs/go/app/App.js')
      return GetSMIMECertificateForEmail(accountId, email)
    },

    getSMIMESignPolicy: async (accountId: string) => {
      const { GetSMIMESignPolicy } = await import('../../wailsjs/go/app/App.js')
      return GetSMIMESignPolicy(accountId)
    },

    getSMIMEEncryptPolicy: async (accountId: string) => {
      const { GetSMIMEEncryptPolicy } = await import('../../wailsjs/go/app/App.js')
      return GetSMIMEEncryptPolicy(accountId)
    },

    checkRecipientCerts: async (emails: string[]) => {
      const { CheckRecipientCerts } = await import('../../wailsjs/go/app/App.js')
      return CheckRecipientCerts(emails)
    },

    pickRecipientCertFile: async () => {
      const { PickRecipientCertFile } = await import('../../wailsjs/go/app/App.js')
      return PickRecipientCertFile()
    },

    importRecipientCert: async (email: string, filePath: string) => {
      const { ImportRecipientCert } = await import('../../wailsjs/go/app/App.js')
      return ImportRecipientCert(email, filePath)
    },

    hasPGPKey: async (accountId: string) => {
      const { HasPGPKey } = await import('../../wailsjs/go/app/App.js')
      return HasPGPKey(accountId)
    },

    getPGPKeyForEmail: async (accountId: string, email: string) => {
      const { GetPGPKeyForEmail } = await import('../../wailsjs/go/app/App.js')
      return GetPGPKeyForEmail(accountId, email)
    },

    getPGPSignPolicy: async (accountId: string) => {
      const { GetPGPSignPolicy } = await import('../../wailsjs/go/app/App.js')
      return GetPGPSignPolicy(accountId)
    },

    getPGPEncryptPolicy: async (accountId: string) => {
      const { GetPGPEncryptPolicy } = await import('../../wailsjs/go/app/App.js')
      return GetPGPEncryptPolicy(accountId)
    },

    checkRecipientPGPKeys: async (emails: string[]) => {
      const { CheckRecipientPGPKeys } = await import('../../wailsjs/go/app/App.js')
      return CheckRecipientPGPKeys(emails)
    },

    pickRecipientPGPKeyFile: async () => {
      const { PickRecipientPGPKeyFile } = await import('../../wailsjs/go/app/App.js')
      return PickRecipientPGPKeyFile()
    },

    importRecipientPGPKey: async (email: string, filePath: string) => {
      const { ImportRecipientPGPKey } = await import('../../wailsjs/go/app/App.js')
      return ImportRecipientPGPKey(email, filePath)
    },

    lookupWKD: async (email: string) => {
      const { LookupWKD } = await import('../../wailsjs/go/app/App.js')
      return LookupWKD(email)
    },

    lookupHKP: async (email: string) => {
      const { LookupHKP } = await import('../../wailsjs/go/app/App.js')
      return LookupHKP(email)
    },

    lookupPGPKey: async (email: string) => {
      const { LookupPGPKey } = await import('../../wailsjs/go/app/App.js')
      return LookupPGPKey(email)
    },

    readFileAsAttachment: async (filePath: string) => {
      const { ReadFileAsAttachment } = await import('../../wailsjs/go/app/App.js')
      return ReadFileAsAttachment(filePath)
    },

    isFlatpak: async () => {
      const { IsFlatpak } = await import('../../wailsjs/go/app/App.js')
      return IsFlatpak()
    },

    getAllAccountIdentities: async () => {
      const { GetAllAccountIdentities } = await import('../../wailsjs/go/app/App.js')
      return GetAllAccountIdentities()
    },

    openComposerWindow: async (accountId: string, mode: string, messageId: string, draftId: string, mailtoURL?: string) => {
      const { OpenComposerWindow } = await import('../../wailsjs/go/app/App.js')
      return OpenComposerWindow(accountId, mode, messageId, draftId, mailtoURL || '')
    },

    listComposerToolbarItems: async () => {
      const { ListComposerToolbarItems } = await import('../../wailsjs/go/app/App.js')
      return ListComposerToolbarItems() || []
    },

    aiWritingListActions: async () => {
      const { AIWriting_ListActions } = await import('../../wailsjs/go/app/App.js')
      return AIWriting_ListActions() || []
    },

    aiWritingImprove: async (req: backend.ImproveRequest) => {
      const { AIWriting_Improve } = await import('../../wailsjs/go/app/App.js')
      return AIWriting_Improve(req)
    },

    aiWritingGetActionPrompts: async () => {
      const { AIWriting_GetActionPrompts } = await import('../../wailsjs/go/app/App.js')
      return AIWriting_GetActionPrompts() || []
    },

    aiWritingListModels: async () => {
      const { AIWriting_ListModels } = await import('../../wailsjs/go/app/App.js')
      return AIWriting_ListModels() || []
    },

    aiWritingGetSettings: async () => {
      const { AIWriting_GetSettings } = await import('../../wailsjs/go/app/App.js')
      return AIWriting_GetSettings()
    },

    aiWritingSaveSettings: async (input: backend.SettingsInput) => {
      const { AIWriting_SaveSettings } = await import('../../wailsjs/go/app/App.js')
      return AIWriting_SaveSettings(input)
    },

    aiWritingGetStatus: async () => {
      const { AIWriting_GetStatus } = await import('../../wailsjs/go/app/App.js')
      return AIWriting_GetStatus()
    },

    mailMergeParseFile: async (path: string, kind: string) => {
      const { MailMerge_ParseFile } = await import('../../wailsjs/go/app/App.js')
      return MailMerge_ParseFile(path, kind)
    },

    mailMergeParseFileFull: async (path: string, kind: string, sheet: string) => {
      const { MailMerge_ParseFileFull } = await import('../../wailsjs/go/app/App.js')
      return MailMerge_ParseFileFull(path, kind, sheet)
    },

    mailMergeGetContacts: async () => {
      const { MailMerge_GetContacts } = await import('../../wailsjs/go/app/App.js')
      return MailMerge_GetContacts()
    },

    mailMergePreview: async (template: MailMergeTemplate, rows: Row[], indices: number[]) => {
      const { MailMerge_Preview } = await import('../../wailsjs/go/app/App.js')
      return MailMerge_Preview(template as never, rows, indices) || []
    },

    mailMergeValidate: async (template: MailMergeTemplate, rows: Row[]) => {
      const { MailMerge_Validate } = await import('../../wailsjs/go/app/App.js')
      return MailMerge_Validate(template as never, rows)
    },

    mailMergeStartRun: async (req: MailMergeRunRequest) => {
      const { MailMerge_StartRun } = await import('../../wailsjs/go/app/App.js')
      return MailMerge_StartRun(req as never)
    },

    mailMergeGetRun: async (runId: string) => {
      const { MailMerge_GetRun } = await import('../../wailsjs/go/app/App.js')
      return MailMerge_GetRun(runId)
    },

    mailMergeListRuns: async () => {
      const { MailMerge_ListRuns } = await import('../../wailsjs/go/app/App.js')
      return MailMerge_ListRuns() || []
    },

    mailMergeCancelRun: async (runId: string) => {
      const { MailMerge_CancelRun } = await import('../../wailsjs/go/app/App.js')
      return MailMerge_CancelRun(runId)
    },

    mailMergeResumeRun: async (runId: string) => {
      const { MailMerge_ResumeRun } = await import('../../wailsjs/go/app/App.js')
      return MailMerge_ResumeRun(runId)
    },

    mailMergeSendNextBatch: async (runId: string) => {
      const { MailMerge_SendNextBatch } = await import('../../wailsjs/go/app/App.js')
      return MailMerge_SendNextBatch(runId)
    },

    mailMergeSendAllRemaining: async (runId: string) => {
      const { MailMerge_SendAllRemaining } = await import('../../wailsjs/go/app/App.js')
      return MailMerge_SendAllRemaining(runId)
    },

    mailMergeCancelRemaining: async (runId: string) => {
      const { MailMerge_CancelRemaining } = await import('../../wailsjs/go/app/App.js')
      return MailMerge_CancelRemaining(runId)
    },

    mailMergeGetSettings: async (accountId: string) => {
      const { MailMerge_GetSettings } = await import('../../wailsjs/go/app/App.js')
      // The generated backend.Settings type is the ai-writing shape;
      // the runtime value is the mail-merge shape (see mailmerge.ts).
      return (await MailMerge_GetSettings(accountId)) as unknown as MailMergeSettings
    },

    mailMergeSaveSettings: async (accountId: string, settings: MailMergeSettings) => {
      const { MailMerge_SaveSettings } = await import('../../wailsjs/go/app/App.js')
      return MailMerge_SaveSettings(accountId, settings as never)
    },

    mailMergeGetSigningInfo: async (accountId: string, fromEmail: string) => {
      const { MailMerge_GetSigningInfo } = await import('../../wailsjs/go/app/App.js')
      return MailMerge_GetSigningInfo(accountId, fromEmail)
    },

    mailMergePickFile: async (kind: string) => {
      const { MailMerge_PickFile } = await import('../../wailsjs/go/app/App.js')
      return MailMerge_PickFile(kind)
    },

    mailMergePickAttachmentDir: async () => {
      const { MailMerge_PickAttachmentDir } = await import('../../wailsjs/go/app/App.js')
      return MailMerge_PickAttachmentDir()
    },
  }
}

/**
 * Creates the composer API implementation for the detached composer window.
 * Uses ComposerApp bindings.
 */
export function createComposerWindowApi(_accountId: string): ComposerApi {
  return {
    sendMessage: async (accountId: string, message: smtp.ComposeMessage) => {
      const { SendMessage } = await import('../../wailsjs/go/app/ComposerApp.js')
      return SendMessage(accountId, message)
    },

    queueSend: async (accountId: string, message: smtp.ComposeMessage, draftId: string) => {
      const { QueueSend } = await import('../../wailsjs/go/app/ComposerApp.js')
      return QueueSend(accountId, message, draftId)
    },

    undoSend: async (queueId: string) => {
      const { UndoSend } = await import('../../wailsjs/go/app/ComposerApp.js')
      return UndoSend(queueId)
    },

    searchContacts: async (query: string, limit: number) => {
      const { SearchContacts } = await import('../../wailsjs/go/app/ComposerApp.js')
      return SearchContacts(query, limit) || []
    },

    getIdentities: async (accountId: string) => {
      const { GetIdentities } = await import('../../wailsjs/go/app/ComposerApp.js')
      return GetIdentities(accountId)
    },

    saveDraft: async (accountId: string, message: smtp.ComposeMessage, draftId: string) => {
      const { SaveDraft } = await import('../../wailsjs/go/app/ComposerApp.js')
      const result = await SaveDraft(accountId, message, draftId || '')
      return { id: result?.id || '', syncStatus: result?.syncStatus || 'pending' }
    },

    deleteDraft: async (draftId: string) => {
      const { DeleteDraft } = await import('../../wailsjs/go/app/ComposerApp.js')
      return DeleteDraft(draftId)
    },

    pickAttachmentFiles: async () => {
      const { PickAttachmentFiles } = await import('../../wailsjs/go/app/ComposerApp.js')
      return PickAttachmentFiles()
    },

    getAccount: async (accountId: string) => {
      const { GetAccount } = await import('../../wailsjs/go/app/ComposerApp.js')
      return GetAccount(accountId)
    },

    getDefaultAddress: async (kind: string, accountId: string) => {
      const { GetDefaultAddress } = await import('../../wailsjs/go/app/ComposerApp.js')
      return GetDefaultAddress(kind, accountId)
    },

    hasSMIMECertificate: async (accountId: string) => {
      const { HasSMIMECertificate } = await import('../../wailsjs/go/app/ComposerApp.js')
      return HasSMIMECertificate(accountId)
    },

    getSMIMECertificateForEmail: async (accountId: string, email: string) => {
      const { GetSMIMECertificateForEmail } = await import('../../wailsjs/go/app/ComposerApp.js')
      return GetSMIMECertificateForEmail(accountId, email)
    },

    getSMIMESignPolicy: async (accountId: string) => {
      const { GetSMIMESignPolicy } = await import('../../wailsjs/go/app/ComposerApp.js')
      return GetSMIMESignPolicy(accountId)
    },

    getSMIMEEncryptPolicy: async (accountId: string) => {
      const { GetSMIMEEncryptPolicy } = await import('../../wailsjs/go/app/ComposerApp.js')
      return GetSMIMEEncryptPolicy(accountId)
    },

    checkRecipientCerts: async (emails: string[]) => {
      const { CheckRecipientCerts } = await import('../../wailsjs/go/app/ComposerApp.js')
      return CheckRecipientCerts(emails)
    },

    pickRecipientCertFile: async () => {
      const { PickRecipientCertFile } = await import('../../wailsjs/go/app/ComposerApp.js')
      return PickRecipientCertFile()
    },

    importRecipientCert: async (email: string, filePath: string) => {
      const { ImportRecipientCert } = await import('../../wailsjs/go/app/ComposerApp.js')
      return ImportRecipientCert(email, filePath)
    },

    hasPGPKey: async (accountId: string) => {
      const { HasPGPKey } = await import('../../wailsjs/go/app/ComposerApp.js')
      return HasPGPKey(accountId)
    },

    getPGPKeyForEmail: async (accountId: string, email: string) => {
      const { GetPGPKeyForEmail } = await import('../../wailsjs/go/app/ComposerApp.js')
      return GetPGPKeyForEmail(accountId, email)
    },

    getPGPSignPolicy: async (accountId: string) => {
      const { GetPGPSignPolicy } = await import('../../wailsjs/go/app/ComposerApp.js')
      return GetPGPSignPolicy(accountId)
    },

    getPGPEncryptPolicy: async (accountId: string) => {
      const { GetPGPEncryptPolicy } = await import('../../wailsjs/go/app/ComposerApp.js')
      return GetPGPEncryptPolicy(accountId)
    },

    checkRecipientPGPKeys: async (emails: string[]) => {
      const { CheckRecipientPGPKeys } = await import('../../wailsjs/go/app/ComposerApp.js')
      return CheckRecipientPGPKeys(emails)
    },

    pickRecipientPGPKeyFile: async () => {
      const { PickRecipientPGPKeyFile } = await import('../../wailsjs/go/app/ComposerApp.js')
      return PickRecipientPGPKeyFile()
    },

    importRecipientPGPKey: async (email: string, filePath: string) => {
      const { ImportRecipientPGPKey } = await import('../../wailsjs/go/app/ComposerApp.js')
      return ImportRecipientPGPKey(email, filePath)
    },

    lookupWKD: async (email: string) => {
      const { LookupWKD } = await import('../../wailsjs/go/app/ComposerApp.js')
      return LookupWKD(email)
    },

    lookupHKP: async (email: string) => {
      const { LookupHKP } = await import('../../wailsjs/go/app/ComposerApp.js')
      return LookupHKP(email)
    },

    lookupPGPKey: async (email: string) => {
      const { LookupPGPKey } = await import('../../wailsjs/go/app/ComposerApp.js')
      return LookupPGPKey(email)
    },

    readFileAsAttachment: async (filePath: string) => {
      const { ReadFileAsAttachment } = await import('../../wailsjs/go/app/ComposerApp.js')
      return ReadFileAsAttachment(filePath)
    },

    isFlatpak: async () => {
      const { IsFlatpak } = await import('../../wailsjs/go/app/ComposerApp.js')
      return IsFlatpak()
    },

    getAllAccountIdentities: async () => {
      const { GetAllAccountIdentities } = await import('../../wailsjs/go/app/ComposerApp.js')
      return GetAllAccountIdentities()
    },

    listComposerToolbarItems: async () => {
      const { ListComposerToolbarItems } = await import('../../wailsjs/go/app/ComposerApp.js')
      return ListComposerToolbarItems() || []
    },

    aiWritingListActions: async () => {
      const { AIWriting_ListActions } = await import('../../wailsjs/go/app/ComposerApp.js')
      return AIWriting_ListActions() || []
    },

    aiWritingImprove: async (req: backend.ImproveRequest) => {
      const { AIWriting_Improve } = await import('../../wailsjs/go/app/ComposerApp.js')
      return AIWriting_Improve(req)
    },

    aiWritingGetActionPrompts: async () => {
      const { AIWriting_GetActionPrompts } = await import('../../wailsjs/go/app/ComposerApp.js')
      return AIWriting_GetActionPrompts() || []
    },

    aiWritingListModels: async () => {
      const { AIWriting_ListModels } = await import('../../wailsjs/go/app/ComposerApp.js')
      return AIWriting_ListModels() || []
    },

    aiWritingGetSettings: async () => {
      const { AIWriting_GetSettings } = await import('../../wailsjs/go/app/ComposerApp.js')
      return AIWriting_GetSettings()
    },

    aiWritingSaveSettings: async (input: backend.SettingsInput) => {
      const { AIWriting_SaveSettings } = await import('../../wailsjs/go/app/ComposerApp.js')
      return AIWriting_SaveSettings(input)
    },

    aiWritingGetStatus: async () => {
      const { AIWriting_GetStatus } = await import('../../wailsjs/go/app/ComposerApp.js')
      return AIWriting_GetStatus()
    },

    mailMergeParseFile: async (path: string, kind: string) => {
      const { MailMerge_ParseFile } = await import('../../wailsjs/go/app/ComposerApp.js')
      return MailMerge_ParseFile(path, kind)
    },

    mailMergeParseFileFull: async (path: string, kind: string, sheet: string) => {
      const { MailMerge_ParseFileFull } = await import('../../wailsjs/go/app/ComposerApp.js')
      return MailMerge_ParseFileFull(path, kind, sheet)
    },

    mailMergeGetContacts: async () => {
      const { MailMerge_GetContacts } = await import('../../wailsjs/go/app/ComposerApp.js')
      return MailMerge_GetContacts()
    },

    mailMergePreview: async (template: MailMergeTemplate, rows: Row[], indices: number[]) => {
      const { MailMerge_Preview } = await import('../../wailsjs/go/app/ComposerApp.js')
      return MailMerge_Preview(template as never, rows, indices) || []
    },

    mailMergeValidate: async (template: MailMergeTemplate, rows: Row[]) => {
      const { MailMerge_Validate } = await import('../../wailsjs/go/app/ComposerApp.js')
      return MailMerge_Validate(template as never, rows)
    },

    mailMergeStartRun: async (req: MailMergeRunRequest) => {
      const { MailMerge_StartRun } = await import('../../wailsjs/go/app/ComposerApp.js')
      return MailMerge_StartRun(req as never)
    },

    mailMergeGetRun: async (runId: string) => {
      const { MailMerge_GetRun } = await import('../../wailsjs/go/app/ComposerApp.js')
      return MailMerge_GetRun(runId)
    },

    mailMergeListRuns: async () => {
      const { MailMerge_ListRuns } = await import('../../wailsjs/go/app/ComposerApp.js')
      return MailMerge_ListRuns() || []
    },

    mailMergeCancelRun: async (runId: string) => {
      const { MailMerge_CancelRun } = await import('../../wailsjs/go/app/ComposerApp.js')
      return MailMerge_CancelRun(runId)
    },

    mailMergeResumeRun: async (runId: string) => {
      const { MailMerge_ResumeRun } = await import('../../wailsjs/go/app/ComposerApp.js')
      return MailMerge_ResumeRun(runId)
    },

    mailMergeSendNextBatch: async (runId: string) => {
      const { MailMerge_SendNextBatch } = await import('../../wailsjs/go/app/ComposerApp.js')
      return MailMerge_SendNextBatch(runId)
    },

    mailMergeSendAllRemaining: async (runId: string) => {
      const { MailMerge_SendAllRemaining } = await import('../../wailsjs/go/app/ComposerApp.js')
      return MailMerge_SendAllRemaining(runId)
    },

    mailMergeCancelRemaining: async (runId: string) => {
      const { MailMerge_CancelRemaining } = await import('../../wailsjs/go/app/ComposerApp.js')
      return MailMerge_CancelRemaining(runId)
    },

    mailMergeGetSettings: async (accountId: string) => {
      const { MailMerge_GetSettings } = await import('../../wailsjs/go/app/ComposerApp.js')
      // The generated backend.Settings type is the ai-writing shape;
      // the runtime value is the mail-merge shape (see mailmerge.ts).
      return (await MailMerge_GetSettings(accountId)) as unknown as MailMergeSettings
    },

    mailMergeSaveSettings: async (accountId: string, settings: MailMergeSettings) => {
      const { MailMerge_SaveSettings } = await import('../../wailsjs/go/app/ComposerApp.js')
      return MailMerge_SaveSettings(accountId, settings as never)
    },

    mailMergeGetSigningInfo: async (accountId: string, fromEmail: string) => {
      const { MailMerge_GetSigningInfo } = await import('../../wailsjs/go/app/ComposerApp.js')
      return MailMerge_GetSigningInfo(accountId, fromEmail)
    },

    mailMergePickFile: async (kind: string) => {
      const { MailMerge_PickFile } = await import('../../wailsjs/go/app/ComposerApp.js')
      return MailMerge_PickFile(kind)
    },

    mailMergePickAttachmentDir: async () => {
      const { MailMerge_PickAttachmentDir } = await import('../../wailsjs/go/app/ComposerApp.js')
      return MailMerge_PickAttachmentDir()
    },
  }
}
