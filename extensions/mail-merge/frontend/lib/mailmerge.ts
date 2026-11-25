// Shared types and file parsers for the mail-merge extension frontend.
//
// The types mirror the backend contract exactly (extensions/mail-merge/
// backend/bridge.go + internal/mailmerge/types.go + settings.go). They
// are declared locally — NOT imported from the generated wailsjs models —
// for two reasons, both mirroring extensions/ai-writing/frontend/lib/
// aiwriting.ts:
//   1. MailMerge_GetSettings / MailMerge_SaveSettings take the
//      mail-merge `Settings` (capEnabled/capLimit/...), but the generated
//      `backend.Settings` in frontend/wailsjs/go/models.ts is the
//      ai-writing settings shape (selectedModel/...): both backend
//      packages are named `backend`, so the generator kept only one
//      Settings class. The runtime JSON is the mail-merge shape; the
//      generated TS type is wrong for it. Local types + a cast at the
//      call site keep this honest. (A Go-side rename of one Settings
//      type would fix the codegen; that is out of scope for the
//      frontend-only Phase 1.)
//   2. MailMerge_ParseFile returns only the first 3 preview rows, so a
//      CSV/JSON run cannot get its full row table from the backend. The
//      frontend reads the picked file's bytes itself (via the existing
//      ComposerApi.readFileAsAttachment, which base64-encodes any file)
//      and parses them here, mirroring internal/mailmerge/sources.go
//      (delimiter auto-detect, `sep=` line, quoted fields, windows-1252
//      fallback, JSON arrays-of-arrays and arrays-of-objects).
//      MailMerge_ParseFile is still called after picking, as a
//      server-side cross-check of columns + row count.
//
// Credit: variable syntax and CSV/JSON source formats follow
// Thunderbird's Mail Merge add-on by Alexander Bergmann, as documented
// in MAIL_MERGE_PLAN.md. No add-on code was read.

export type Row = Record<string, string>

export interface MailMergeSettings {
  capEnabled: boolean
  capLimit: number
  defaultPauseSeconds: number
  defaultRandomPause: boolean
  defaultDeliverMode: string // "drafts" | "send"
  // SignMessages signs every generated message with the account's
  // configured identity (S/MIME preferred, else PGP).
  // SignMessagesSet records that the user chose this explicitly in the
  // merge dialog; when false the dialog defaults to the account's
  // composer signing setting instead.
  signMessages: boolean
  signMessagesSet: boolean
}

export const DEFAULT_MAIL_MERGE_SETTINGS: MailMergeSettings = {
  capEnabled: true,
  capLimit: 100,
  defaultPauseSeconds: 2,
  defaultRandomPause: false,
  defaultDeliverMode: 'drafts',
  signMessages: false,
  signMessagesSet: false,
}

export interface MailMergeGlobalAttachment {
  filename: string
  contentType: string
  size: number
  dataBase64: string
}

export interface MailMergeTemplate {
  accountId: string
  from: string
  to: string
  cc: string
  bcc: string
  replyTo: string
  subject: string
  bodyHtml: string
  bodyText: string
  isPlainText: boolean
  globalAttachments: MailMergeGlobalAttachment[]
  attachmentField: string
  // Sign requests per-recipient signing (Phase 3). signKind is the
  // kind resolved by the backend before the run ("smime" | "pgp").
  sign: boolean
  signKind?: string
}

export interface MailMergeRunRequest {
  template: MailMergeTemplate
  rows: Row[]
  sourceKind: string // "csv" | "json" | "xlsx" | "contacts"
  deliverMode: string // "drafts" | "send" | "later" ("later" = outbox queue)
  pauseSeconds: number
  randomPause: boolean
  startRow: number
  stopRow: number
  capEnabled: boolean
  capLimit: number
  accountId: string
  attachmentDir: string
}

export interface MailMergeInvalidEmail {
  row: number
  address: string
}

export interface MailMergeMissingAttachment {
  row: number
  path: string
}

export interface MailMergeValidationResult {
  unresolvedVariables: string[]
  invalidEmails: MailMergeInvalidEmail[]
  duplicateEmails: number
  emptyToRows: number[]
  missingAttachmentFiles: MailMergeMissingAttachment[]
  // Set when signing was requested but the account has no usable
  // signing identity. Blocks the run like an unresolved variable.
  signingError?: string
}

