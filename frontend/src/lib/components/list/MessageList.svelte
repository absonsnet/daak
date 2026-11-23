<script lang="ts" module>
  // Session-only memory of how far the user paginated ("Load more"), where
  // they scrolled, and which thread was selected (the keyboard-nav anchor),
  // per folder view. Module scope so it survives folder switches and
  // component remounts for the life of the app session; gone on quit.
  // Keyed "accountId:folderId" ("unified:inbox" for the unified view).
  const sessionListState = new Map<
    string,
    { loadedCount: number; scrollTop: number; selectedThreadId: string | null }
  >()
</script>

<script lang="ts">
  import { onMount, onDestroy } from 'svelte'
  import Icon from '@iconify/svelte'
  import ConversationRow from './ConversationRow.svelte'
  import NeedsAttentionStrip from './NeedsAttentionStrip.svelte'
  import { DropdownMenu } from 'bits-ui'
  import { cn } from '$lib/utils'
  import { Button } from '$lib/components/ui/button'
  // @ts-ignore - wailsjs bindings
  import { GetConversations, GetConversationCount, SyncFolder, ForceSyncFolder, CancelFolderSync, SetMessageListSortOrder, GetUnifiedFolderConversations, GetUnifiedFolderCount, SearchConversations, SearchUnifiedFolder, GetSearchCount, GetSearchCountUnifiedFolder, GetFTSIndexStatus, IsFTSIndexing, Trash, DeletePermanently, EmptyTrash, EmptySpam, Undo, IMAPSearchFolder, FetchServerMessage, MarkAsRead, MarkAsUnread, Archive, Star, Unstar, MarkAsSpam, MarkAsNotSpam, MoveToFolder, GetAccounts, GetCategoryCounts, GetInboxTabDefault, MailMerge_ListQueue, MailMerge_SendUnsent, MailMerge_CancelQueuedItem, ListSendQueue, RetrySendQueueItem } from '../../../../wailsjs/go/app/App'
  import { toasts } from '$lib/stores/toast'
  import { _ } from '$lib/i18n'
  import { ConfirmDialog } from '$lib/components/ui/confirm-dialog'
  import FolderPickerDialog from '$lib/components/common/FolderPickerDialog.svelte'
  // @ts-ignore - wailsjs path
  import { message, account, app, sendqueue } from '../../../../wailsjs/go/models'
  // @ts-ignore - wailsjs runtime
  import { EventsOn, EventsOff } from '../../../../wailsjs/runtime/runtime'
  import { getMessageListDensity, getMessageListSortOrder, setMessageListSortOrder, getShowMessageListProfilePics } from '$lib/stores/settings.svelte'
  import { normalizeQueueItems, type MailMergeQueuedItem, type MailMergeDispatchResult } from '$extensions/mail-merge/frontend/lib/mailmerge'
  import { contactPhotos } from '$lib/stores/contactPhotos.svelte'
  import { accountStore } from '$lib/stores/accounts.svelte'
  import { getLayoutMode, hideViewer } from '$lib/stores/layout.svelte'
  import { dialogGuardOpen, dialogGuardClose, isDialogGuardActive } from '$lib/stores/dialogGuard'

  interface Props {
    accountId?: string | null
    folderId?: string | null
    folderName?: string
    folderType?: string
    onConversationSelect?: (threadId: string, folderId: string, accountId: string) => void
    // Called (instead of onConversationSelect) when the user explicitly
    // opens a conversation — double-click or Enter — while the list is in
    // select-only mode (preview pane off). Receives the subject for the tab label.
    onConversationOpen?: (threadId: string, folderId: string, accountId: string, subject: string) => void
    // Select-only mode (preview pane off): a single click only highlights
    // the row; opening needs a double-click or Enter via onConversationOpen.
    selectOnly?: boolean
    onReply?: (mode: 'reply' | 'reply-all' | 'forward', messageId: string) => void
    onRowActionComplete?: () => void
    // Undo Send: the Outbox Undo button on a normal-send row. The
    // parent cancels the queued send and reopens the composer with
    // the draft (App.svelte owns that flow).
    onUndoSend?: (queueId: string, draftId: string, accountId: string) => void
    isFocused?: boolean
    isFlashing?: boolean
    showFolderToggle?: boolean
    onToggleSidebar?: () => void
  }

  let {
    accountId = null,
    folderId = null,
    folderName = 'Inbox',
    folderType = 'inbox',
    onConversationSelect,
    onConversationOpen,
    selectOnly = false,
    onReply,
    onRowActionComplete,
    onUndoSend,
    isFocused: _isFocused = false,
    isFlashing = false,
    showFolderToggle = false,
    onToggleSidebar,
  }: Props = $props()

  // State
  let attentionStripRef = $state<{ refresh: () => Promise<void> } | undefined>()
  let conversations = $state<message.Conversation[]>([])
  let totalCount = $state(0)
  let loading = $state(false)
  let error = $state<string | null>(null)
  let selectedThreadId = $state<string | null>(null)
  // A selection requested from outside (notification click) while the list
  // may still be loading its folder. Applied when the load lands, and it
  // takes precedence over the auto-select-first and session-restore writers,
  // which otherwise overwrite it (regression since the session restore).
  let pendingSelectThreadId: string | null = null
  let lastLoadedFolderId = $state<string | null>(null) // Track folder changes
  let loadGeneration = $state(0) // Invalidates stale async results when folder changes mid-load (#200)

  // Unified views: the sidebar's Unified zone merges one folder type
  // across all accounts (folderId is the type: inbox/starred/sent/
  // drafts/spam/trash). Outbox is the local send queue, not a message
  // folder, so it is a unified selection but not a conversation view.
  const isUnifiedInboxView = $derived(accountId === 'unified' && folderId === 'inbox')
  const isUnifiedView = $derived(accountId === 'unified' && !!folderId && folderId !== 'outbox')
  const isOutboxView = $derived(accountId === 'unified' && folderId === 'outbox')

  // Derived: check if this folder is currently syncing (from account store's progress tracking)
  // In a unified view, any folder of the selected type syncing counts — plus folders
  // this unified refresh started that have not settled yet, which covers
  // the gap before the backend's first sync:progress event lands.
  function getUnifiedSyncTargets(): { accountId: string; folderId: string }[] {
    // Starred flags live in messages across folders, so a Starred refresh
    // syncs the five folder-type folders of every account.
    const types = folderId === 'starred'
      ? ['inbox', 'sent', 'drafts', 'spam', 'trash']
      : folderId ? [folderId] : []
    const targets: { accountId: string; folderId: string }[] = []
    const walk = (accountId: string, trees: { folder?: { id: string; type: string }; children?: typeof trees }[]) => {
      for (const tree of trees) {
        if (tree.folder && types.includes(tree.folder.type)) {
          targets.push({ accountId, folderId: tree.folder.id })
        }
        if (tree.children) walk(accountId, tree.children)
      }
    }
    for (const acc of accountStore.accounts) {
      walk(acc.account.id, acc.folders as never)
    }
    return targets
  }

  const unifiedFolders = $derived(getUnifiedSyncTargets())

  // Keys ("accountId:folderId") of folder syncs started by a unified
  // refresh that have not settled yet. Plain $state array, reassigned on
  // change so the derived syncing flag below stays reactive.
  let unifiedSyncKeys = $state<string[]>([])

  const unifiedSyncing = $derived(
    isUnifiedView &&
      (unifiedSyncKeys.length > 0 ||
        unifiedFolders.some(
          (f) => accountStore.syncProgress[f.accountId]?.[f.folderId] !== undefined
        ))
  )

  const syncing = $derived(
    isUnifiedView
      ? unifiedSyncing
      : !!(accountId && folderId && accountStore.syncProgress[accountId]?.[folderId] !== undefined)
  )

  // Derived: get sync progress for this folder (if syncing). In the unified
  // view, the first folder of the selected type currently reporting progress.
  const syncProgress = $derived(
    isUnifiedView
      ? (unifiedFolders
          .map((f) => accountStore.syncProgress[f.accountId]?.[f.folderId])
          .find((p) => p !== undefined) ?? null)
      : accountId && folderId
        ? accountStore.syncProgress[accountId]?.[folderId]
        : null
  )

  // Whether there is anything the refresh control can sync: a selected
  // folder, or at least one folder of the unified type. List loading (DB
  // load) deliberately does NOT disable refresh.
  const hasSyncTarget = $derived(
    isUnifiedView ? unifiedFolders.length > 0 : !!(accountId && folderId)
  )

  // Outbox (local send queues): Mail Merge "Send later" runs AND
  // normal sends waiting out the Undo Send window. Both share one
  // list: normal sends are `kind: 'send'` with status 'pending'
  // (in the undo window or on retry backoff) and carry the draft to
  // reopen on Undo; reactions are `kind: 'reaction'` (same pending
  // window, no draft); Mail Merge rows are `kind: 'mailmerge'`
  // with status 'queued'. ListQueue returns nil when the extension
  // is disabled, so a disabled extension reads as an empty outbox.
  // One shared row shape; the template branches on `kind` for the
  // Undo/Retry/Cancel actions. Sent and cancelled rows are deleted
  // by the backend on success/undo, so the list shows only rows
  // still in flight or failed — there is no history section.
  interface OutboxRow {
    id: string
    kind: 'mailmerge' | 'send' | 'reaction'
    accountId: string
    draftId?: string
    recipient: string
    subject: string
    fromAddress: string
    to: string[]
    status: string
    attempts: number
    maxAttempts: number
    nextAttemptAt?: string
    sendAt?: string
    lastError?: string
    createdAt?: string
    sentAt?: string
  }

  let queueItems = $state<OutboxRow[]>([])
  let queueLoading = $state(false)
  let queueError = $state<string | null>(null)
  let expandedQueueId = $state<string | null>(null)
  let queueActionBusy = $state('')

  const waitingQueueItems = $derived(queueItems.filter((i) => i.status === 'queued' || i.status === 'sending' || i.status === 'pending'))
  // "Send unsent now" only dispatches Mail Merge rows — normal sends
  // go out on their own countdown, so the button enables on those.
  const waitingMailMergeItems = $derived(waitingQueueItems.filter((i) => i.kind === 'mailmerge'))
  const failedQueueItems = $derived(queueItems.filter((i) => i.status === 'failed'))

  // Waiting, then failed — each group only when non-empty (the
  // template skips empty groups). Sent and cancelled rows are
  // deleted by the backend, so there is no history group.
  const queueGroups = $derived([
    { key: 'waiting', label: $_('messageList.outboxStatusQueued'), items: waitingQueueItems },
    { key: 'failed', label: $_('messageList.outboxStatusFailed'), items: failedQueueItems },
  ].filter((g) => g.items.length > 0))

  async function loadQueue() {
    queueLoading = true
    queueError = null
    try {
      const [mmItems, sendItems] = await Promise.all([
        MailMerge_ListQueue(),
        ListSendQueue(),
      ])
      // Sent and cancelled rows are deleted by the backend, but a
      // fallback row can survive a failed delete — filter them out
      // here too so the history section never reappears.
      const mmRows: OutboxRow[] = normalizeQueueItems((mmItems as MailMergeQueuedItem[] | null) ?? []).filter((i) => i.status !== 'sent' && i.status !== 'cancelled').map((i) => ({
        id: i.id,
        kind: 'mailmerge',
        accountId: i.accountId,
        recipient: i.recipient,
        subject: i.subject,
        fromAddress: i.fromAddress,
        to: i.to ?? [],
        status: i.status,
        attempts: i.attempts,
        maxAttempts: i.maxAttempts,
        nextAttemptAt: i.nextAttemptAt,
        lastError: i.lastError,
        createdAt: i.createdAt,
        sentAt: i.sentAt,
      }))
      // Normal sends first (newest action on top), then the Mail
      // Merge queue in its existing order. Reaction rows keep their
      // kind so the template can label them; they have no draft.
      const sendRows: OutboxRow[] = ((sendItems as sendqueue.Item[] | null) ?? []).filter((i) => i.status !== 'sent' && i.status !== 'cancelled').map((i) => ({
        id: i.id,
        kind: i.kind === 'reaction' ? 'reaction' : 'send',
        accountId: i.accountId,
        draftId: i.draftId,
        recipient: i.recipient,
        subject: i.subject,
        fromAddress: i.fromAddress,
        to: i.to ?? [],
        status: i.status,
        attempts: i.attempts,
        maxAttempts: i.maxAttempts,
        nextAttemptAt: typeof i.dueAt === 'string' ? i.dueAt : '',
        sendAt: typeof i.sendAt === 'string' ? i.sendAt : '',
        lastError: i.lastError,
        createdAt: typeof i.createdAt === 'string' ? i.createdAt : '',
        sentAt: typeof i.sentAt === 'string' ? i.sentAt : undefined,
      }))
      queueItems = [...sendRows, ...mmRows]
    } catch (err) {
      console.error('Failed to load outbox queue:', err)
      queueError = $_('viewer.failedToLoadMessages')
    } finally {
      queueLoading = false
    }
  }

  function queueStatusLabel(status: string): string {
    switch (status) {
      case 'queued': return $_('messageList.outboxStatusQueued')
      case 'pending': return $_('messageList.outboxStatusQueued')
      case 'sending': return $_('messageList.outboxStatusSending')
      case 'sent': return $_('messageList.outboxStatusSent')
      case 'failed': return $_('messageList.outboxStatusFailed')
      case 'cancelled': return $_('messageList.outboxStatusCancelled')
      default: return status
    }
  }

  function formatQueueTime(value: string): string {
    if (!value) return ''
    const d = new Date(value)
    return Number.isNaN(d.getTime()) ? value : d.toLocaleString()
  }

  async function sendUnsentNow() {
    queueActionBusy = 'send-unsent'
    try {
      const res = (await MailMerge_SendUnsent()) as MailMergeDispatchResult
      toasts.success(`Send unsent: ${res.sent} sent, ${res.failed} failed, ${res.remaining} still queued.`)
      await loadQueue()
    } catch (err) {
      console.error('Send unsent failed:', err)
      toasts.error((err as Error)?.message ?? String(err))
    } finally {
      queueActionBusy = ''
    }
  }

  async function cancelQueuedItem(id: string) {
    queueActionBusy = `cancel-${id}`
    try {
      await MailMerge_CancelQueuedItem(id)
      await loadQueue()
    } catch (err) {
      console.error('Cancel queued item failed:', err)
      toasts.error((err as Error)?.message ?? String(err))
    } finally {
      queueActionBusy = ''
    }
  }

  // Undo Send: cancel a normal send still in its undo window and
  // reopen the composer with the draft (the parent owns that flow).
  async function undoSendQueueItem(item: OutboxRow) {
    // Reactions have no draft to reopen — the App handler tolerates
    // an empty draftId, so no guard here.
    queueActionBusy = `undo-${item.id}`
    try {
      await onUndoSend?.(item.id, item.draftId ?? '', item.accountId)
      await loadQueue()
    } catch (err) {
      console.error('Undo queued send failed:', err)
      toasts.error((err as Error)?.message ?? String(err))
    } finally {
      queueActionBusy = ''
    }
  }

  // Retry a failed normal send now (fresh attempt budget, due at once).
  async function retrySendQueueItem(id: string) {
    queueActionBusy = `retry-${id}`
    try {
      await RetrySendQueueItem(id)
      await loadQueue()
    } catch (err) {
      console.error('Retry queued send failed:', err)
      toasts.error((err as Error)?.message ?? String(err))
    } finally {
      queueActionBusy = ''
    }
  }

  // Multi-select state
  let checkedThreadIds = $state<Set<string>>(new Set())
  let lastClickedIndex = $state<number | null>(null)

  // Pagination
  const PAGE_SIZE = 50
  let offset = $state(0)

  // Debounce timer for reloading after flag changes
  let reloadTimer: ReturnType<typeof setTimeout> | null = null

  // Debounce timer for coalescing sync event reloads (fixes event flooding with 3+ accounts)
  let syncReloadTimer: ReturnType<typeof setTimeout> | null = null

  // Deferred reload: when a dialog (e.g. folder picker) is open, defer the reload
  // so the component tree isn't destroyed mid-interaction
  let pendingReload = false

  // Buffer for flag changes that arrive while loadConversations() is in-flight.
  // On notification click, loadConversations (folder change) and MarkAsRead race —
  // the flagsChanged event may fire before the new conversations array is ready.
  let pendingFlagChanges: Array<{messageIds: string[], isRead: boolean}> = []

  // Search state
  let showSearch = $state(false)
  let searchQuery = $state('')
  let searchResults = $state<any[]>([])  // ConversationSearchResult from backend
  let searchTotalCount = $state(0)
  let searchOffset = $state(0)
  let isSearching = $state(false)
  let searchDebounceTimer: ReturnType<typeof setTimeout> | null = null

  // Check if we're in search mode with results (declared up here: the
  // Phase E showCategoryTabs derived below reads it, and $derived
  // declarations must precede their use).
  const isSearchMode = $derived(showSearch && searchQuery.trim().length > 0)

  // Filter state
  let filterMode = $state<string>('')  // '' | 'unread' | 'starred' | 'attachments'

  const filterLabel = $derived((() => {
    switch (filterMode) {
      case 'unread': return $_('messageList.filterUnread')
      case 'starred': return $_('messageList.filterStarred')
      case 'attachments': return $_('messageList.filterAttachments')
      default: return ''
    }
  })())

  const filterOptions = $derived([
    { value: '', label: $_('messageList.filterAll') },
    { value: 'unread', label: $_('messageList.filterUnread'), separator: true },
    { value: 'starred', label: $_('messageList.filterStarred') },
    { value: 'attachments', label: $_('messageList.filterAttachments') },
  ])

  // Category tabs (Phase E): All / Personal / Automated, in that order,
  // All first and default. A filter over the same folder, not new folders
  // — no mail moves, nothing to undo, All is always one click away. The
  // tab bar appears only in Inbox / Unified Inbox, below the Needs
  // attention bars. The chosen tab is session state; the default comes
  // from Settings → Categories (inbox_tab_default, default "all").
  let inboxTab = $state<string>('all') // 'all' | 'personal' | 'automated'
  let subFilter = $state<string>('') // '' | sub-category, inside Automated only
  let categoryCounts = $state<message.CategoryCounts | null>(null)

  // Tabs only exist where the classifier files mail: Inbox / Unified
  // Inbox, and never in search (search results span every folder).
  const showCategoryTabs = $derived(!isSearchMode && (isUnifiedInboxView || folderType === 'inbox'))

  // List-header scope naming (Phase E): the header always says where you
  // are — "All Inboxes · All accounts" in the unified view, "Work · Inbox"
  // for one account's folder.
  const scopeName = $derived(
    isUnifiedView
      ? `${folderName} · ${$_('messageList.allAccounts')}`
      : (() => {
          const acc = accountStore.accounts.find((a) => a.account.id === accountId)
          return acc ? `${acc.account.name} · ${folderName}` : folderName
        })()
  )

  const categoryTabs = $derived([
    { value: 'all', label: $_('messageList.categoryAll'), count: categoryCounts?.total },
    { value: 'personal', label: $_('messageList.categoryPersonal'), count: categoryCounts?.personal },
    { value: 'automated', label: $_('messageList.categoryAutomated'), count: categoryCounts?.automated },
  ])

  // Second-level filter inside the Automated tab only, in the plan's
  // order. Each chip shows its count; the row wraps instead of scrolling
  // sideways (flex-wrap below), scrolling only if the window is genuinely
  // narrow.
  const subCategoryOrder = ['Transactions', 'Orders', 'Shipping', 'Travel', 'Social', 'Promotions', 'Updates']
  const subCategoryLabels: Record<string, string> = $derived({
    Transactions: $_('messageList.subTransactions'),
    Orders: $_('messageList.subOrders'),
    Shipping: $_('messageList.subShipping'),
    Travel: $_('messageList.subTravel'),
    Social: $_('messageList.subSocial'),
    Promotions: $_('messageList.subPromotions'),
    Updates: $_('messageList.subUpdates'),
  })
  const subFilterOptions = $derived([
    { value: '', label: $_('messageList.subAll'), count: categoryCounts?.automated },
    ...subCategoryOrder.map((sub) => ({
      value: sub,
      label: subCategoryLabels[sub] ?? sub,
      count: categoryCounts?.subCategories?.[sub] ?? 0,
    })),
  ])

  // The category/sub-category actually sent to the backend: '' means "no
  // restriction" (All tab / all automated). A sub-category only applies
  // inside the Automated tab. Forced to '' where the tabs don't exist, so
  // a stale Personal tab never filters Sent/Spam/etc.
  const listCategory = $derived(showCategoryTabs && inboxTab !== 'all' ? inboxTab : '')
  const listSubCategory = $derived(showCategoryTabs && inboxTab === 'automated' ? subFilter : '')

  async function refreshCategoryCounts() {
    if (!showCategoryTabs) return
    try {
      categoryCounts = await GetCategoryCounts(isUnifiedInboxView ? '' : folderId!, isUnifiedInboxView)
    } catch (err) {
      console.error('Failed to load category counts:', err)
    }
  }

  function setCategoryTab(tab: string) {
    if (tab === inboxTab) return
    inboxTab = tab
    subFilter = ''
    offset = 0
    loadConversations()
  }

  function setSubFilter(sub: string) {
    if (sub === subFilter) return
    subFilter = sub
    offset = 0
    loadConversations()
  }

  // Server search state
  let serverSearchMode = $state(false)
  let serverSearchResults = $state<any[]>([])
  let serverSearchCount = $state(0)
  let serverSearchTotalCount = $state(0)  // Total matching UIDs on server (may exceed serverSearchCount when limited)
  let isServerSearching = $state(false)
  let lastServerQuery = $state('')
  const SERVER_SEARCH_LIMIT = 200

  // FTS indexing state
  let indexProgress = $state(0)
  let indexComplete = $state(true)
  let isIndexing = $state(false)
  let searchInputRef = $state<HTMLInputElement | null>(null)

  // Check if a synced folder belongs to the current unified view, by
  // looking its type up in the account store. Starred flags live in
  // messages across folders, so a Starred refresh follows any of the
  // five folder-type folders.
  function isUnifiedSyncFolder(acctId: string, fldId: string): boolean {
    const acct = accountStore.accounts.find((a) => a.account.id === acctId)
    if (!acct) return false
    const findType = (trees: { folder?: { id: string; type: string }; children?: typeof trees }[]): string | null => {
      for (const tree of trees) {
        if (tree.folder?.id === fldId) return tree.folder.type
        if (tree.children) {
          const found = findType(tree.children)
          if (found) return found
        }
      }
      return null
    }
    const type = findType(acct.folders as never)
    if (!type) return false
    if (folderId === 'starred') return ['inbox', 'sent', 'drafts', 'spam', 'trash'].includes(type)
    return type === folderId
  }

  // Schedule a debounced reload — coalesces rapid sync events from multiple accounts
  // into a single loadConversations() call after they settle (300ms).
  // Defers if a dialog guard is active (e.g. folder picker open).
  function scheduleReload() {
    if (isDialogGuardActive()) {
      pendingReload = true
      return
    }
    if (syncReloadTimer) clearTimeout(syncReloadTimer)
    syncReloadTimer = setTimeout(() => {
      syncReloadTimer = null
      if (loading) {
        pendingReload = true
        return
      }
      // Preserve the loaded window + scroll position across the sync reload
      // (#348) — same pattern as handleActionComplete. Without this, a sync
      // collapses "Load more" pagination back to the first page and jumps
      // the view to the top.
      const scrollTop = listContainerRef?.scrollTop ?? 0
      const totalLoaded = Math.max(conversations.length, PAGE_SIZE)
      offset = 0
      loadConversations(totalLoaded).then(() => {
        if (listContainerRef) {
          requestAnimationFrame(() => {
            listContainerRef!.scrollTop = scrollTop
          })
        }
      })
    }, 300)
  }

  // Listen for folder sync events from backend
  onMount(() => {
    EventsOn('folder:synced', (data: { accountId: string; folderId: string }) => {
      // Reload if this is the current folder, or a folder of the current
      // unified type synced
      if ((isUnifiedView && isUnifiedSyncFolder(data.accountId, data.folderId)) || (!isUnifiedView && accountId && folderId && data.accountId === accountId && data.folderId === folderId)) {
        scheduleReload()
      }
    })

    // Listen for messages:updated events (e.g., from IDLE push notifications)
    EventsOn('messages:updated', (data: { accountId: string; folderId: string }) => {
      // Reload if this is the current folder, or a folder of the current
      // unified type updated
      if ((isUnifiedView && isUnifiedSyncFolder(data.accountId, data.folderId)) || (!isUnifiedView && accountId && folderId && data.accountId === accountId && data.folderId === folderId)) {
        scheduleReload()
      }
    })

    // Listen for message read-state changes. Star changes ride their own
    // `messages:starredChanged` event and don't affect unread counts here.
    EventsOn('messages:readChanged', (data: { messageIds: string[], isRead: boolean }) => {
      // Update conversations locally instead of reloading from DB
      let anyUpdated = false
      for (const c of conversations) {
        const affectedCount = (c.messageIds || []).filter(id => data.messageIds.includes(id)).length
        if (affectedCount > 0) {
          anyUpdated = true
          const delta = data.isRead ? -affectedCount : affectedCount
          c.unreadCount = Math.max(0, (c.unreadCount || 0) + delta)
        }
      }
      if (anyUpdated) {
        conversations = conversations
        return
      }
      // loadConversations() is in-flight — the new array isn't ready yet.
      // Buffer this change so we can apply it after the load completes.
      if (loading) {
        pendingFlagChanges.push({ messageIds: data.messageIds, isRead: data.isRead })
      }
    })

    // Listen for FTS indexing progress
    EventsOn('fts:progress', (data: { folderId: string; indexed: number; total: number; percentage: number }) => {
      if (folderId && data.folderId === folderId) {
        indexProgress = data.percentage
        indexComplete = false
        isIndexing = true
      }
    })

    // Listen for FTS indexing completion
    EventsOn('fts:complete', (data: { folderId: string }) => {
      if (folderId && data.folderId === folderId) {
        indexComplete = true
        isIndexing = false
        indexProgress = 100
      }
    })

    // Listen for FTS indexing status changes
    EventsOn('fts:indexing', (data: { status: string }) => {
      switch (data.status) {
        case 'completed':
          indexComplete = true
          isIndexing = false
          break
        case 'started':
          isIndexing = true
          break
      }
    })

    // Sidebar "Needs attention" shortcut: scroll the list to the bars at
    // the top (the bars are above the list, so top of list = bars).
    EventsOn('attention:scrollToTop', () => {
      if (listContainerRef) listContainerRef.scrollTop = 0
    })

    // Outbox: reload the queue when the Mail Merge runner reports a change.
    EventsOn('mailmerge:queue-updated', () => {
      if (isOutboxView) void loadQueue()
    })

    // Outbox: reload when the Undo Send worker reports a change
    // (queued, sent, failed, undone).
    EventsOn('sendqueue:updated', () => {
      if (isOutboxView) void loadQueue()
    })

    // Category tab default (Phase E): the user's Settings → Categories
    // pick, "all" unless they chose otherwise. If it differs from the
    // "all" the first load already used, reload under the right tab.
    GetInboxTabDefault()
      .then((tab) => {
        if (tab && tab !== inboxTab) {
          inboxTab = tab
          offset = 0
          loadConversations()
        }
      })
      .catch((err) => console.error('Failed to load default inbox tab:', err))

    // A sender override changed (context menu or Settings): re-file the
    // list under the new categories.
    EventsOn('categories:changed', () => {
      offset = 0
      loadConversations()
    })

    // Check initial FTS index status for current folder
    checkFTSIndexStatus()

    // Flush deferred reloads once dialogs close
    dialogGuardInterval = setInterval(() => {
      if (pendingReload && !isDialogGuardActive()) {
        pendingReload = false
        scheduleReload()
      }
    }, 500)
  })

  let dialogGuardInterval: ReturnType<typeof setInterval> | null = null

  onDestroy(() => {
    EventsOff('folder:synced')
    EventsOff('messages:updated')
    EventsOff('messages:readChanged')
    EventsOff('categories:changed')
    EventsOff('mailmerge:queue-updated')
    EventsOff('sendqueue:updated')
    EventsOff('fts:progress')
    EventsOff('fts:complete')
    EventsOff('fts:indexing')
    if (reloadTimer) clearTimeout(reloadTimer)
    if (syncReloadTimer) clearTimeout(syncReloadTimer)
    if (searchDebounceTimer) clearTimeout(searchDebounceTimer)
    if (dialogGuardInterval) clearInterval(dialogGuardInterval)
  })

  // Check FTS index status for current folder (single-folder views only —
  // a unified view has no single folder ID to check).
  async function checkFTSIndexStatus() {
    if (!folderId || isUnifiedView) return
    try {
      const status = await GetFTSIndexStatus(folderId)
      if (status) {
        indexComplete = status.isComplete
        if (status.totalCount > 0) {
          indexProgress = Math.round((status.indexedCount / status.totalCount) * 100)
        }
      }
      isIndexing = await IsFTSIndexing()
    } catch (err) {
      console.error('Failed to check FTS index status:', err)
    }
  }

  // Track previous folder to detect actual changes
  let prevAccountId: string | null = null
  let prevFolderId: string | null = null

  // Remember the departing folder's pagination + scroll for the session so
  // returning to it restores the view (follow-up to #348).
  function rememberListState() {
    if (prevAccountId === null || prevFolderId === null) return
    sessionListState.set(`${prevAccountId}:${prevFolderId}`, {
      loadedCount: conversations.length,
      scrollTop: listContainerRef?.scrollTop ?? 0,
      selectedThreadId,
    })
  }

  // Clear selection and search when folder changes
  $effect(() => {
    const currentAccount = isUnifiedView ? 'unified' : accountId
    const currentFolder = folderId

    if (!isUnifiedView && (!accountId || !folderId)) {
      rememberListState()
      prevAccountId = null
      prevFolderId = null
      conversations = []
      totalCount = 0
      checkedThreadIds = new Set()
      return
    }

    // Only reset and reload if folder actually changed
    if (currentAccount === prevAccountId && currentFolder === prevFolderId) return

    rememberListState()
    prevAccountId = currentAccount
    prevFolderId = currentFolder
    loadGeneration++ // Invalidate any in-flight loads from the previous folder (#200)
    offset = 0
    checkedThreadIds = new Set()
    lastClickedIndex = null
    // Clear search state when folder changes
    showSearch = false
    searchQuery = ''
    searchResults = []
    searchTotalCount = 0
    searchOffset = 0
    serverSearchMode = false
    serverSearchResults = []
    serverSearchCount = 0
    serverSearchTotalCount = 0
    lastServerQuery = ''
    // Restore this folder's session state: reload the previously-paginated
    // window and put the scroll back where the user left it.
    const remembered = sessionListState.get(`${currentAccount}:${currentFolder}`)
    const restoreCount = remembered && remembered.loadedCount > PAGE_SIZE ? remembered.loadedCount : undefined
    loadConversations(restoreCount).then(() => {
      // Guard against rapid folder switches: only restore if we're still on
      // the folder this load was for.
      if (!remembered || prevAccountId !== currentAccount || prevFolderId !== currentFolder) {
        return
      }
      // Restore the selection (keyboard-nav anchor) so j/k continue from
      // where the user left off instead of the auto-selected first row —
      // only if the thread still exists in the reloaded window.
      // An external selection (notification click) wins over the restore.
      if (
        !pendingSelectThreadId &&
        remembered.selectedThreadId &&
        conversations.some((c) => c.threadId === remembered.selectedThreadId)
      ) {
        selectedThreadId = remembered.selectedThreadId
      }
      if (remembered.scrollTop > 0 && listContainerRef) {
        requestAnimationFrame(() => {
          listContainerRef!.scrollTop = remembered.scrollTop
        })
      }
    })
    checkFTSIndexStatus()
  })

  // Compute selected message IDs from all checked conversations (for multi-select context menu)
  // Check both conversations and searchResults since selections can span both
  // Use Set to deduplicate in case same conversation appears in both arrays
  const selectedMessageIds = $derived(
    [...new Set(
      [...conversations, ...searchResults]
        .filter((c) => checkedThreadIds.has(c.threadId))
        .flatMap((c: any) => c.messageIds || c.messages?.map((m: any) => m.id) || [])
    )]
  )

  // Aggregated star/read state for multi-select context menu
  // Show "Star" if any selected is unstarred, show "Mark as Read" if any selected is unread
  const selectedHasUnstarred = $derived(
    [...conversations, ...searchResults]
      .filter((c) => checkedThreadIds.has(c.threadId))
      .some((c: any) => !c.isStarred)
  )
  const selectedHasUnread = $derived(
    [...conversations, ...searchResults]
      .filter((c) => checkedThreadIds.has(c.threadId))
      .some((c: any) => (c.unreadCount || 0) > 0)
  )

  // Read/unread counts for the single Mark read/unread toggle button in
  // the selection bar. A conversation counts as read when it has no unread
  // messages. Majority read -> the button reads "Mark unread"; majority
  // unread, or an exact tie, -> it reads "Mark read". Derived, so the
  // label follows the selection and flips right after the action (the
  // readChanged listener updates unreadCount in place).
  const selectedReadCount = $derived(
    [...conversations, ...searchResults]
      .filter((c) => checkedThreadIds.has(c.threadId))
      .reduce((n: number, c: any) => n + ((c.unreadCount || 0) === 0 ? 1 : 0), 0)
  )
  const selectedUnreadCount = $derived(
    [...conversations, ...searchResults]
      .filter((c) => checkedThreadIds.has(c.threadId))
      .reduce((n: number, c: any) => n + ((c.unreadCount || 0) > 0 ? 1 : 0), 0)
  )
  const selectedMajorityRead = $derived(selectedReadCount > selectedUnreadCount)

  // Clear multi-select (called when right-clicking on unchecked row)
  function clearSelection() {
    checkedThreadIds = new Set()
    lastClickedIndex = null
  }

  async function loadConversations(customLimit?: number) {
    // Outbox is the local send queue, not a conversation list.
    if (isOutboxView) {
      await loadQueue()
      return
    }

    // For unified view, we don't need accountId/folderId
    if (!isUnifiedView && (!accountId || !folderId)) return

    // Prevent concurrent loads — defer instead of dropping
    if (loading) {
      pendingReload = true
      return
    }

    loading = true
    error = null

    // Capture offset and generation at start — both may change during async operations
    const currentOffset = offset
    const limit = customLimit ?? PAGE_SIZE
    const generation = loadGeneration

    try {
      const [convList, count] = isUnifiedView
        ? await Promise.all([
          GetUnifiedFolderConversations(folderId!, currentOffset, limit, getMessageListSortOrder(), filterMode, listCategory, listSubCategory),
          GetUnifiedFolderCount(folderId!, filterMode, listCategory, listSubCategory),
        ])
        : await Promise.all([
          GetConversations(accountId!, folderId!, currentOffset, limit, getMessageListSortOrder(), filterMode, listCategory, listSubCategory),
          GetConversationCount(accountId!, folderId!, filterMode, listCategory, listSubCategory),
        ])

      // Discard stale result — folder was switched while this load was in-flight (#200)
      if (generation !== loadGeneration) return

      if (currentOffset !== 0) {
        conversations = [...conversations, ...(convList || [])]
        totalCount = count
        return
      }

      conversations = convList || []

      // Apply any flag changes that arrived while we were loading.
      // This fixes the race where MarkAsRead fires before the new array is ready.
      if (pendingFlagChanges.length > 0) {
        for (const change of pendingFlagChanges) {
          for (const c of conversations) {
            const affectedCount = (c.messageIds || []).filter(
              (id: string) => change.messageIds.includes(id)
            ).length
            if (affectedCount > 0) {
              const delta = change.isRead ? -affectedCount : affectedCount
              c.unreadCount = Math.max(0, (c.unreadCount || 0) + delta)
            }
          }
        }
        pendingFlagChanges = []
      }

      // Check if we switched to a different folder
      const folderChanged = lastLoadedFolderId !== folderId
      lastLoadedFolderId = folderId

      // Auto-select first message on folder navigation or initial load
      if (conversations.length === 0) {
        selectedThreadId = null
        totalCount = count
        return
      }

      if (pendingSelectThreadId && conversations.some(c => c.threadId === pendingSelectThreadId)) {
        selectedThreadId = pendingSelectThreadId
        pendingSelectThreadId = null
        scrollToIndex(conversations.findIndex(c => c.threadId === selectedThreadId))
      }
      if (!pendingSelectThreadId && (folderChanged || !selectedThreadId)) {
        selectedThreadId = conversations[0].threadId
      }
      totalCount = count
    } catch (err) {
      // Discard stale error — folder was switched while this load was in-flight (#200)
      if (generation !== loadGeneration) return
      console.error('Failed to load messages:', err)
      error = $_('viewer.failedToLoadMessages')
    } finally {
      loading = false
      // New mail may have arrived with the sync — refresh the bars too.
      attentionStripRef?.refresh()
      // Tab/chip counts follow the scope, not the page: refresh them on
      // every list load so a re-filed message moves its count at once.
      if (generation === loadGeneration) void refreshCategoryCounts()
      // Flush any deferred reload (from sync event during load or dialog guard).
      // Only the latest-generation load should drive the flush — otherwise a
      // stale completion could fire scheduleReload redundantly.
      if (generation === loadGeneration && pendingReload && !isDialogGuardActive()) {
        pendingReload = false
        scheduleReload()
      }
    }
  }

  export async function syncFolder() {
    error = null

    // Outbox has nothing to sync — refresh just reloads the queue.
    if (isOutboxView) {
      await loadQueue()
      return
    }

    // Unified view: sync EVERY account's folder of the selected type.
    // SyncFolder is per account+folder and concurrent-safe, so fire them
    // all at once; the account store's per-folder progress keeps the
    // spinner correct (unifiedSyncKeys covers the gap before the first
    // progress event).
    if (isUnifiedView) {
      const targets = getUnifiedSyncTargets()
      if (targets.length === 0) return

      const keys = targets.map((f) => `${f.accountId}:${f.folderId}`)
      unifiedSyncKeys = [...unifiedSyncKeys, ...keys]
      try {
        const scrollTop = listContainerRef?.scrollTop ?? 0
        const totalLoaded = Math.max(conversations.length, PAGE_SIZE)
        const results = await Promise.allSettled(
          targets.map((f) => SyncFolder(f.accountId, f.folderId))
        )
        offset = 0
        await loadConversations(totalLoaded)
        if (listContainerRef) {
          requestAnimationFrame(() => {
            listContainerRef!.scrollTop = scrollTop
          })
        }
        // A failed inbox sync must NOT set the list-level error: the
        // template hides the whole list while `error` is set, so one
        // unreachable server would wipe the loaded messages from view.
        // Per-account failures are already surfaced in the sidebar via
        // folder:syncError ("Sync error. Try again...").
        const failed = results.filter((r) => r.status === 'rejected')
        if (failed.length > 0) {
          console.error(`Failed to sync ${failed.length} of ${targets.length} inboxes`)
        }
      } catch (err) {
        console.error('Failed to sync inboxes:', err)
        error = $_('viewer.failedToLoadMessages')
      } finally {
        unifiedSyncKeys = unifiedSyncKeys.filter((k) => !keys.includes(k))
      }
      return
    }

    if (!accountId || !folderId) return

    try {
      // SyncFolder returns after headers sync, but body fetch continues in background
      // The account store tracks sync:progress and folder:synced events to manage syncing state
      // Preserve the loaded window + scroll position (#348) — manual sync must
      // not collapse "Load more" pagination.
      const scrollTop = listContainerRef?.scrollTop ?? 0
      const totalLoaded = Math.max(conversations.length, PAGE_SIZE)
      await SyncFolder(accountId, folderId)
      offset = 0
      await loadConversations(totalLoaded)
      if (listContainerRef) {
        requestAnimationFrame(() => {
          listContainerRef!.scrollTop = scrollTop
        })
      }
    } catch (err) {
      console.error('Failed to sync folder:', err)
      error = $_('viewer.failedToLoadMessages')
    }
    // No need to manage syncing state - account store handles it via events
  }

  // Cancel folder sync
  export async function cancelFolderSync() {
    // Unified view: cancel every folder sync of the selected type started
    // by this refresh (still in-flight keys) plus any folder of the type
    // currently reporting progress (its body-fetch phase, started by this
    // refresh, outlives the SyncFolder call that began it).
    if (isUnifiedView) {
      const targets = getUnifiedSyncTargets().filter(
        (f) =>
          unifiedSyncKeys.includes(`${f.accountId}:${f.folderId}`) ||
          accountStore.syncProgress[f.accountId]?.[f.folderId] !== undefined
      )
      const keys = targets.map((f) => `${f.accountId}:${f.folderId}`)
      unifiedSyncKeys = unifiedSyncKeys.filter((k) => !keys.includes(k))
      try {
        await Promise.allSettled(
          targets.map((f) => CancelFolderSync(f.accountId, f.folderId))
        )
      } catch (err) {
        console.error('Failed to cancel inbox syncs:', err)
      }
      return
    }

    if (!accountId || !folderId) return

    try {
      await CancelFolderSync(accountId, folderId)
    } catch (err) {
      console.error('Failed to cancel folder sync:', err)
    }
  }

  // Toggle folder sync (start if not running, cancel if running) - for keyboard shortcut and UI
  export async function toggleFolderSync() {
    if (syncing) {
      await cancelFolderSync()
      return
    }
    await syncFolder()
  }

  // Force re-sync folder (clears bodies & attachments, then re-fetches)
  async function forceSyncFolder() {
    if (isUnifiedView || !accountId || !folderId) return

    error = null

    try {
      await ForceSyncFolder(accountId, folderId)
      offset = 0
      await loadConversations()
    } catch (err) {
      console.error('Failed to force re-sync folder:', err)
      error = $_('viewer.failedToLoadMessages')
    }
  }

  // Handle search input with debounce
  function handleSearchInput() {
    if (searchDebounceTimer) clearTimeout(searchDebounceTimer)

    if (!searchQuery.trim()) {
      // Clear search immediately if query is empty
      searchResults = []
      searchTotalCount = 0
      serverSearchResults = []
      serverSearchCount = 0
      serverSearchTotalCount = 0
      serverSearchMode = false
      return
    }

    // In server mode, don't auto-search locally — user will press Shift+Enter
    if (serverSearchMode) return

    searchDebounceTimer = setTimeout(() => {
      performSearch()
    }, 300)
  }

  // Perform the actual search
  async function performSearch() {
    const query = searchQuery.trim()
    if (!query) {
      searchResults = []
      searchTotalCount = 0
      searchOffset = 0
      return
    }

    // Don't start a new search if one is already in progress
    if (isSearching) return

    isSearching = true
    error = null
    searchOffset = 0  // Reset offset for new search

    try {
      let results: any[] = []
      let count = 0

      if (isUnifiedView) {
        ;[results, count] = await Promise.all([
          SearchUnifiedFolder(folderId!, query, 0, PAGE_SIZE, filterMode),
          GetSearchCountUnifiedFolder(folderId!, query, filterMode),
        ])
      } else if (accountId && folderId) {
        ;[results, count] = await Promise.all([
          SearchConversations(accountId, folderId, query, 0, PAGE_SIZE, filterMode),
          GetSearchCount(accountId, folderId, query, filterMode),
        ])
      }

      searchResults = results || []
      searchTotalCount = count
      // Auto-select first search result for keyboard navigation
      if (searchResults.length > 0) {
        selectedThreadId = searchResults[0].threadId
      }
    } catch (err) {
      console.error('Search failed:', err)
      error = $_('viewer.failedToLoadMessages')
    } finally {
      isSearching = false
    }
  }

  // Load more search results (pagination)
  async function loadMoreSearchResults() {
    const query = searchQuery.trim()
    if (!query || isSearching) return

    // Cancel any pending search debounce to prevent race conditions
    if (searchDebounceTimer) {
      clearTimeout(searchDebounceTimer)
      searchDebounceTimer = null
    }

    isSearching = true
    const newOffset = searchOffset + PAGE_SIZE

    try {
      let results: any[] = []
      if (isUnifiedView) {
        results = await SearchUnifiedFolder(folderId!, query, newOffset, PAGE_SIZE, filterMode)
      } else if (accountId && folderId) {
        results = await SearchConversations(accountId, folderId, query, newOffset, PAGE_SIZE, filterMode)
      }

      if (results && results.length > 0) {
        searchResults = [...searchResults, ...results]
        searchOffset = newOffset
      }
    } catch (err) {
      console.error('Load more search results failed:', err)
      error = $_('viewer.failedToLoadMessages')
    } finally {
      isSearching = false
    }
  }

  // Clear search and return to normal view
  function clearSearch() {
    searchQuery = ''
    searchResults = []
    searchTotalCount = 0
    searchOffset = 0
    showSearch = false
    serverSearchMode = false
    serverSearchResults = []
    serverSearchCount = 0
    serverSearchTotalCount = 0
    lastServerQuery = ''
    isServerSearching = false
    if (searchDebounceTimer) clearTimeout(searchDebounceTimer)
  }

  // Handle keyboard events in search input
  function handleSearchKeydown(event: KeyboardEvent) {
    switch (true) {
      case event.key === 'Enter' && event.shiftKey:
        event.preventDefault()
        if (isUnifiedView) return
        handleShiftEnter()
        break
      case event.key === 'Enter':
        // Move focus from search input to message list so user can navigate with arrow keys
        event.preventDefault()
        searchInputRef?.blur()
        listContainerRef?.focus()
        break
    }
  }

  // Smart toggle/re-search for server search (Shift+Enter)
  function handleShiftEnter() {
    const query = searchQuery.trim()
    if (!query) return

    if (!serverSearchMode) {
      // Local → server
      serverSearchMode = true
      lastServerQuery = query
      performServerSearch()
      return
    }

    if (query !== lastServerQuery) {
      // Server mode, query changed → re-search
      lastServerQuery = query
      performServerSearch()
      return
    }

    // Server mode, same query → toggle back to local
    serverSearchMode = false
  }

  // Perform IMAP server-side search. limit=0 means no limit (show all).
  async function performServerSearch(limit: number = SERVER_SEARCH_LIMIT) {
    const query = searchQuery.trim()
    if (!query || !accountId || !folderId || isUnifiedView) return

    isServerSearching = true
    error = null
    try {
      const response = await IMAPSearchFolder(accountId, folderId, query, limit)
      const items = (response?.results || []).map(adaptServerResult)
      serverSearchResults = items
      serverSearchCount = items.length
      serverSearchTotalCount = response?.totalCount ?? items.length
      if (items.length > 0) {
        selectedThreadId = items[0].threadId
      }
    } catch (err) {
      console.error('Server search failed:', err)
      error = $_('viewer.failedToLoadMessages')
    } finally {
      isServerSearching = false
    }
  }

  // Map IMAPSearchResult to ConversationRow-compatible shape
  function adaptServerResult(r: any): any {
    return {
      threadId: r.messageId || `server-uid-${r.uid}`,
      subject: r.subject,
      snippet: r.isLocal ? r.snippet : '',
      messageCount: 1,
      unreadCount: r.isRead ? 0 : 1,
      hasAttachments: r.hasAttachments,
      isStarred: r.isStarred,
      latestDate: r.date,
      participants: [{ name: r.fromName, email: r.fromEmail }],
      messageIds: r.messageId ? [r.messageId] : [],
      accountId: r.accountId,
      folderId: r.folderId,
      _isLocal: r.isLocal,
      _uid: r.uid,
    }
  }

  // Toggle search visibility
  function toggleSearch() {
    showSearch = !showSearch
    if (showSearch) {
      // Focus input after it appears
      setTimeout(() => searchInputRef?.focus(), 50)
      return
    }
    clearSearch()
  }

  // Active list - either conversations, local search results, or server search results
  const activeList = $derived(
    isSearchMode
      ? (serverSearchMode ? serverSearchResults : searchResults)
      : conversations
  )
  const activeCount = $derived(
    isSearchMode
      ? (serverSearchMode ? serverSearchTotalCount : searchTotalCount)
      : totalCount
  )

  // Opt-in contact photos: batch-prefetch the visible rows' avatar emails in one
  // Wails call (never per-row). Debounced (~150ms, cancelled via effect cleanup)
  // so fast folder switching only fetches the folder you land on. participants[0]
  // is already folder-aware (recipient in Sent/Drafts, sender elsewhere), and the
  // cache serves repeat contacts / return visits instantly. Feature off ⇒ no calls.
  $effect(() => {
    if (!getShowMessageListProfilePics()) return
    const list = activeList
    const t = setTimeout(() => {
      const emails: string[] = []
      for (const c of list) {
        const email = c?.participants?.[0]?.email
        if (email) emails.push(email)
      }
      void contactPhotos.ensure(emails)
    }, 150)
    return () => clearTimeout(t)
  })

  function toggleSetEntry(set: Set<string>, key: string) {
    if (set.has(key)) {
      set.delete(key)
      return
    }
    set.add(key)
  }

  function selectConversation(threadId: string, index: number, event?: MouseEvent) {
    // Shift+click: range select (preserve anchor). With no anchor yet (the
    // first click in this folder is a Shift+click), the clicked row becomes
    // the anchor so the next Shift+click ranges from it.
    if (event?.shiftKey) {
      if (lastClickedIndex === null) lastClickedIndex = index
      const start = Math.min(lastClickedIndex, index)
      const end = Math.max(lastClickedIndex, index)
      const newChecked = new Set(checkedThreadIds)
      for (let i = start; i <= end; i++) {
        newChecked.add(activeList[i].threadId)
      }
      checkedThreadIds = newChecked
      return
    }

    // Update anchor for non-shift clicks
    lastClickedIndex = index

    // Ctrl/Cmd+click: toggle single checkbox without changing selection
    if (event?.ctrlKey || event?.metaKey) {
      const newChecked = new Set(checkedThreadIds)
      toggleSetEntry(newChecked, threadId)
      checkedThreadIds = newChecked
      return
    }

    // Normal click - select for viewing, clear checks
    checkedThreadIds = new Set()
    selectedThreadId = threadId

    // Select-only mode (preview pane off): a single click only highlights
    // the row. Opening is a double-click / Enter via openConversation.
    if (selectOnly) return

    // For unified view or search, use real folderId and accountId from conversation data
    const conversation = activeList[index] as any
    const realFolderId = (isUnifiedView || isSearchMode) && conversation.folderId ? conversation.folderId : folderId!
    const realAccountId = (isUnifiedView || isSearchMode) && conversation.accountId ? conversation.accountId : accountId!

    // If this is a non-local server result, fetch it first
    if (serverSearchMode && conversation._isLocal === false && conversation._uid) {
      fetchAndSelectServerResult(conversation, realFolderId, realAccountId)
      return
    }
    onConversationSelect?.(threadId, realFolderId, realAccountId)
  }

  // Explicitly open a conversation (double-click / Enter in select-only
  // mode). Resolves the real folder/account the same way selectConversation
  // does and hands the subject to the parent for the tab label.
  function openConversation(threadId: string, index: number) {
    const conv = activeList[index] as any
    if (!conv) return
    const realFolderId = (isUnifiedView || isSearchMode) && conv.folderId ? conv.folderId : folderId!
    const realAccountId = (isUnifiedView || isSearchMode) && conv.accountId ? conv.accountId : accountId!
    const subject = (conv.subject as string) || ''
    if (serverSearchMode && conv._isLocal === false && conv._uid) {
      fetchAndOpenServerResult(conv, realFolderId, realAccountId, subject)
      return
    }
    selectedThreadId = threadId
    onConversationOpen?.(threadId, realFolderId, realAccountId, subject)
  }

  // Open the message behind an attention bar in the active view mode
  // (select in classic mode, explicit open in select-only/tab mode).
  function openAttentionItem(item: app.AttentionItem) {
    selectedThreadId = item.threadId
    if (selectOnly) {
      onConversationOpen?.(item.threadId, item.folderId, item.accountId, item.subject)
    } else {
      onConversationSelect?.(item.threadId, item.folderId, item.accountId)
    }
  }

  // Subject lookup for a thread in the currently loaded list (tab labels).
  export function getConversationSubject(threadId: string): string {
    const conv = activeList.find(c => c.threadId === threadId) as any
    return (conv?.subject as string) || ''
  }

  // Fetch a non-local server result, save locally, update the result, then select
  async function fetchAndSelectServerResult(conversation: any, realFolderId: string, realAccountId: string) {
    try {
      const msg = await FetchServerMessage(realAccountId, realFolderId, conversation._uid)
      if (msg) {
        // Update the server result to be local
        const idx = serverSearchResults.findIndex(r => r._uid === conversation._uid)
        if (idx >= 0) {
          serverSearchResults[idx] = {
            ...serverSearchResults[idx],
            threadId: msg.threadId || msg.id,
            messageIds: [msg.id],
            snippet: msg.snippet || '',
            _isLocal: true,
            _uid: conversation._uid,
          }
          serverSearchResults = serverSearchResults
          selectedThreadId = serverSearchResults[idx].threadId
        }
        onConversationSelect?.(msg.threadId || msg.id, realFolderId, realAccountId)
      }
    } catch (err) {
      console.error('Failed to fetch server message:', err)
      error = $_('viewer.failedToLoadMessages')
    }
  }

  // Fetch a non-local server result, save locally, update the result, then open it
  async function fetchAndOpenServerResult(conversation: any, realFolderId: string, realAccountId: string, subject: string) {
    try {
      const msg = await FetchServerMessage(realAccountId, realFolderId, conversation._uid)
      if (msg) {
        const idx = serverSearchResults.findIndex(r => r._uid === conversation._uid)
        if (idx >= 0) {
          serverSearchResults[idx] = {
            ...serverSearchResults[idx],
            threadId: msg.threadId || msg.id,
            messageIds: [msg.id],
            snippet: msg.snippet || '',
            _isLocal: true,
            _uid: conversation._uid,
          }
          serverSearchResults = serverSearchResults
          selectedThreadId = serverSearchResults[idx].threadId
        }
        onConversationOpen?.(msg.threadId || msg.id, realFolderId, realAccountId, subject || msg.subject || '')
      }
    } catch (err) {
      console.error('Failed to fetch server message:', err)
      error = $_('viewer.failedToLoadMessages')
    }
  }

  function handleCheck(threadId: string, isChecked: boolean, index: number, event?: MouseEvent) {
    if (event?.shiftKey && lastClickedIndex !== null) {
      const start = Math.min(lastClickedIndex, index)
      const end = Math.max(lastClickedIndex, index)
      const newChecked = new Set(checkedThreadIds)
      for (let i = start; i <= end; i++) {
        newChecked.add(activeList[i].threadId)
      }
      checkedThreadIds = newChecked
      return
    }

    lastClickedIndex = index
    const newChecked = new Set(checkedThreadIds)
    if (isChecked) newChecked.add(threadId)
    if (!isChecked) newChecked.delete(threadId)
    checkedThreadIds = newChecked
  }

  export function handleActionComplete(autoSelectNext: boolean = false) {
    onRowActionComplete?.()
    // Get target index BEFORE reload (for auto-select after delete/archive/spam)
    // Uses earliest checked item's index so bulk delete doesn't overshoot
    const currentIndex = getEarliestCheckedIndex()
    const scrollTop = listContainerRef?.scrollTop ?? 0

    // If in search mode, refresh search results instead of conversations
    if (isSearchMode) {
      performSearch().then(() => {
        // Restore scroll position
        if (listContainerRef) {
          requestAnimationFrame(() => {
            listContainerRef!.scrollTop = scrollTop
          })
        }

        // Auto-select next message if requested
        if (autoSelectNext) {
          const isNarrow = getLayoutMode() === 'narrow'
          if (isNarrow) {
            hideViewer()
          }
          if (currentIndex >= 0 && searchResults.length > 0) {
            const newIndex = Math.min(currentIndex, searchResults.length - 1)
            const conv = searchResults[newIndex]
            if (conv) {
              if (isNarrow) {
                selectedThreadId = conv.threadId
              }
              if (!isNarrow) {
                selectConversation(conv.threadId, newIndex)
              }
            }
          }
        }
      })
      return
    }

    // Preserve loaded messages: reload all messages that were loaded
    // Use conversations.length to track actual loaded count (offset gets reset after first action)
    const totalLoaded = Math.max(conversations.length, PAGE_SIZE)
    offset = 0

    loadConversations(totalLoaded).then(() => {
      // Restore scroll position
      if (listContainerRef) {
        requestAnimationFrame(() => {
          listContainerRef!.scrollTop = scrollTop
        })
      }

      // Auto-select next message if requested (for delete/archive/spam actions)
      // After reload, the same index now points to what was the "next" message
      if (autoSelectNext) {
        const isNarrow = getLayoutMode() === 'narrow'
        if (isNarrow) {
          hideViewer()
        }
        if (currentIndex >= 0 && conversations.length > 0) {
          const newIndex = Math.min(currentIndex, conversations.length - 1)
          const conv = conversations[newIndex]
          if (conv) {
            if (isNarrow) {
              selectedThreadId = conv.threadId
            }
            if (!isNarrow) {
              selectConversation(conv.threadId, newIndex)
            }
          }
        }
      }

    })
  }

  // ---- Bulk-action header (Phase 1: selection bulk-action bar) ----
  // Same bindings, toasts and Undo as the context menu (MessageContextMenu).
  // After each action the list reloads via handleActionComplete() (scroll +
  // pagination preserved) and the checks are cleared. Search state is
  // untouched — the query/results survive the header swap.
  const isSpamFolder = $derived(folderType === 'spam')
  const isTrashFolder = $derived(folderType === 'trash')

  // Select-all checkbox: reflects only the LOADED page (activeList, 50 at
  // a time) — select-all never reaches beyond what is currently loaded.
  const selectionAllChecked = $derived(
    activeList.length > 0 && activeList.every((c) => checkedThreadIds.has(c.threadId))
  )
  const selectionPartial = $derived(checkedThreadIds.size > 0 && !selectionAllChecked)
  let selectAllCheckboxRef = $state<HTMLInputElement | null>(null)
  $effect(() => {
    if (selectAllCheckboxRef) selectAllCheckboxRef.indeterminate = selectionPartial
  })

  // Folder picker for bulk Move to (reuses the context menu's picker)
  let showBulkFolderPicker = $state(false)
  let bulkAccounts = $state<account.Account[]>([])
  let bulkAccountsLoaded = $state(false)
  let bulkAccountsLoading = $state(false)

  // Initial account for the picker: the account of the first checked row
  // (unified/search selections may span accounts); falls back to the
  // current folder's account, or '' (user picks from the dropdown).
  const bulkMoveInitialAccountId = $derived(
    (activeList.find((c: any) => checkedThreadIds.has(c.threadId)) as any)?.accountId
      || (accountId && accountId !== 'unified' ? accountId : '')
  )

  // Keep background reloads from dismissing the picker while it's open
  $effect(() => {
    if (showBulkFolderPicker) {
      dialogGuardOpen()
      return () => dialogGuardClose()
    }
  })

  async function loadBulkAccounts() {
    if (bulkAccountsLoaded || bulkAccountsLoading) return
    bulkAccountsLoading = true
    try {
      bulkAccounts = (await GetAccounts()) || []
      bulkAccountsLoaded = true
    } catch (err) {
      console.error('Failed to load accounts:', err)
    } finally {
      bulkAccountsLoading = false
    }
  }

  // Select-all covers only the LOADED page (activeList, PAGE_SIZE at a time).
  function handleBulkSelectAll() {
    if (selectionAllChecked) {
      clearChecked()
    } else {
      selectAll()
    }
  }

  // Single toggle for the selection bar: marks ALL selected messages the
  // opposite of the majority status. The selection is deliberately kept
  // after the action (unlike the other bulk actions) so the button flips
  // to the new majority label right away; the user dismisses the bar with
  // the × button or Esc.
  async function handleBulkMarkRead() {
    try {
      await MarkAsRead(selectedMessageIds)
      toasts.success($_('toast.markedAsRead'))
      handleActionComplete()
    } catch (err) {
      console.error('Bulk mark as read failed:', err)
      toasts.error($_('toast.failedToUpdateReadStatus'))
    }
  }

  async function handleBulkMarkUnread() {
    try {
      await MarkAsUnread(selectedMessageIds)
      toasts.success($_('toast.markedAsUnread'))
      handleActionComplete()
    } catch (err) {
      console.error('Bulk mark as unread failed:', err)
      toasts.error($_('toast.failedToUpdateReadStatus'))
    }
  }

  async function handleBulkArchive() {
    try {
      await Archive(selectedMessageIds)
      toasts.success($_('toast.archived'), [{ label: $_('common.undo'), onClick: handleUndo }])
      handleActionComplete(true)
      clearChecked()
    } catch (err) {
      console.error('Bulk archive failed:', err)
      toasts.error($_('toast.failedToArchive'))
    }
  }

  // Delete reuses requestDelete: Trash folder shows the permanent-delete
  // confirm dialog, otherwise the messages move to Trash.
  function handleBulkDelete() {
    requestDelete(selectedMessageIds)
  }

  async function handleBulkSpam() {
    try {
      if (isSpamFolder) {
        await MarkAsNotSpam(selectedMessageIds)
        toasts.success($_('toast.markedAsNotSpam'), [{ label: $_('common.undo'), onClick: handleUndo }])
        handleActionComplete(true)
        clearChecked()
        return
      }
      const movedToSpam = await MarkAsSpam(selectedMessageIds)
      const toastMsg = movedToSpam ? $_('toast.markedAsSpam') : $_('toast.deletedFromFolder')
      const actions = movedToSpam ? [{ label: $_('common.undo'), onClick: handleUndo }] : []
      toasts.success(toastMsg, actions)
      handleActionComplete(true)
      clearChecked()
    } catch (err) {
      console.error('Bulk spam toggle failed:', err)
      toasts.error($_(isSpamFolder ? 'toast.failedToMarkAsNotSpam' : 'toast.failedToMarkAsSpam'))
    }
  }

  async function handleBulkToggleStar() {
    try {
      if (selectedHasUnstarred) {
        await Star(selectedMessageIds)
        toasts.success($_('toast.starred'))
      } else {
        await Unstar(selectedMessageIds)
        toasts.success($_('toast.starRemoved'))
      }
      handleActionComplete()
      clearChecked()
    } catch (err) {
      console.error('Bulk star toggle failed:', err)
      toasts.error($_('toast.failedToUpdateStar'))
    }
  }

  function openBulkMoveTo() {
    loadBulkAccounts()
    showBulkFolderPicker = true
  }

  function handleBulkFolderSelected(destFolderId: string, folderName: string, _destAccountId: string) {
    showBulkFolderPicker = false
    handleBulkMoveTo(destFolderId, folderName)
  }

  async function handleBulkMoveTo(destFolderId: string, folderName: string) {
    try {
      await MoveToFolder(selectedMessageIds, destFolderId)
      toasts.success($_('toast.movedTo', { values: { folder: folderName } }), [{ label: $_('common.undo'), onClick: handleUndo }])
      handleActionComplete(true)
      clearChecked()
    } catch (err) {
      console.error('Bulk move failed:', err)
      toasts.error($_('toast.failedToMove'))
    }
  }

  // Toggle sort order and persist to backend
  async function toggleSortOrder() {
    const newOrder = getMessageListSortOrder() === 'newest' ? 'oldest' : 'newest'
    try {
      await SetMessageListSortOrder(newOrder)
      setMessageListSortOrder(newOrder)
      offset = 0
      loadConversations()
    } catch (err) {
      console.error('Failed to save sort order:', err)
    }
  }

  // Set filter mode and reload
  function setFilter(mode: string) {
    filterMode = mode
    offset = 0
    if (isSearchMode) {
      searchOffset = 0
      performSearch()
      return
    }
    loadConversations()
  }

  // Calculate total unread count
  const unreadCount = $derived(
    conversations.reduce((sum, c) => sum + (c.unreadCount || 0), 0)
  )

  // Reference to the list container for scrolling
  let listContainerRef = $state<HTMLDivElement | null>(null)

  // Reference to the "Load more" button for keyboard navigation
  let loadMoreButtonRef = $state<HTMLButtonElement | null>(null)

  // Get current selected index
  function getSelectedIndex(): number {
    if (!selectedThreadId) return -1
    return activeList.findIndex(c => c.threadId === selectedThreadId)
  }

  // Select previous message (exposed for keyboard navigation)
  // Just moves focus, doesn't clear checkboxes or open in viewer
  export function selectPrevious() {
    if (activeList.length === 0) return

    const currentIndex = getSelectedIndex()
    const newIndex = currentIndex <= 0 ? 0 : currentIndex - 1

    const conv = activeList[newIndex]
    if (conv) {
      selectedThreadId = conv.threadId
      // Keyboard focus moves the range anchor too, so a later Shift+click
      // or Shift+checkbox ranges from the keyboard-focused row, not from
      // a stale mouse click.
      lastClickedIndex = newIndex
      scrollToIndex(newIndex)
      // Blur any focused element so Enter key triggers openSelected() instead of the button
      ;(document.activeElement as HTMLElement)?.blur?.()
    }
  }

  // Select next message (exposed for keyboard navigation)
  // Just moves focus, doesn't clear checkboxes or open in viewer
  export function selectNext() {
    if (activeList.length === 0) return

    const currentIndex = getSelectedIndex()

    // If at last message and more are available, focus the "Load more" button
    if (currentIndex >= activeList.length - 1 && activeList.length < activeCount) {
      loadMoreButtonRef?.focus()
      return
    }

    const newIndex = currentIndex >= activeList.length - 1 ? activeList.length - 1 : currentIndex + 1

    const conv = activeList[newIndex]
    if (conv) {
      selectedThreadId = conv.threadId
      lastClickedIndex = newIndex
      scrollToIndex(newIndex)
      // Blur any focused element so Enter key triggers openSelected() instead of the button
      ;(document.activeElement as HTMLElement)?.blur?.()
    }
  }

  // Select + scroll to the first message (g)
  export function selectFirst() {
    if (activeList.length === 0) return
    selectedThreadId = activeList[0].threadId
    lastClickedIndex = 0
    scrollToIndex(0)
    // Blur any focused element so Enter key triggers openSelected() instead of the button
    ;(document.activeElement as HTMLElement)?.blur?.()
  }

  // Select + scroll to the last LOADED message (G) — does not paginate,
  // consistent with j/k stopping at the loaded window
  export function selectLast() {
    if (activeList.length === 0) return
    const last = activeList.length - 1
    selectedThreadId = activeList[last].threadId
    lastClickedIndex = last
    scrollToIndex(last)
    // Blur any focused element so Enter key triggers openSelected() instead of the button
    ;(document.activeElement as HTMLElement)?.blur?.()
  }

  // Open the currently selected conversation (exposed for keyboard navigation)
  export function openSelected() {
    if (!selectedThreadId) return

    const index = getSelectedIndex()
    if (index >= 0) {
      // Select-only mode: Enter opens via the open path (tab), not the
      // classic select path.
      if (selectOnly) {
        openConversation(selectedThreadId, index)
        return
      }
      const conv = activeList[index] as any
      const realFolderId = (isUnifiedView || isSearchMode) && conv.folderId ? conv.folderId : folderId!
      const realAccountId = (isUnifiedView || isSearchMode) && conv.accountId ? conv.accountId : accountId!
      onConversationSelect?.(selectedThreadId, realFolderId, realAccountId)
    }
  }

  // Select a specific thread by ID (exposed for notification clicks). If the
  // thread is not in the list yet (folder still loading), the request is kept
  // and applied when the load completes.
  export function selectThread(threadId: string) {
    const index = activeList.findIndex(c => c.threadId === threadId)
    if (index < 0 || loading) {
      pendingSelectThreadId = threadId
      return
    }
    pendingSelectThreadId = null
    selectedThreadId = threadId
    lastClickedIndex = index
    scrollToIndex(index)
  }

  // Toggle search focus (exposed for keyboard navigation via Ctrl+S)
  // Three-state: closed → open, open but unfocused → focus, open and focused → close
  export function toggleSearchFocus() {
    switch (true) {
      case !showSearch:
        showSearch = true
        setTimeout(() => searchInputRef?.focus(), 50)
        break
      case document.activeElement !== searchInputRef:
        searchInputRef?.focus()
        break
      default:
        clearSearch()
    }
  }

  // Get the currently selected thread ID (exposed for parent access)
  export function getSelectedThreadId(): string | null {
    return selectedThreadId
  }

  // Get message IDs for the keyboard-focused thread (for delete without checking)
  export function getSelectedMessageIds(): string[] {
    if (!selectedThreadId) return []
    const conv = activeList.find(c => c.threadId === selectedThreadId) as any
    if (!conv) return []
    return conv.messageIds || conv.messages?.map((m: any) => m.id) || []
  }

  // Get account and folder info for the keyboard-focused thread (for unified inbox)
  export function getSelectedConversationInfo(): { accountId: string; folderId: string } | null {
    if (!selectedThreadId) return null
    const conv = activeList.find(c => c.threadId === selectedThreadId) as any
    if (!conv) return null

    const realAccountId = (isUnifiedView || isSearchMode) && conv.accountId ? conv.accountId : accountId
    const realFolderId = (isUnifiedView || isSearchMode) && conv.folderId ? conv.folderId : folderId

    if (!realAccountId || !realFolderId) return null
    return { accountId: realAccountId, folderId: realFolderId }
  }

  // Check if the keyboard-focused thread is starred
  export function isSelectedStarred(): boolean {
    if (!selectedThreadId) return false
    const conv = activeList.find(c => c.threadId === selectedThreadId) as any
    return conv?.isStarred ?? false
  }

  // Toggle checkbox for focused message (Space key)
  export function toggleCheck() {
    if (!selectedThreadId) return
    const newChecked = new Set(checkedThreadIds)
    toggleSetEntry(newChecked, selectedThreadId)
    checkedThreadIds = newChecked
    lastClickedIndex = getSelectedIndex()
  }

  // Select previous message AND check both current and previous (Shift+Up/k)
  export function selectPreviousWithCheck() {
    if (activeList.length === 0) return

    const currentIndex = getSelectedIndex()
    if (currentIndex <= 0) return  // Already at top or no selection

    const newIndex = currentIndex - 1
    const conv = activeList[newIndex]
    if (!conv) return

    // Check both current and new message
    const newChecked = new Set(checkedThreadIds)
    newChecked.add(activeList[currentIndex].threadId)
    newChecked.add(conv.threadId)
    checkedThreadIds = newChecked
    lastClickedIndex = newIndex

    // Move focus (but don't open in viewer)
    selectedThreadId = conv.threadId
    scrollToIndex(newIndex)
    // Blur any focused element so Enter key triggers openSelected() instead of the button
    ;(document.activeElement as HTMLElement)?.blur?.()
  }

  // Select next message AND check both current and next (Shift+Down/j)
  export function selectNextWithCheck() {
    if (activeList.length === 0) return

    const currentIndex = getSelectedIndex()
    if (currentIndex < 0 || currentIndex >= activeList.length - 1) return  // No selection or already at bottom

    const newIndex = currentIndex + 1
    const conv = activeList[newIndex]
    if (!conv) return

    // Check both current and new message
    const newChecked = new Set(checkedThreadIds)
    newChecked.add(activeList[currentIndex].threadId)
    newChecked.add(conv.threadId)
    checkedThreadIds = newChecked
    lastClickedIndex = newIndex

    // Move focus (but don't open in viewer)
    selectedThreadId = conv.threadId
    scrollToIndex(newIndex)
    // Blur any focused element so Enter key triggers openSelected() instead of the button
    ;(document.activeElement as HTMLElement)?.blur?.()
  }

  // Get all checked message IDs for bulk operations
  export function getCheckedMessageIds(): string[] {
    return selectedMessageIds
  }

  // Check if any messages are checked
  export function hasCheckedMessages(): boolean {
    return checkedThreadIds.size > 0
  }

  // Get aggregated star state (true if any unstarred)
  export function getCheckedHasUnstarred(): boolean {
    return selectedHasUnstarred
  }

  // Get aggregated read state (true if any unread)
  export function getCheckedHasUnread(): boolean {
    return selectedHasUnread
  }

  // Get index of earliest checked item (for post-delete focus)
  function getEarliestCheckedIndex(): number {
    if (checkedThreadIds.size === 0) return getSelectedIndex()
    for (let i = 0; i < activeList.length; i++) {
      if (checkedThreadIds.has(activeList[i].threadId)) return i
    }
    return getSelectedIndex()
  }

  // Clear all checkboxes
  export function clearChecked() {
    checkedThreadIds = new Set()
    lastClickedIndex = null
  }

  export function selectAll() {
    checkedThreadIds = new Set(activeList.map(c => c.threadId))
  }

  // Open context menu for the currently selected conversation row
  export function openContextMenu() {
    if (!selectedThreadId || !listContainerRef) return
    const index = activeList.findIndex(c => c.threadId === selectedThreadId)
    if (index < 0) return
    const rows = listContainerRef.querySelectorAll('[data-conversation-row]')
    const row = rows[index] as HTMLElement | undefined
    if (!row) return
    const rect = row.getBoundingClientRect()
    row.dispatchEvent(new MouseEvent('contextmenu', {
      bubbles: true,
      clientX: rect.right,
      clientY: rect.top + rect.height / 2,
    }))
  }

  // Row component refs keyed by threadId — lets keyboard shortcuts reach the
  // focused row's context-menu folder picker (Alt+M / Alt+C)
  let rowRefs: Record<string, ConversationRow | null> = {}

  function getFocusedRowRef(): ConversationRow | null {
    if (!selectedThreadId) return null
    return rowRefs[selectedThreadId] ?? null
  }

  export function isFolderPickerOpen(): boolean {
    return getFocusedRowRef()?.isFolderPickerOpen() ?? false
  }

  export function toggleMoveToDialog() {
    getFocusedRowRef()?.toggleFolderPicker('move')
  }

  export function toggleCopyToDialog() {
    getFocusedRowRef()?.toggleFolderPicker('copy')
  }

  // Permanent delete confirmation state
  let showDeleteConfirm = $state(false)
  let pendingDeleteIds = $state<string[]>([])

  // Empty trash confirmation state
  let showEmptyTrashConfirm = $state(false)
  let showEmptySpamConfirm = $state(false)

  async function handleUndo() {
    try {
      const description = await Undo()
      toasts.success($_('toast.undone', { values: { description } }))
    } catch (err) {
      console.error('Undo failed:', err)
      toasts.error($_('toast.undoFailed'))
    }
  }

  async function handleConfirmPermanentDelete() {
    try {
      await DeletePermanently(pendingDeleteIds)
      toasts.success($_('toast.permanentlyDeleted'))
      handleActionComplete(true)
      clearChecked()
    } catch (err) {
      console.error('Permanent delete failed:', err)
      toasts.error($_('toast.failedToDelete'))
    }
    showDeleteConfirm = false
    pendingDeleteIds = []
  }

  async function handleEmptyTrash() {
    if (!accountId || !folderId) return
    try {
      await EmptyTrash(accountId, folderId)
      toasts.success($_('toast.trashEmptied'))
      handleActionComplete(true)
      clearChecked()
    } catch (err) {
      console.error('Empty trash failed:', err)
      toasts.error($_('toast.failedToEmptyTrash'))
    }
    showEmptyTrashConfirm = false
  }

  // Opens the confirm dialog matching the viewed folder (trash or spam bar)
  function requestEmptyFolder() {
    if (folderType === 'trash') {
      showEmptyTrashConfirm = true
      return
    }
    showEmptySpamConfirm = true
  }

  async function handleEmptySpam() {
    if (!accountId || !folderId) return
    try {
      await EmptySpam(accountId, folderId)
      toasts.success($_('toast.spamEmptied'))
      handleActionComplete(true)
      clearChecked()
    } catch (err) {
      console.error('Empty spam failed:', err)
      toasts.error($_('toast.failedToEmptySpam'))
    }
    showEmptySpamConfirm = false
  }

  // Shared delete handler — same flow as context menu "Delete" action
  // Set permanent=true to force permanent delete (e.g. Shift+Delete)
  export function requestDelete(messageIds: string[], permanent: boolean = false) {
    if (permanent || folderType === 'trash') {
      pendingDeleteIds = messageIds
      showDeleteConfirm = true
      return
    }
    Trash(messageIds)
      .then((movedToTrash) => {
        const toastMsg = movedToTrash ? $_('toast.movedToTrash') : $_('toast.deletedFromFolder')
        const actions = movedToTrash ? [{ label: $_('common.undo'), onClick: handleUndo }] : []
        toasts.success(toastMsg, actions)
        handleActionComplete(true)
        clearChecked()
      })
      .catch((err) => {
        console.error('Delete failed:', err)
        toasts.error($_('toast.failedToDelete'))
      })
  }

  // Scroll to a specific index in the list
  function scrollToIndex(index: number) {
    if (!listContainerRef) return

    const rows = listContainerRef.querySelectorAll('[data-conversation-row]')
    const row = rows[index] as HTMLElement | undefined
    if (row) {
      row.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
    }
  }