// MailMergeSigningInfo is the account's signing setup, from
// MailMerge_GetSigningInfo: whether a usable S/MIME certificate or
// PGP key exists, which kind would be used, and whether the
// composer's sign policy would sign by default.
export interface MailMergeSigningInfo {
  available: boolean
  kind: string // "smime" | "pgp" | ""
  defaultSign: boolean
  reason?: string
}

export interface MailMergeRenderedMessage {
  rowIndex: number
  to: string
  cc: string
  bcc: string
  subject: string
  bodyHtml: string
  bodyText: string
  attachments: string[]
}

export interface MailMergeRowResult {
  index: number
  email: string
  status: string // sent | draft-saved | failed | skipped | held | queued
  error?: string
  timestamp: string
}

export interface MailMergeRun {
  id: string
  createdAt: string
  accountId: string
  deliverMode: string
  status: string // running | done | cancelled | held | queued | failed
  totalRows: number
  sentCount: number
  failedCount: number
  skippedCount: number
  heldCount: number
  queuedCount: number
  rows: MailMergeRowResult[]
  error?: string // whole-run failure (status 'failed' only)
}

export interface MailMergeRunSummary {
  id: string
  createdAt: string
  accountId: string
  deliverMode: string
  status: string
  totalRows: number
  sentCount: number
  failedCount: number
  skippedCount: number
  heldCount: number
  queuedCount: number
  error?: string // whole-run failure (status 'failed' only)
}

export interface MailMergeParsedFile {
  columns: string[]
  rowCount: number
  previewRows: Row[]
  sheets?: string[] // XLSX workbook sheet names, in order
}

export interface MailMergeParsedFileFull {
  columns: string[]
  rows: Row[]
  sheets?: string[]
  rowCount: number
}

// --- Outbox queue (Phase 2) ---

export interface MailMergeQueuedItem {
  id: string
  runId: string
  rowIndex: number
  accountId: string
  recipient: string
  subject: string
  fromAddress: string
  to: string[]
  status: string // queued | sending | sent | failed | cancelled
  attempts: number
  maxAttempts: number
  nextAttemptAt: string
  lastError?: string
  createdAt: string
  sentAt?: string
}

export interface MailMergeQueueRunStats {
  runId: string
  queued: number
  sent: number
  failed: number
  cancelled: number
  paused: boolean
}

export interface MailMergeQueueStats {
  total: number
  queued: number
  sent: number
  failed: number
  cancelled: number
  runs: MailMergeQueueRunStats[]
}

export interface MailMergeQueueConfig {
  perHour: number
  paused: boolean
  // countNormalSends (re-audit R27, opt-in, default false): when
  // true, normal composer sends dispatched through the Outbox also
  // count toward the same hourly budget; when false (the default)
  // the budget counts Mail Merge mail only.
  countNormalSends: boolean
}

export interface MailMergeDispatchResult {
  sent: number
  failed: number
  remaining: number
  queued: number
}

export interface MailMergeCancelQueuedRunResult {
  cancelled: number
  inFlight: number
}

export interface MailMergeContactsResult {
  rows: Row[]
  columns: string[]
}

export interface ParsedSource {
  columns: string[]
  rows: Row[]
}

// ---------------------------------------------------------------------------
// Null-array normalisation
// ---------------------------------------------------------------------------
// Go nil slices arrive over the Wails bridge as JSON null, not []. The
// Svelte templates read `.length` / `{#each}` on these arrays, so a null
// crashes the render (the results section never paints and the dialog looks
// frozen). Normalise every backend-provided array at the assignment boundary.
export function normalizeValidationResult(raw: MailMergeValidationResult): MailMergeValidationResult {
  return {
    ...raw,
    unresolvedVariables: raw.unresolvedVariables ?? [],
    invalidEmails: raw.invalidEmails ?? [],
    emptyToRows: raw.emptyToRows ?? [],
    missingAttachmentFiles: raw.missingAttachmentFiles ?? [],
    signingError: raw.signingError ?? '',
  }
}

export function normalizeRenderedMessage(raw: MailMergeRenderedMessage): MailMergeRenderedMessage {
  return { ...raw, attachments: raw.attachments ?? [] }
}

export function normalizeRun(raw: MailMergeRun): MailMergeRun {
  return { ...raw, rows: raw.rows ?? [] }
}

export function normalizeQueueItems(raw: MailMergeQueuedItem[] | null | undefined): MailMergeQueuedItem[] {
  return (raw ?? []).map((item) => ({ ...item, to: item.to ?? [] }))
}

export function normalizeQueueStats(raw: MailMergeQueueStats): MailMergeQueueStats {
  return { ...raw, runs: raw.runs ?? [] }
}

// ---------------------------------------------------------------------------
// Address formatting (smtp.Address is { name, address })
// ---------------------------------------------------------------------------

export function formatAddress(name: string, address: string): string {
  const n = (name || '').trim()
  const a = (address || '').trim()
  if (!a) return ''
  return n ? `${n} <${a}>` : a
}

export function formatAddressList(list: Array<{ name: string; address: string }>): string {
  return (list || [])
    .map((r) => formatAddress(r.name, r.address))
    .filter(Boolean)
    .join(', ')
}

// ---------------------------------------------------------------------------
// base64 helpers (readFileAsAttachment returns base64 file content)
// ---------------------------------------------------------------------------

export function base64ToBytes(b64: string): Uint8Array {
  const bin = atob(b64)
  const bytes = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
  return bytes
}

/**
 * Decode file bytes the way Go's decodeCSVBytes does with charset "":
 * strip a UTF-8 BOM, use UTF-8 when the bytes are valid, else fall
 * back to windows-1252.
 */
export function decodeFileBytes(bytes: Uint8Array): string {
  let buf = bytes
  if (buf.length >= 3 && buf[0] === 0xef && buf[1] === 0xbb && buf[2] === 0xbf) {
    buf = buf.subarray(3)
  }
  try {
    return new TextDecoder('utf-8', { fatal: true }).decode(buf)
  } catch {
    return new TextDecoder('windows-1252').decode(buf)
  }
}

// ---------------------------------------------------------------------------
// CSV parsing — mirrors internal/mailmerge/sources.go ParseCSV
// ---------------------------------------------------------------------------

function dedupColumns(headers: string[]): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const h of headers) {
    if (seen.has(h)) continue
    seen.add(h)
    out.push(h)
  }
  return out
}

function countUnquoted(s: string, delim: string): number {
  let n = 0
  let inQuotes = false
  for (let i = 0; i < s.length; i++) {
    const c = s[i]
    if (c === '"') {
      if (inQuotes && i + 1 < s.length && s[i + 1] === '"') {
        i++
      } else {
        inQuotes = !inQuotes
      }
    } else if (c === delim && !inQuotes) {
      n++
    }
  }
  return n
}

function firstLine(s: string): string {
  const i = s.indexOf('\n')
  return (i >= 0 ? s.slice(0, i) : s).replace(/\r$/, '')
}

function detectDelimiter(headerLine: string): string {
  // Ties prefer comma, then semicolon, then tab, then colon (Go order,
  // strict-greater replacement).
  const candidates = [',', ';', '\t', ':']
  let best = ','
  let bestCount = -1
  for (const c of candidates) {
    const n = countUnquoted(headerLine, c)
    if (n > bestCount) {
      best = c
      bestCount = n
    }
  }
  return best
}

/** Minimal RFC-4180 reader: quoted fields, doubled quotes, embedded
 *  newlines, CRLF/LF. Throws on an unterminated quoted field. */
function parseCsvRecords(text: string, delim: string): string[][] {
  const records: string[][] = []
  let record: string[] = []
  let field = ''
  let inQuotes = false
  let fieldStarted = false
  let i = 0
  const pushField = () => {
    record.push(field)
    field = ''
    fieldStarted = false
  }
  const pushRecord = () => {
    pushField()
    records.push(record)
    record = []
  }
  while (i < text.length) {
    const c = text[i]
    if (inQuotes) {
      if (c === '"') {
        if (i + 1 < text.length && text[i + 1] === '"') {
          field += '"'
          i += 2
        } else {
          inQuotes = false
          i++
        }
      } else {
        field += c
        i++
      }
    } else if (c === '"' && !fieldStarted && field === '') {
      inQuotes = true
      fieldStarted = true
      i++
    } else if (c === delim) {
      pushField()
      i++
    } else if (c === '\r') {
      // CRLF or lone CR ends the record.
      if (i + 1 < text.length && text[i + 1] === '\n') i++
      pushRecord()
      i++
    } else if (c === '\n') {
      pushRecord()
      i++
    } else {
      field += c
      fieldStarted = true
      i++
    }
  }
  if (inQuotes) throw new Error('mail-merge: parse CSV: unterminated quoted field')
  // Trailing record without a newline.
  if (field !== '' || fieldStarted || record.length > 0) pushRecord()
  // Drop a final empty record produced by a trailing newline.
  while (
    records.length > 0 &&
    records[records.length - 1].length === 1 &&
    records[records.length - 1][0] === ''
  ) {
    records.pop()
  }
  return records
}