</script>

<div class="flex flex-col h-full {isFlashing ? 'pane-focus-flash' : ''}">
  <!-- Header -->
  <div class="flex items-center justify-between px-4 py-3 border-b border-border">
    {#if checkedThreadIds.size > 0}
      <!-- Selection mode: bulk-action bar replaces the folder header.
           Search state is untouched — the query/results are kept and the
           search input returns when the selection is cleared. -->
      <div class="flex items-center gap-2 min-w-0">
        <!-- Select-all: only the LOADED page (50 at a time); indeterminate when partial -->
        <input
          bind:this={selectAllCheckboxRef}
          type="checkbox"
          class="h-4 w-4 shrink-0 cursor-pointer accent-primary"
          checked={selectionAllChecked}
          title={$_('messageList.selectAll')}
          aria-label={$_('messageList.selectAll')}
          onchange={handleBulkSelectAll}
        />
        <span class="text-sm font-semibold text-foreground whitespace-nowrap truncate min-w-0">
          {$_('messageList.selected', { values: { count: checkedThreadIds.size } })}
        </span>
        <button
          class="p-1.5 shrink-0 rounded-md hover:bg-muted transition-colors"
          title={$_('messageList.clearSelection')}
          aria-label={$_('messageList.clearSelection')}
          onclick={clearChecked}
        >
          <Icon icon="mdi:close" class="w-5 h-5 text-muted-foreground" />
        </button>
      </div>
      <div class="flex items-center gap-1 shrink-0">
        <button
          class="p-1.5 shrink-0 rounded-md hover:bg-muted transition-colors"
          title={selectedMajorityRead ? $_('contextMenu.markAsUnread') : $_('contextMenu.markAsRead')}
          aria-label={selectedMajorityRead ? $_('contextMenu.markAsUnread') : $_('contextMenu.markAsRead')}
          onclick={selectedMajorityRead ? handleBulkMarkUnread : handleBulkMarkRead}
        >
          <Icon icon={selectedMajorityRead ? "mdi:email-outline" : "mdi:email-open-outline"} class="w-5 h-5 text-muted-foreground" />
        </button>
        <button
          class="p-1.5 shrink-0 rounded-md hover:bg-muted transition-colors"
          title={$_('contextMenu.archive')}
          aria-label={$_('contextMenu.archive')}
          onclick={handleBulkArchive}
        >
          <Icon icon="mdi:archive-outline" class="w-5 h-5 text-muted-foreground" />
        </button>
        <button
          class="p-1.5 shrink-0 rounded-md hover:bg-muted transition-colors"
          title={$_(isTrashFolder ? 'contextMenu.deletePermanently' : 'contextMenu.delete')}
          aria-label={$_(isTrashFolder ? 'contextMenu.deletePermanently' : 'contextMenu.delete')}
          onclick={handleBulkDelete}
        >
          <Icon icon={isTrashFolder ? 'mdi:delete-forever' : 'mdi:delete-outline'} class="w-5 h-5 text-muted-foreground" />
        </button>
        <button
          class="p-1.5 shrink-0 rounded-md hover:bg-muted transition-colors"
          title={$_(isSpamFolder ? 'contextMenu.markAsNotSpam' : 'contextMenu.markAsSpam')}
          aria-label={$_(isSpamFolder ? 'contextMenu.markAsNotSpam' : 'contextMenu.markAsSpam')}
          onclick={handleBulkSpam}
        >
          <Icon icon={isSpamFolder ? 'mdi:email-check-outline' : 'mdi:alert-octagon-outline'} class="w-5 h-5 text-muted-foreground" />
        </button>
        <button
          class="p-1.5 shrink-0 rounded-md hover:bg-muted transition-colors"
          title={$_('contextMenu.moveTo')}
          aria-label={$_('contextMenu.moveTo')}
          onclick={openBulkMoveTo}
        >
          <Icon icon="mdi:folder-move-outline" class="w-5 h-5 text-muted-foreground" />
        </button>
        <button
          class="p-1.5 shrink-0 rounded-md hover:bg-muted transition-colors"
          title={$_(selectedHasUnstarred ? 'contextMenu.star' : 'contextMenu.removeStar')}
          aria-label={$_(selectedHasUnstarred ? 'contextMenu.star' : 'contextMenu.removeStar')}
          onclick={handleBulkToggleStar}
        >
          <Icon
            icon={selectedHasUnstarred ? 'mdi:star-outline' : 'mdi:star'}
            class="w-5 h-5 {selectedHasUnstarred ? 'text-muted-foreground' : 'text-yellow-500'}"
          />
        </button>
      </div>
    {:else}
    <div class="flex items-center gap-2">
      {#if showFolderToggle}
        <button
          class="p-1.5 -ml-1 rounded-md hover:bg-muted transition-colors"
          title={$_('responsive.folders')}
          aria-label={$_('aria.toggleSidebar')}
          onclick={onToggleSidebar}
        >
          <Icon icon="mdi:dock-left" class="w-5 h-5 text-muted-foreground" />
        </button>
      {/if}
      {#if showSearch}
        <!-- Search input -->
        <div class="flex items-center gap-1 bg-muted rounded-md px-2 flex-1">
          <Icon icon="mdi:magnify" class="w-4 h-4 text-muted-foreground flex-shrink-0" />
          <input
            bind:this={searchInputRef}
            type="text"
            placeholder={$_('messageList.searchMessages')}
            class="bg-transparent border-none outline-none text-sm py-1.5 w-full min-w-[200px]"
            bind:value={searchQuery}
            oninput={handleSearchInput}
            onkeydown={handleSearchKeydown}
          />
          {#if serverSearchMode}
            <button
              onclick={() => { serverSearchMode = false }}
              class="px-1.5 py-0.5 text-[10px] font-medium bg-primary/20 text-primary rounded-full flex-shrink-0 hover:bg-primary/30 transition-colors"
              title={$_('search.localSearch')}
            >
              {$_('search.server')}
            </button>
          {/if}
          {#if searchQuery || isSearching || isServerSearching}
            <button
              onclick={clearSearch}
              class="p-0.5 hover:bg-muted-foreground/20 rounded"
              title={$_('messageList.clearSearch')}
            >
              {#if isSearching || isServerSearching}
                <Icon icon="mdi:loading" class="w-4 h-4 animate-spin text-muted-foreground" />
              {:else}
                <Icon icon="mdi:close" class="w-4 h-4 text-muted-foreground" />
              {/if}
            </button>
          {/if}
        </div>
      {:else}
        <h2 class="font-semibold text-foreground">{scopeName}</h2>
        {#if !isOutboxView}
          <span class="text-sm text-muted-foreground">
            {$_('messageList.unread', { values: { count: unreadCount } })}
          </span>
        {/if}
      {/if}
    </div>
    {#if isOutboxView}
      <div class="flex items-center gap-1">
        <Button
          size="sm"
          variant="outline"
          disabled={queueActionBusy !== '' || waitingMailMergeItems.length === 0}
          onclick={sendUnsentNow}
        >
          <Icon icon="mdi:send" class="w-4 h-4 mr-1.5" />
          {$_('messageList.outboxSendUnsent')}
        </Button>
      </div>
    {:else}
    <div class="flex items-center gap-1">
      {#if syncing}
        <!-- While syncing, show spinning icon that cancels on click -->
        <button
          class="p-2 rounded-md hover:bg-muted transition-colors"
          title={syncProgress ? `${$_('sidebar.syncing')} ${syncProgress.phase}: ${syncProgress.percentage}% - ${$_('sidebar.clickToCancel')}` : `${$_('sidebar.syncing')} ${$_('sidebar.clickToCancel')}`}
          onclick={cancelFolderSync}
        >
          <Icon
            icon="mdi:refresh"
            class="w-5 h-5 text-muted-foreground animate-spin"
          />
        </button>
      {:else}
        <!-- Split refresh control: the main icon runs Sync Folder directly
             (enabled whenever there is something to sync — list loading
             does not disable it); the chevron opens the menu with Sync
             Folder / Force Re-sync. While a sync runs, the spinner above
             replaces both and cancels on click. -->
        <div class="flex items-center">
          <button
            class="p-2 rounded-md hover:bg-muted transition-colors disabled:opacity-50"
            disabled={!hasSyncTarget}
            title={isUnifiedView ? $_('messageList.syncAllInboxes') : $_('messageList.syncFolder')}
            aria-label={isUnifiedView ? $_('messageList.syncAllInboxes') : $_('messageList.syncFolder')}
            onclick={syncFolder}
          >
            <Icon
              icon="mdi:refresh"
              class="w-5 h-5 text-muted-foreground"
            />
          </button>
          <DropdownMenu.Root>
            <DropdownMenu.Trigger
              class="p-1 rounded-md hover:bg-muted transition-colors disabled:opacity-50"
              disabled={!hasSyncTarget}
              title={$_('messageList.moreSyncOptions')}
              aria-label={$_('messageList.moreSyncOptions')}
            >
              <Icon
                icon="mdi:chevron-down"
                class="w-4 h-4 text-muted-foreground"
              />
            </DropdownMenu.Trigger>
            <DropdownMenu.Portal>
              <DropdownMenu.Content
                side="bottom"
                align="end"
                sideOffset={4}
                class={cn(
                  'z-50 min-w-[180px] rounded-md border bg-popover p-1 text-popover-foreground shadow-md',
                  'data-[state=open]:animate-in data-[state=closed]:animate-out',
                  'data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0',
                  'data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95',
                  'data-[side=bottom]:slide-in-from-top-2'
                )}
              >
                <DropdownMenu.Item
                  onSelect={syncFolder}
                  class="relative flex cursor-default select-none items-center rounded-sm px-2 py-1.5 text-sm outline-none focus:bg-accent focus:text-accent-foreground"
                >
                  <Icon icon="mdi:refresh" class="w-4 h-4 mr-2" />
                  {$_('messageList.syncFolder')}
                </DropdownMenu.Item>
                <DropdownMenu.Separator class="-mx-1 my-1 h-px bg-border" />
                <DropdownMenu.Item
                  onSelect={forceSyncFolder}
                  disabled={isUnifiedView}
                  title={isUnifiedView ? $_('messageList.forceResyncUnifiedDisabled') : undefined}
                  class="relative flex cursor-default select-none items-center rounded-sm px-2 py-1.5 text-sm outline-none focus:bg-accent focus:text-accent-foreground data-[disabled]:opacity-50"
                >
                  <Icon icon="mdi:refresh-auto" class="w-4 h-4 mr-2" />
                  {$_('messageList.forceResync')}
                </DropdownMenu.Item>
              </DropdownMenu.Content>
            </DropdownMenu.Portal>
          </DropdownMenu.Root>
        </div>
      {/if}
      <button
        class="p-2 rounded-md hover:bg-muted transition-colors {showSearch ? 'bg-muted' : ''}"
        title={showSearch ? $_('common.close') : $_('common.search')}
        onclick={toggleSearch}
      >
        <Icon icon={showSearch ? 'mdi:close' : 'mdi:magnify'} class="w-5 h-5 text-muted-foreground" />
      </button>
      <DropdownMenu.Root>
        <DropdownMenu.Trigger
          class="p-2 rounded-md hover:bg-muted transition-colors {filterMode ? 'bg-muted' : ''}"
          title={$_('messageList.filter')}
        >
          <Icon
            icon={filterMode ? 'mdi:filter' : 'mdi:filter-outline'}
            class="w-5 h-5 {filterMode ? 'text-primary' : 'text-muted-foreground'}"
          />
        </DropdownMenu.Trigger>
        <DropdownMenu.Portal>
          <DropdownMenu.Content
            side="bottom"
            align="end"
            sideOffset={4}
            class={cn(
              'z-50 min-w-[180px] rounded-md border bg-popover p-1 text-popover-foreground shadow-md',
              'data-[state=open]:animate-in data-[state=closed]:animate-out',
              'data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0',
              'data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95',
              'data-[side=bottom]:slide-in-from-top-2'
            )}
          >
            {#each filterOptions as opt (opt.value ?? opt.label)}
              {#if opt.separator}
                <DropdownMenu.Separator class="-mx-1 my-1 h-px bg-border" />
              {/if}
              <DropdownMenu.Item
                onSelect={() => setFilter(opt.value)}
                class="relative flex cursor-default select-none items-center rounded-sm px-2 py-1.5 text-sm outline-none focus:bg-accent focus:text-accent-foreground"
              >
                <Icon icon="mdi:check" class="w-4 h-4 mr-2 {filterMode === opt.value ? '' : 'invisible'}" />
                {opt.label}
              </DropdownMenu.Item>
            {/each}
          </DropdownMenu.Content>
        </DropdownMenu.Portal>
      </DropdownMenu.Root>
      <button
        class="p-2 rounded-md hover:bg-muted transition-colors"
        title={getMessageListSortOrder() === 'newest' ? $_('messageList.showingNewest') : $_('messageList.showingOldest')}
        onclick={toggleSortOrder}
      >
        <Icon
          icon={getMessageListSortOrder() === 'newest' ? 'mdi:sort-descending' : 'mdi:sort-ascending'}
          class="w-5 h-5 text-muted-foreground"
        />
      </button>
    </div>
    {/if}
    {/if}
  </div>

  <!-- Active filter chip -->
  {#if filterMode}
    <div class="flex items-center gap-2 px-4 py-1.5 border-b border-border bg-muted/30">
      <span class="text-xs text-muted-foreground">{$_('messageList.filterLabel')}:</span>
      <button
        class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-xs bg-primary/10 text-primary hover:bg-primary/20 transition-colors"
        onclick={() => setFilter('')}
      >
        {filterLabel}
        <Icon icon="mdi:close" class="w-3 h-3" />
      </button>
    </div>
  {/if}

  <!-- Empty Trash/Spam bar (only shown when viewing one account's trash or
       spam folder with messages, not in search mode and never for a
       unified view — emptying spans accounts and has no single target). -->
  {#if !isUnifiedView && (folderType === 'trash' || folderType === 'spam') && totalCount > 0 && !isSearchMode}
    <div class="flex items-center justify-end px-4 py-2 bg-muted/50 border-b border-border">
      <Button
        size="sm"
        variant="outline"
        class="text-destructive hover:text-destructive hover:bg-destructive/10 border-destructive/50 bg-muted/50"
        onclick={requestEmptyFolder}
      >
        <Icon icon="mdi:delete-sweep-outline" class="w-4 h-4 mr-1.5" />
        {folderType === 'trash' ? $_('messageList.emptyTrash') : $_('messageList.emptySpam')}
      </Button>
    </div>
  {/if}

  <!-- FTS Indexing indicator (only shown when searching and index is incomplete) -->
  {#if showSearch && !indexComplete && isIndexing}
    <div class="px-4 py-2 bg-muted/50 border-b border-border">
      <div class="flex items-center gap-2 text-sm text-muted-foreground">
        <Icon icon="mdi:database-sync" class="w-4 h-4 animate-pulse" />
        <span>{$_('messageList.ftsBuilding', { values: { percentage: indexProgress } })}</span>
      </div>
      <div class="h-1 bg-muted rounded-full mt-1.5 overflow-hidden">
        <div
          class="h-full bg-primary transition-all duration-300"
          style="width: {indexProgress}%"
        ></div>
      </div>
    </div>
  {/if}

  <!-- Needs attention bars (Phase A): only Inbox / Unified Inbox, never
       Spam/Trash or search. They push the list down, never overlay. -->
  {#if !isSearchMode && (isUnifiedInboxView || folderType === 'inbox')}
    <NeedsAttentionStrip
      bind:this={attentionStripRef}
      folderId={isUnifiedInboxView ? null : folderId}
      unified={isUnifiedInboxView}
      onOpenItem={openAttentionItem}
    />
  {/if}

  <!-- Category tabs (Phase E): All / Personal / Automated, below the
       Needs attention bars, only in Inbox / Unified Inbox. A filter over
       the same folder — no mail moves, nothing to undo. -->
  {#if showCategoryTabs}
    <div class="flex items-center gap-1 px-4 pt-2 border-b border-border" role="tablist">
      {#each categoryTabs as tab (tab.value)}
        <button
          role="tab"
          aria-selected={inboxTab === tab.value}
          class="px-3 py-1.5 text-sm rounded-t-md border-b-2 transition-colors {inboxTab === tab.value
            ? 'border-primary text-foreground font-medium'
            : 'border-transparent text-muted-foreground hover:text-foreground'}"
          onclick={() => setCategoryTab(tab.value)}
        >
          {tab.label}
          {#if tab.count !== undefined}
            <span class="ml-1 text-xs text-muted-foreground">({tab.count})</span>
          {/if}
        </button>
      {/each}
    </div>
    <!-- Automated sub-filter chips: second level inside the Automated tab
         only. The row wraps instead of scrolling sideways. -->
    {#if inboxTab === 'automated'}
      <div class="flex flex-wrap items-center gap-1.5 px-4 py-2 border-b border-border">
        {#each subFilterOptions as opt (opt.value || 'all')}
          <button
            class="inline-flex items-center gap-1 px-2.5 py-1 rounded-full text-xs transition-colors {subFilter === opt.value
              ? 'bg-primary text-primary-foreground'
              : 'bg-muted text-muted-foreground hover:bg-muted/70 hover:text-foreground'}"
            onclick={() => setSubFilter(opt.value)}
          >
            {opt.label}
            {#if opt.count !== undefined}
              <span class="opacity-75">({opt.count})</span>
            {/if}
          </button>
        {/each}
      </div>
    {/if}
  {/if}

  <!-- Conversation List -->
  <div bind:this={listContainerRef} class="flex-1 overflow-y-auto scrollbar-thin">
    {#if isOutboxView}
      <!-- Outbox: the local Mail Merge send queue. Rows expand inline to
           show the queued message's details; nothing here is a
           conversation, so no row actions, tabs or viewer apply. -->
      {#if queueLoading && queueItems.length === 0}
        <div class="flex items-center justify-center h-32">
          <Icon icon="mdi:loading" class="w-6 h-6 animate-spin text-muted-foreground" />
        </div>
      {:else if queueError}
        <div class="flex flex-col items-center justify-center h-32 text-center px-4">
          <Icon icon="mdi:alert-circle-outline" class="w-8 h-8 text-destructive mb-2" />
          <p class="text-sm text-destructive">{queueError}</p>
          <button
            class="mt-2 text-sm text-primary hover:underline"
            onclick={() => loadQueue()}
          >
            {$_('messageList.tryAgain')}
          </button>
        </div>
      {:else if queueItems.length === 0}
        <div class="flex flex-col items-center justify-center h-full text-center px-6 text-muted-foreground">
          <Icon icon="mdi:send-clock-outline" class="w-12 h-12 mb-2" />
          <p>{$_('messageList.outboxEmpty')}</p>
          <p class="text-xs mt-1">{$_('messageList.outboxEmptyHint')}</p>
        </div>
      {:else}
        {#each queueGroups as group (group.key)}
          <div class="px-4 pt-3 pb-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">
            {group.label} ({group.items.length})
          </div>
          {#each group.items as item (item.id)}
            <div class="border-b border-border">
              <button
                type="button"
                class="w-full flex items-center gap-2 px-4 py-2.5 text-left hover:bg-muted/50"
                onclick={() => { expandedQueueId = expandedQueueId === item.id ? null : item.id }}
              >
                <Icon icon="mdi:send-clock-outline" class="w-4 h-4 text-muted-foreground flex-shrink-0" />
                <span class="flex-1 min-w-0">
                  <span class="block truncate text-sm font-medium">{item.subject || $_('messageList.noContent')}</span>
                  <span class="block truncate text-xs text-muted-foreground">{item.recipient}</span>
                </span>
                {#if item.kind === 'reaction'}
                  <span class="flex-shrink-0 px-1.5 py-0.5 text-xs font-medium rounded-full bg-muted text-muted-foreground">
                    {$_('messageList.outboxReaction')}
                  </span>
                {/if}
                <span class="flex-shrink-0 px-1.5 py-0.5 text-xs font-medium rounded-full {item.status === 'failed' ? 'bg-destructive/10 text-destructive' : item.status === 'sent' ? 'bg-emerald-500/10 text-emerald-600' : 'bg-muted text-muted-foreground'}">
                  {queueStatusLabel(item.status)}
                </span>
              </button>
              {#if expandedQueueId === item.id}
                <div class="px-4 pb-3 text-xs text-muted-foreground space-y-1">
                  <p class="truncate">From: {item.fromAddress}</p>
                  <p class="break-words">To: {item.to.join(', ')}</p>
                  <p>{$_('messageList.outboxAttempts', { values: { attempts: item.attempts, max: item.maxAttempts } })}</p>
                  {#if (item.kind === 'send' || item.kind === 'reaction') && item.status === 'pending' && item.sendAt}
                    <p>{$_('messageList.outboxSendAt', { values: { time: formatQueueTime(item.sendAt) } })}</p>
                  {/if}
                  {#if item.status === 'queued' && item.nextAttemptAt}
                    <p>{$_('messageList.outboxNextAttempt', { values: { time: formatQueueTime(item.nextAttemptAt) } })}</p>
                  {/if}
                  {#if item.sentAt}
                    <p>Sent: {formatQueueTime(item.sentAt)}</p>
                  {/if}
                  {#if item.lastError}
                    <p class="text-destructive break-words">{$_('messageList.outboxLastError', { values: { error: item.lastError } })}</p>
                  {/if}
                  {#if (item.kind === 'send' || item.kind === 'reaction') && item.status === 'pending'}
                    <div class="pt-1 flex gap-2">
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={queueActionBusy !== ''}
                        onclick={() => undoSendQueueItem(item)}
                      >
                        {$_('messageList.outboxUndo')}
                      </Button>
                    </div>
                  {/if}
                  {#if (item.kind === 'send' || item.kind === 'reaction') && item.status === 'failed'}
                    <div class="pt-1 flex gap-2">
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={queueActionBusy !== ''}
                        onclick={() => retrySendQueueItem(item.id)}
                      >
                        {$_('messageList.outboxRetry')}
                      </Button>
                    </div>
                  {/if}
                  {#if item.status === 'queued'}
                    <div class="pt-1">
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={queueActionBusy !== ''}
                        onclick={() => cancelQueuedItem(item.id)}
                      >
                        {$_('messageList.outboxCancel')}
                      </Button>
                    </div>
                  {/if}
                </div>
              {/if}
            </div>
          {/each}
        {/each}
      {/if}
    {:else if loading && conversations.length === 0 && !isSearchMode}
      <div class="flex items-center justify-center h-32">
        <Icon icon="mdi:loading" class="w-6 h-6 animate-spin text-muted-foreground" />
      </div>
    {:else if error}
      <div class="flex flex-col items-center justify-center h-32 text-center px-4">
        <Icon icon="mdi:alert-circle-outline" class="w-8 h-8 text-destructive mb-2" />
        <p class="text-sm text-destructive">{error}</p>
        <button
          class="mt-2 text-sm text-primary hover:underline"
          onclick={() => isSearchMode ? performSearch() : loadConversations()}
        >
          {$_('messageList.tryAgain')}
        </button>
      </div>
    {:else if !isUnifiedView && (!accountId || !folderId)}
      <div class="flex flex-col items-center justify-center h-full text-center px-6 text-muted-foreground">
        <Icon icon="mdi:email-outline" class="w-12 h-12 mb-2" />
        <p>{$_('messageList.selectFolder')}</p>
      </div>
    {:else if isSearchMode}
      <!-- Search Results -->
      {#if isSearching || isServerSearching}
        <div class="flex flex-col items-center justify-center h-32 gap-2">
          <Icon icon="mdi:loading" class="w-6 h-6 animate-spin text-muted-foreground" />
          {#if isServerSearching}
            <span class="text-xs text-muted-foreground">{$_('search.serverSearching')}</span>
          {/if}
        </div>
      {:else if serverSearchMode}
        <!-- Server search results -->
        {#if serverSearchResults.length === 0}
          <div class="flex flex-col items-center justify-center h-full text-center px-6 text-muted-foreground">
            <Icon icon="mdi:magnify" class="w-12 h-12 mb-2" />
            <p>{$_('messageList.noResults', { values: { query: searchQuery } })}</p>
          </div>
        {:else}
          <!-- Server results header -->
          <div class="flex items-center justify-between px-4 py-2 bg-muted/30 border-b border-border text-sm text-muted-foreground">
            <span>
              {#if serverSearchCount < serverSearchTotalCount}
                {$_('search.serverResultsCapped', { values: { shown: serverSearchCount, total: serverSearchTotalCount, query: searchQuery } })}
              {:else}
                {$_('search.serverResults', { values: { count: serverSearchCount, query: searchQuery } })}
              {/if}
            </span>
            <button
              class="text-xs text-primary hover:underline"
              onclick={() => { serverSearchMode = false }}
            >
              {$_('search.localSearch')}
            </button>
          </div>
          {#each serverSearchResults as result, index (result.threadId + '-' + index)}
            {@const resultAccountId = result.accountId || accountId}
            {@const resultFolderId = result.folderId || folderId}
            <ConversationRow
              bind:this={rowRefs[result.threadId]}
              conversation={result}
              density={getMessageListDensity()}
              selected={selectedThreadId === result.threadId}
              checked={checkedThreadIds.has(result.threadId)}
              accountId={resultAccountId}
              folderId={resultFolderId}
              {folderType}
              {selectedMessageIds}
              selectedIsStarred={!selectedHasUnstarred}
              selectedIsRead={!selectedHasUnread}
              isNonLocal={result._isLocal === false}
              onSelect={(e) => selectConversation(result.threadId, index, e)}
              onOpen={selectOnly ? () => openConversation(result.threadId, index) : undefined}
              onCheck={(checked, e) => handleCheck(result.threadId, checked, index, e)}
              onClearSelection={clearSelection}
              onActionComplete={handleActionComplete}
              {onReply}
              onDelete={(ids) => requestDelete(ids)}
            />
          {/each}

          <!-- Show all results button (when results are capped) -->
          {#if serverSearchCount < serverSearchTotalCount}
            <div class="flex justify-center py-4">
              <button
                bind:this={loadMoreButtonRef}
                class="text-sm text-primary hover:underline focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 rounded px-2 py-1"
                onclick={() => performServerSearch(0)}
                disabled={isServerSearching}
              >
                {isServerSearching ? $_('common.loading') : $_('search.showAllResults', { values: { total: serverSearchTotalCount } })}
              </button>
            </div>
          {/if}
        {/if}
      {:else if searchResults.length === 0}
        <div class="flex flex-col items-center justify-center h-full text-center px-6 text-muted-foreground">
          <Icon icon="mdi:magnify" class="w-12 h-12 mb-2" />
          <p>{$_('messageList.noResults', { values: { query: searchQuery } })}</p>
          {#if !indexComplete}
            <p class="text-xs mt-1">{$_('messageList.indexBuilding')}</p>
          {/if}
          {#if !isUnifiedView && accountId && folderId}
            <button
              class="mt-2 text-sm text-primary hover:underline"
              onclick={() => { serverSearchMode = true; lastServerQuery = searchQuery.trim(); performServerSearch() }}
            >
              {$_('search.searchOnServer')}
            </button>
          {/if}
        </div>
      {:else}
        <!-- Local search results header -->
        <div class="flex items-center justify-between px-4 py-2 bg-muted/30 border-b border-border text-sm text-muted-foreground">
          <span>{$_('messageList.foundResults', { values: { count: searchTotalCount, query: searchQuery } })}</span>
          {#if !isUnifiedView && accountId && folderId}
            <button
              class="text-xs text-primary hover:underline"
              onclick={() => { serverSearchMode = true; lastServerQuery = searchQuery.trim(); performServerSearch() }}
            >
              {$_('search.serverSearch')}
            </button>
          {/if}
        </div>
        {#each searchResults as result, index (result.threadId + '-' + index)}
          {@const resultAccountId = result.accountId || accountId}
          {@const resultFolderId = result.folderId || folderId}
          {@const resultAccountColor = result.accountColor || ''}
          {@const resultAccountName = result.accountName || ''}
          <ConversationRow
            bind:this={rowRefs[result.threadId]}
            conversation={result}
            density={getMessageListDensity()}
            selected={selectedThreadId === result.threadId}
            checked={checkedThreadIds.has(result.threadId)}
            accountId={isUnifiedView ? resultAccountId : accountId!}
            folderId={isUnifiedView ? resultFolderId : folderId!}
            {folderType}
            {selectedMessageIds}
            selectedIsStarred={!selectedHasUnstarred}
            selectedIsRead={!selectedHasUnread}
            showAccountIndicator={isUnifiedView}
            accountColor={resultAccountColor}
            accountName={resultAccountName}
            highlightedSubject={result.highlightedSubject}
            highlightedSnippet={result.highlightedSnippet}
            highlightedFromName={result.highlightedFromName}
            searchFolderName={result.folderName}
            searchFolderType={result.folderType}
            onSelect={(e) => selectConversation(result.threadId, index, e)}
              onOpen={selectOnly ? () => openConversation(result.threadId, index) : undefined}
            onCheck={(checked, e) => handleCheck(result.threadId, checked, index, e)}
            onClearSelection={clearSelection}
            onActionComplete={handleActionComplete}
            {onReply}
            onDelete={(ids) => requestDelete(ids)}
          />
        {/each}

        <!-- Load more search results -->
        {#if searchResults.length < searchTotalCount}
          <div class="flex justify-center py-4">
            <button
              bind:this={loadMoreButtonRef}
              class="text-sm text-primary hover:underline focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 rounded px-2 py-1"
              onclick={() => loadMoreSearchResults()}
              disabled={isSearching}
            >
              {isSearching ? $_('common.loading') : $_('messageList.loadMore', { values: { remaining: searchTotalCount - searchResults.length } })}
            </button>
          </div>
        {/if}
      {/if}
    {:else if conversations.length === 0 && filterMode}
      <div class="flex flex-col items-center justify-center h-full text-center px-6 text-muted-foreground">
        <Icon icon="mdi:filter-off-outline" class="w-12 h-12 mb-2" />
        <p>{$_('messageList.noFilteredMessages')}</p>
        <button
          class="mt-2 text-sm text-primary hover:underline"
          onclick={() => setFilter('')}
        >
          {$_('messageList.filterAll')}
        </button>
      </div>
    {:else if conversations.length === 0 && inboxTab !== 'all' && showCategoryTabs}
      <div class="flex flex-col items-center justify-center h-full text-center px-6 text-muted-foreground">
        <Icon icon="mdi:filter-off-outline" class="w-12 h-12 mb-2" />
        <p>{$_('messageList.noCategoryMessages')}</p>
        <button
          class="mt-2 text-sm text-primary hover:underline"
          onclick={() => setCategoryTab('all')}
        >
          {$_('messageList.categoryAll')}
        </button>
      </div>
    {:else if conversations.length === 0}
      {@const serverFolder = !isUnifiedView && accountId && folderId ? accountStore.getFolder(accountId, folderId) : null}
      <div class="flex flex-col items-center justify-center h-full text-center px-6 text-muted-foreground">
        <Icon icon="mdi:inbox-outline" class="w-12 h-12 mb-2" />
        {#if serverFolder && serverFolder.totalCount > 0}
          <!-- The server holds mail older than the sync period: say so instead of "no messages" (#458) -->
          <p class="max-w-xs">{$_('messageList.olderThanSyncPeriod', { values: { count: serverFolder.totalCount } })}</p>
        {:else}
          <p>{$_('messageList.noMessages')}</p>
        {/if}
        <button
          class="mt-2 text-sm text-primary hover:underline"
          onclick={syncFolder}
          disabled={syncing}
        >
          {$_('messageList.syncNow')}
        </button>
      </div>
    {:else}
      {#each conversations as conv, index (conv.threadId + '-' + (conv.accountId || accountId || ''))}
        {@const convAccountId = (conv as any).accountId || accountId}
        {@const convFolderId = (conv as any).folderId || folderId}
        {@const convAccountColor = (conv as any).accountColor || ''}
        {@const convAccountName = (conv as any).accountName || ''}
        <ConversationRow
          bind:this={rowRefs[conv.threadId]}
          conversation={conv}
          density={getMessageListDensity()}
          selected={selectedThreadId === conv.threadId}
          checked={checkedThreadIds.has(conv.threadId)}
          accountId={isUnifiedView ? convAccountId : accountId!}
          folderId={isUnifiedView ? convFolderId : folderId!}
          {folderType}
          {selectedMessageIds}
          selectedIsStarred={!selectedHasUnstarred}
          selectedIsRead={!selectedHasUnread}
          showAccountIndicator={isUnifiedView}
          accountColor={convAccountColor}
          accountName={convAccountName}
          onSelect={(e) => selectConversation(conv.threadId, index, e)}
          onOpen={selectOnly ? () => openConversation(conv.threadId, index) : undefined}
          onCheck={(checked, e) => handleCheck(conv.threadId, checked, index, e)}
          onClearSelection={clearSelection}
          onActionComplete={handleActionComplete}
          {onReply}
          onDelete={(ids) => requestDelete(ids)}
        />
      {/each}

      <!-- Load more button for pagination -->
      {#if conversations.length < totalCount}
        <div class="flex justify-center py-4">
          <button
            bind:this={loadMoreButtonRef}
            class="text-sm text-primary hover:underline focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 rounded px-2 py-1"
            onclick={() => {
              // Continue from the true end of the loaded window, not a
              // page-size increment — after a preserved reload (sync or
              // action) offset is 0 while conversations holds several pages,
              // and += PAGE_SIZE would re-fetch and append duplicate rows.
              offset = conversations.length
              loadConversations()
            }}
            disabled={loading}
          >
            {loading ? $_('common.loading') : $_('messageList.loadMore', { values: { remaining: totalCount - conversations.length } })}
          </button>
        </div>
      {/if}
    {/if}
  </div>
</div>

<!-- Permanent Delete Confirmation Dialog -->
<ConfirmDialog
  bind:open={showDeleteConfirm}
  title={$_('dialog.deletePermanently')}
  description={$_('dialog.deleteDescription')}
  confirmLabel={$_('dialog.confirmDeletePermanently')}
  variant="destructive"
  onConfirm={handleConfirmPermanentDelete}
  onCancel={() => { showDeleteConfirm = false; pendingDeleteIds = [] }}
/>

<!-- Empty Trash Confirmation Dialog -->
<ConfirmDialog
  bind:open={showEmptyTrashConfirm}
  title={$_('dialog.emptyTrash')}
  description={$_('dialog.emptyTrashDescription')}
  confirmLabel={$_('dialog.confirmEmptyTrash')}
  variant="destructive"
  onConfirm={handleEmptyTrash}
  onCancel={() => { showEmptyTrashConfirm = false }}
/>

<!-- Empty Spam Confirmation Dialog -->
<ConfirmDialog
  bind:open={showEmptySpamConfirm}
  title={$_('dialog.emptySpam')}
  description={$_('dialog.emptySpamDescription')}
  confirmLabel={$_('dialog.confirmEmptySpam')}
  variant="destructive"
  onConfirm={handleEmptySpam}
  onCancel={() => { showEmptySpamConfirm = false }}
/>

<!-- Bulk Move to folder picker (reuses the context menu's picker) -->
<FolderPickerDialog
  bind:open={showBulkFolderPicker}
  title={$_('contextMenu.moveTo')}
  initialAccountId={bulkMoveInitialAccountId}
  accounts={bulkAccounts}
  excludeFolderId={folderId ?? ''}
  onSelect={handleBulkFolderSelected}
/>