export function parseCsvText(input: string): ParsedSource {
  if (input.trim() === '') throw new Error('mail-merge: empty CSV file')
  let text = input
  // Strip a UTF-8 BOM when the caller passed a decoded string.
  if (text.charCodeAt(0) === 0xfeff) text = text.slice(1)
  let delimiter = ''
  if (text.startsWith('sep=')) {
    const nl = text.indexOf('\n')
    if (nl >= 0) {
      const first = text.slice(0, nl).replace(/\r$/, '')
      if (first.length === 5) {
        // "sep=" + exactly one char.
        delimiter = first[4]
        text = text.slice(nl + 1)
      }
    }
  }
  if (!delimiter) delimiter = detectDelimiter(firstLine(text))
  const records = parseCsvRecords(text, delimiter)
  if (records.length === 0) throw new Error('mail-merge: empty CSV file')
  const headers = records[0]
  if (headers.length === 0 || (headers.length === 1 && headers[0].trim() === '')) {
    throw new Error('mail-merge: CSV first row must contain column names')
  }
  const columns = dedupColumns(headers)
  const rows: Row[] = []
  for (const rec of records.slice(1)) {
    const row: Row = {}
    for (let i = 0; i < columns.length; i++) {
      row[columns[i]] = i < rec.length ? rec[i] : ''
    }
    rows.push(row)
  }
  return { columns, rows }
}

// ---------------------------------------------------------------------------
// JSON parsing — mirrors internal/mailmerge/sources.go ParseJSON
// ---------------------------------------------------------------------------

function stringifyJsonValue(v: unknown): string {
  if (v === null || v === undefined) return ''
  if (typeof v === 'string') return v
  if (typeof v === 'number') return String(v)
  if (typeof v === 'boolean') return v ? 'true' : 'false'
  return JSON.stringify(v) ?? ''
}

export function parseJsonText(input: string): ParsedSource {
  const text = input.trim()
  if (text === '') throw new Error('mail-merge: empty JSON file')
  let raw: unknown
  try {
    raw = JSON.parse(text)
  } catch (err) {
    throw new Error(`mail-merge: parse JSON: ${(err as Error).message}`)
  }
  if (!Array.isArray(raw)) {
    throw new Error('mail-merge: JSON must be an array of arrays or an array of objects')
  }
  if (raw.length === 0) throw new Error('mail-merge: empty JSON file')
  if (Array.isArray(raw[0])) {
    const rowsArr = raw as unknown[][]
    const headers = rowsArr[0]
    for (const h of headers) {
      if (typeof h !== 'string') {
        throw new Error('mail-merge: JSON header row must contain strings')
      }
    }
    const columns = dedupColumns(headers as string[])
    const rows: Row[] = []
    for (const rec of rowsArr.slice(1)) {
      const row: Row = {}
      for (let i = 0; i < columns.length; i++) {
        row[columns[i]] = i < rec.length ? stringifyJsonValue(rec[i]) : ''
      }
      rows.push(row)
    }
    return { columns, rows }
  }
  if (raw[0] !== null && typeof raw[0] === 'object') {
    // Array of objects: first object's key order is the column order.
    const columns = dedupColumns(Object.keys(raw[0] as Record<string, unknown>))
    const rows: Row[] = []
    for (const item of raw as Record<string, unknown>[]) {
      const row: Row = {}
      for (const col of columns) {
        row[col] = col in item ? stringifyJsonValue(item[col]) : ''
      }
      rows.push(row)
    }
    return { columns, rows }
  }
  throw new Error('mail-merge: JSON must be an array of arrays or an array of objects')
}

/** Parse a picked source file's bytes by kind ("csv" | "json"). */
export function parseSourceBytes(bytes: Uint8Array, kind: string): ParsedSource {
  const text = decodeFileBytes(bytes)
  if (kind === 'csv') return parseCsvText(text)
  if (kind === 'json') return parseJsonText(text)
  throw new Error(`mail-merge: unknown file kind ${JSON.stringify(kind)}`)
}
