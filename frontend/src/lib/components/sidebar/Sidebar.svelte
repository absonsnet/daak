<script lang="ts">
  import Icon from '@iconify/svelte'
  import { onMount } from 'svelte'
  import AccountSection from './AccountSection.svelte'
  import UnifiedSection, { UNIFIED_FOLDER_DEFS } from './UnifiedSection.svelte'
  import AccountDialog from '$lib/components/settings/AccountDialog.svelte'
  import DeleteAccountDialog from '$lib/components/settings/DeleteAccountDialog.svelte'
  import SettingsDialog from '$lib/components/settings/SettingsDialog.svelte'
  import SidebarFooter from '$lib/components/kit/SidebarFooter.svelte'
  import { Button } from '$lib/components/ui/button'
  import { accountStore } from '$lib/stores/accounts.svelte'
  import { contactSourcesStore } from '$lib/stores/contactSources.svelte'
  import { setFolderCollapsed, getUIState, getUIStateVersion, saveUIState } from '$lib/stores/uiState.svelte'
  import { loadSidebarSettings, hasStoredExpandedAccounts, getExpandedAccountIds, setExpandedAccountIds, setSidebarAccountExpanded, getHideUnifiedSingleAccount, getUnifiedFolderOrder, setUnifiedFolderOrder } from '$lib/stores/sidebarSettings.svelte'
  import { setFocusedPane } from '$lib/stores/keyboard.svelte'
  import { _ } from '$lib/i18n'
  // @ts-ignore - wailsjs path
  import { account, folder } from '../../../../wailsjs/go/models'
  // @ts-ignore - wailsjs path
  import { GetUnifiedFolderUnreadCounts, GetAttentionCount, MailMerge_GetQueueStats, GetSendQueueStats } from '../../../../wailsjs/go/app/App'
  import { formatDistanceToNow } from 'date-fns'
  import { getCurrentDateFnsLocale } from '$lib/stores/settings.svelte'
  import { EventsOn, EventsEmit } from '../../../../wailsjs/runtime/runtime'

  // Folder item type for flat navigation list
  interface FolderNavItem {
    type: 'unified-folder' | 'attention' | 'account-header' | 'folder'
    accountId?: string
    folderId?: string
    folderPath?: string
    folderName: string
    folderType?: string
  }

  // Track focused account header for keyboard navigation
  let focusedAccountId = $state<string | null>(null)

  // Ref to scrollable container for auto-scroll
  let scrollContainer: HTMLDivElement | null = null

  // Track expanded state for each account (reactive, synced with persisted
  // state in the sidebar_expanded_accounts setting)
  let expandedAccounts = $state<Record<string, boolean>>({})

  // True once the launch-time collapse below has run. The effect re-runs
  // when the account list changes mid-session; only its first run with
  // accounts present forces the collapse — later runs keep today's
  // behaviour (stored set rules, pruned to existing accounts), so adding
  // an account mid-session does not collapse what the user just opened.
  let expandedInitDone = false

  // Initialize expanded state from the persisted setting.
  // On launch every account starts collapsed — the stored set from the
  // previous session does NOT carry over, and a first run after the
  // upgrade no longer expands the first account. Expanding an account
  // with its chevron still works during a session; it just does not
  // survive the next launch.
  $effect(() => {
    // Read version to create dependency - effect re-runs when UI state finishes loading
    const _version = getUIStateVersion()

    const ids = accountStore.accounts.map((acc) => acc.account.id)
    if (ids.length === 0) {
      expandedAccounts = {}
      return
    }

    if (!expandedInitDone) {
      // First run with accounts present: collapse everything.
      setExpandedAccountIds([])
      const collapsed: Record<string, boolean> = {}
      for (const id of ids) {
        collapsed[id] = false
      }
      expandedAccounts = collapsed
      expandedInitDone = true
      return
    }

    if (!hasStoredExpandedAccounts()) {
      setExpandedAccountIds([ids[0]])
    } else {
      // Prune IDs of accounts that no longer exist
      const stored = getExpandedAccountIds()
      const pruned = stored.filter((id) => ids.includes(id))
      if (pruned.length !== stored.length) setExpandedAccountIds(pruned)
    }

    const storedSet = new Set(getExpandedAccountIds())
    const newExpanded: Record<string, boolean> = {}
    for (const id of ids) {
      newExpanded[id] = storedSet.has(id)
    }
    expandedAccounts = newExpanded
  })

  // Set account expansion (local state + persisted setting)
  function setAccountExpandedValue(accountId: string, expanded: boolean) {
    expandedAccounts = { ...expandedAccounts, [accountId]: expanded }
    setSidebarAccountExpanded(accountId, expanded)
  }

  // Toggle account expansion
  function toggleAccountExpanded(accountId: string) {
    setAccountExpandedValue(accountId, !expandedAccounts[accountId])
  }

  // Track collapsed state for folders with children (reactive, synced with persisted state)
  let collapsedFolders = $state<Record<string, boolean>>({})

  // Initialize collapsed state from persisted storage and prune stale entries
  $effect(() => {
    const _version = getUIStateVersion()

    // Collect all folder IDs that exist in current accounts
    const allFolderIds = new Set<string>()
    const collectIds = (trees: folder.FolderTree[]) => {
      for (const tree of trees) {
        if (tree.folder) allFolderIds.add(tree.folder.id)
        if (tree.children) collectIds(tree.children)
      }
    }
    for (const acc of accountStore.accounts) {
      collectIds(acc.folders || [])
    }

    // Read persisted collapsed state, keep only entries for existing folders
    const persisted = getUIState().collapsedFolders
    const newCollapsed: Record<string, boolean> = {}
    let hasStale = false
    for (const folderId of Object.keys(persisted)) {
      if (allFolderIds.has(folderId)) {
        newCollapsed[folderId] = persisted[folderId]
      } else {
        hasStale = true
      }
    }
    collapsedFolders = newCollapsed

    // Persist cleaned state if stale entries were pruned
    if (hasStale) {
      saveUIState({ collapsedFolders: newCollapsed })
    }
  })

  // Toggle folder collapse
  function toggleFolderCollapsed(folderId: string) {
    const isCurrentlyCollapsed = collapsedFolders[folderId] !== false
    const newValue = !isCurrentlyCollapsed
    setFolderCollapsedValue(folderId, newValue)
  }

  // Set folder collapse explicitly (keyboard expand/collapse)
  function setFolderCollapsedValue(folderId: string, collapsed: boolean) {
    collapsedFolders = { ...collapsedFolders, [folderId]: collapsed }
    setFolderCollapsed(folderId, collapsed)
  }

  interface Props {
    onFolderSelect?: (accountId: string, folderId: string, folderPath: string, folderName: string, folderType: string) => void
    onUnifiedFolderSelect?: (folderType: string, label: string) => void
    onUnifiedInboxSelect?: () => void
    onCompose?: () => void
    onMessagesMoved?: () => void
    selectedAccountId?: string | null
    selectedFolderId?: string | null
    selectionSource?: 'unified' | 'account' | null
    isFocused?: boolean
    isFlashing?: boolean
    showBackButton?: boolean
    onBack?: () => void
  }

  let {
    onFolderSelect,
    onUnifiedFolderSelect,
    onUnifiedInboxSelect,
    onCompose,
    onMessagesMoved,
    selectedAccountId = null,
    selectedFolderId = null,
    selectionSource = null,
    isFocused: _isFocused = false,
    isFlashing = false,
    showBackButton = false,
    onBack,
  }: Props = $props()

  // Unified folder counts: unread per folder type, for the sidebar badges.
  let unifiedUnreadCounts = $state<Record<string, number>>({})

  // Outbox badge: Mail Merge items still waiting to be sent.
  let outboxCount = $state(0)

  // Unified folder display order (drag-to-reorder, persisted).
  let unifiedFolderOrder = $state<string[]>(getUnifiedFolderOrder())

  // Needs attention (Phase A): live count for the shortcut entry in the
  // Unified zone. Hidden when the count is 0.
  let attentionCount = $state(0)

  function handleUnifiedFolderReorder(order: string[]) {
    unifiedFolderOrder = order
    setUnifiedFolderOrder(order)
  }

  // Dialog state
  let showAccountDialog = $state(false)
  let showDeleteDialog = $state(false)
  let showSettingsDialog = $state(false)
  let editingAccount = $state<account.Account | null>(null)
  let deletingAccount = $state<account.Account | null>(null)

  // Load accounts and contact sources on mount
  onMount(() => {
    loadSidebarSettings()

    // Load accounts, then trigger comprehensive sync on launch
    accountStore.load().then(async () => {
      try {
        await accountStore.syncAllComplete()
      } catch (err) {
        console.error('Failed to sync on launch:', err)
      }
    })

    contactSourcesStore.load()
    loadUnifiedFolderCounts()
    loadAttentionCount()

    // Listen for folder count changes to update unified folder counts
    const unsubscribe = EventsOn('folders:countsChanged', (_data: Record<string, number>) => {
      loadUnifiedFolderCounts()
    })
    // The outbox badge follows the Mail Merge queue AND the Undo
    // Send queue (normal sends waiting out the undo window).
    const unsubscribeQueue = EventsOn('mailmerge:queue-updated', () => {
      loadOutboxCount()
    })
    const unsubscribeSendQueue = EventsOn('sendqueue:updated', () => {
      loadOutboxCount()
    })
    // Chip actions, dismissals and settings changes all emit this.
    const unsubscribeAttention = EventsOn('attention:changed', () => {
      loadAttentionCount()
    })
    // Tray menu: open the Settings dialog (the Go side shows the window
    // first).
    const unsubscribeTraySettings = EventsOn('tray:settings', () => {
      showSettingsDialog = true
    })

    return () => {
      unsubscribe()
      unsubscribeQueue()
      unsubscribeSendQueue()
      unsubscribeAttention()
      unsubscribeTraySettings()
    }
  })

  // Load unified folder unread counts + the outbox badge
  async function loadUnifiedFolderCounts() {
    try {
      unifiedUnreadCounts = await GetUnifiedFolderUnreadCounts()
    } catch (err) {
      console.error('Failed to load unified folder counts:', err)
    }
    await loadOutboxCount()
  }

  async function loadOutboxCount() {
    try {
      const [mmStats, sqStats] = await Promise.all([
        MailMerge_GetQueueStats(),
        GetSendQueueStats(),
      ])
      // The badge counts everything in the Outbox that still needs
      // attention (fourth-audit finding 48): waiting Mail Merge and
      // normal sends, failed sends of either kind, and normal sends
      // that were delivered but never saved to Sent. Counting only
      // the waiting rows used to drop the badge to zero on the one
      // thing needing attention — a failed send.
      outboxCount =
        (mmStats?.queued ?? 0) +
        (mmStats?.failed ?? 0) +
        (sqStats?.pending ?? 0) +
        (sqStats?.failed ?? 0) +
        (sqStats?.sentUnsaved ?? 0)
    } catch (err) {
      console.error('Failed to load outbox count:', err)
    }
  }

  // Load Needs attention count
  async function loadAttentionCount() {
    try {
      attentionCount = await GetAttentionCount()
    } catch (err) {
      console.error('Failed to load attention count:', err)
    }
  }

  // Needs attention shortcut: opens Inbox/Unified Inbox with the bars in
  // place (and scrolls the list to them when already there). This is a
  // shortcut to the same bars, not a separate folder.
  function handleAttentionSelect() {
    handleUnifiedInboxSelect()
    EventsEmit('attention:scrollToTop')
  }

  // Find the first folder of a given type in a folder tree (recursive —
  // special folders can be nested, e.g. Gmail's [Gmail]/Sent).
  function findFolderOfType(trees: folder.FolderTree[], type: string): folder.Folder | null {
    for (const tree of trees) {
      if (tree.folder?.type === type) return tree.folder
      if (tree.children) {
        const found = findFolderOfType(tree.children, type)
        if (found) return found
      }
    }
    return null
  }

  // Find the inbox folder in a folder tree
  function findInboxFolder(trees: folder.FolderTree[]): folder.Folder | null {
    return findFolderOfType(trees, 'inbox')
  }

  // The Unified zone still shows with a single account, unless the
  // sidebar_hide_unified_single_account setting hides it.
  let showUnified = $derived(
    accountStore.accounts.length !== 1 || !getHideUnifiedSingleAccount()
  )

  // Handle unified inbox selection (All Inboxes)
  function handleUnifiedInboxSelect() {
    onUnifiedInboxSelect?.()
  }

  // Handle folder selection from the Unified zone (one of the seven
  // unified folders — the view merges that folder across all accounts).
  function handleUnifiedFolderSelect(folderType: string, label: string) {
    onUnifiedFolderSelect?.(folderType, label)
  }

  // --- Sidebar divider: Unified zone vs Accounts block ---
  // The divider sits just above the first account and is draggable up and
  // down. It sets the Accounts pane height (px); the Unified pane takes
  // the rest. The position persists in the backend UI state
  // (accountsHeight, 0 = unset → size to content).
  //
  // Detents: the divider moves freely past both section boundaries, but it
  // SNAPS at each one — the drag sticks at the boundary until the pointer
  // pushes a little further, and the divider highlights while snapped so
  // the stop is felt and seen:
  //   - bottom detent: Accounts pane exactly fits every account row;
  //     dragging further down starts hiding accounts.
  //   - top detent: Unified pane exactly fits every unified folder;
  //     dragging further up starts hiding unified folders.
  // Past a detent the clipped section scrolls internally (overflow-y-auto)
  // with a scrollbar that brightens while scrolling.
  const DIVIDER_HEIGHT = 9
  // Hard extremes for the Accounts pane: label + one account header + Add
  // Account button at the bottom, the UNIFIED heading at the top. The
  // detents sit inside this range; the hard extremes only stop a pane
  // collapsing to nothing.
  const ACCOUNTS_MIN_PANE = 96
  const UNIFIED_MIN_PANE = 64
  // Snap feel: the drag sticks within this many px of a detent, and needs
  // this much further pointer travel to break past it.
  const SNAP_RADIUS = 10
  const SNAP_BREAKAWAY = 16

  let unifiedContent = $state<HTMLDivElement | null>(null)
  let accountsContent = $state<HTMLDivElement | null>(null)

  // Live Accounts pane height (px). 0 until measured — before that the
  // pane sizes to its content.
  let accountsHeightPx = $state(0)
  // Whether the persisted divider position has been applied once. The
  // Sidebar mounts before loadUIState() resolves, so the first measure
  // runs with no persisted value; without this flag the content-fit
  // default set then would block the restore when the load lands.
  let dividerRestored = false
  // Set when the user moves the divider (drag or keyboard) — a live
  // adjustment always wins over the persisted value.
  let dividerUserAdjusted = false
  // Hard bounds + detents, recomputed from the measured content heights.
  let accountsMin = $state(ACCOUNTS_MIN_PANE)
  let accountsMax = $state(ACCOUNTS_MIN_PANE)
  // Bottom detent: Accounts pane exactly fits its content (every account
  // visible). Top detent: Unified pane exactly fits its content (every
  // unified folder visible). Null when that boundary cannot be reached
  // inside the hard range (section taller than the window).
  let detentBottom = $state<number | null>(null)
  let detentTop = $state<number | null>(null)

  let dividerDragging = $state(false)
  let dividerDragStartY = 0
  let dividerDragStartHeight = 0
  // Snap engagement during a drag: which detent is holding the divider,
  // and the pointer Y where it engaged. While engaged the height stays
  // pinned until the pointer travels SNAP_BREAKAWAY past the detent.
  let snapDetent = $state<number | null>(null)
  let snapPointerY = 0
  // A detent released during this drag stays disarmed until the pointer
  // moves clear of it, so the drag does not re-snap on the same spot.
  let snapDisarmed = new Set<number>()

  // Whether the divider is sitting on a detent right now (dragging or
  // parked) — drives the snap highlight and the accessible value text.
  let dividerSnapped = $derived.by(() => {
    const h = Math.round(accountsHeightPx)
    if (snapDetent !== null && Math.round(snapDetent) === h) return true
    return detents().some((d) => Math.round(d) === h)
  })
  let dividerSnapLabel = $derived.by(() => {
    const h = Math.round(accountsHeightPx)
    if (detentBottom !== null && Math.round(detentBottom) === h) return $_('sidebar.dividerSnapAccounts')
    if (detentTop !== null && Math.round(detentTop) === h) return $_('sidebar.dividerSnapUnified')
    return ''
  })

  function clampNum(value: number, min: number, max: number): number {
    return Math.max(min, Math.min(max, value))
  }

  function detents(): number[] {
    const out: number[] = []
    if (detentBottom !== null) out.push(detentBottom)
    if (detentTop !== null) out.push(detentTop)
    return out.sort((a, b) => a - b)
  }

  // Re-measure content heights and re-derive the hard bounds, the detents
  // and the position. A clipped section scrolls internally; the bounds only
  // stop a pane collapsing to nothing.
  function measureDividerBounds() {
    if (!scrollContainer || !accountsContent) return
    const columnH = scrollContainer.getBoundingClientRect().height
    const unifiedContentH = unifiedContent?.scrollHeight ?? 0
    const accountsContentH = accountsContent.scrollHeight
    accountsMin = ACCOUNTS_MIN_PANE
    accountsMax = Math.max(accountsMin, columnH - DIVIDER_HEIGHT - UNIFIED_MIN_PANE)
    detentBottom =
      accountsContentH > accountsMin && accountsContentH < accountsMax
        ? accountsContentH
        : null
    const top = columnH - DIVIDER_HEIGHT - unifiedContentH
    detentTop =
      top > accountsMin &&
      top < accountsMax &&
      (detentBottom === null || Math.abs(top - detentBottom) > SNAP_RADIUS + 2)
        ? top
        : null
    const persisted = getUIState().accountsHeight || 0
    if (!dividerRestored && persisted > 0 && !dividerUserAdjusted) {
      accountsHeightPx = clampNum(persisted, accountsMin, accountsMax)
      dividerRestored = true
    } else if (accountsHeightPx === 0) {
      accountsHeightPx = Math.min(accountsContentH, accountsMax)
    } else {
      accountsHeightPx = clampNum(accountsHeightPx, accountsMin, accountsMax)
    }
  }

  // Re-measure when anything that changes the content heights changes.
  // The effect runs after the DOM updates, so the measurements are fresh.
  // Skipped while dragging (the drag handler owns the height then).
  $effect(() => {
    const _version = getUIStateVersion()
    const _accounts = accountStore.accounts
    const _expanded = expandedAccounts
    const _attention = attentionCount
    const _order = unifiedFolderOrder
    const _showUnified = showUnified
    if (dividerDragging) return
    measureDividerBounds()
  })

  function startResizeDivider(e: MouseEvent) {
    e.preventDefault()
    dividerUserAdjusted = true
    dividerDragStartY = e.clientY
    dividerDragStartHeight = accountsHeightPx
    dividerDragging = true
    snapDetent = null
    snapDisarmed = new Set()
  }

  function handleDividerMouseMove(e: MouseEvent) {
    if (!dividerDragging) return
    const raw = clampNum(
      dividerDragStartHeight + (dividerDragStartY - e.clientY),
      accountsMin,
      accountsMax
    )

    // A snap holds the divider at its detent until the pointer pushes
    // SNAP_BREAKAWAY past it; the breakaway travel is spent on the
    // resistance, and the drag then resumes from the detent with no jump.
    if (snapDetent !== null) {
      if (Math.abs(e.clientY - snapPointerY) <= SNAP_BREAKAWAY) {
        accountsHeightPx = snapDetent
        return
      }
      snapDisarmed.add(snapDetent)
      const broken = snapDetent
      snapDetent = null
      dividerDragStartY = e.clientY
      dividerDragStartHeight = broken
      accountsHeightPx = broken
      return
    }

    // Re-arm a disarmed detent once the pointer has moved clear of it.
    for (const d of [...snapDisarmed]) {
      if (Math.abs(raw - d) > SNAP_RADIUS + 2) snapDisarmed.delete(d)
    }

    // Snap: within SNAP_RADIUS of an armed detent, stick to it.
    for (const d of detents()) {
      if (!snapDisarmed.has(d) && Math.abs(raw - d) <= SNAP_RADIUS) {
        snapDetent = d
        snapPointerY = e.clientY
        accountsHeightPx = d
        return
      }
    }

    accountsHeightPx = raw
  }

  function handleDividerMouseUp() {
    if (!dividerDragging) return
    dividerDragging = false
    snapDetent = null
    // Persist through the same setting the list/sidebar widths use.
    saveUIState({ accountsHeight: Math.round(accountsHeightPx) })
  }

  // Keyboard: arrows move the divider 10px (Shift: 50px), persisted. A
  // step that crosses a detent lands exactly on it; the next press
  // continues past.
  function handleDividerKeyDown(e: KeyboardEvent) {
    let delta = 0
    if (e.key === 'ArrowUp') delta = e.shiftKey ? 50 : 10
    else if (e.key === 'ArrowDown') delta = -(e.shiftKey ? 50 : 10)
    else return
    e.preventDefault()
    dividerUserAdjusted = true
    let next = clampNum(accountsHeightPx + delta, accountsMin, accountsMax)
    for (const d of detents()) {
      if (
        (delta > 0 && accountsHeightPx < d && next >= d) ||
        (delta < 0 && accountsHeightPx > d && next <= d)
      ) {
        next = d
        break
      }
    }
    accountsHeightPx = next
    snapDetent = detents().includes(next) ? next : null
    saveUIState({ accountsHeight: Math.round(accountsHeightPx) })
  }

  // Scrollbar emphasis: while a sidebar pane is scrolling, brighten its
  // thin scrollbar thumb so the scroll position reads at a glance. The
  // class drops shortly after scrolling stops.
  let unifiedScrollTimer: ReturnType<typeof setTimeout> | undefined
  let accountsScrollTimer: ReturnType<typeof setTimeout> | undefined

  function handlePaneScroll(e: Event, which: 'unified' | 'accounts') {
    const el = e.currentTarget as HTMLElement | null
    if (!el) return
    el.classList.add('scrolling')
    const prev = which === 'unified' ? unifiedScrollTimer : accountsScrollTimer
    if (prev) clearTimeout(prev)
    const next = setTimeout(() => el.classList.remove('scrolling'), 700)
    if (which === 'unified') unifiedScrollTimer = next
    else accountsScrollTimer = next
  }

  // Sync status: { accountName, label, percentage } — accountName + percentage are populated only when a sync is active
  let syncStatus = $derived.by<{ accountName: string | null; label: string; percentage: number | null }>(() => {
    if (accountStore.isAnySyncing) {
      const syncingAcc = accountStore.accounts.find((a) => a.syncing)
      if (syncingAcc) {
        const accountName = syncingAcc.account.name
        const progress = accountStore.getSyncProgress(syncingAcc.account.id)
        if (progress) {
          if (progress.phase === 'folders') return { accountName, label: $_('sidebar.syncingFolders'), percentage: null }
          if (progress.phase === 'messages') return { accountName, label: $_('sidebar.fetchingMessageList'), percentage: null }
          if (progress.phase === 'headers') return { accountName, label: $_('sidebar.fetchingHeaders', { values: { percentage: progress.percentage } }), percentage: progress.percentage }
          return { accountName, label: $_('sidebar.syncingContent', { values: { percentage: progress.percentage } }), percentage: progress.percentage }
        }
        return { accountName, label: $_('sidebar.syncing'), percentage: null }
      }
      return { accountName: null, label: $_('sidebar.syncing'), percentage: null }
    }
    if (!accountStore.isOnline) return { accountName: null, label: $_('sidebar.offline'), percentage: null }
    if (!accountStore.lastSyncTime) return { accountName: null, label: $_('sidebar.notSynced'), percentage: null }
    return { accountName: null, label: $_('sidebar.synced', { values: { time: formatDistanceToNow(accountStore.lastSyncTime, { addSuffix: true, locale: getCurrentDateFnsLocale() }) } }), percentage: null }
  })

  // Handle folder selection
  function handleFolderSelect(accountId: string, folderId: string, folderPath: string, folderName: string, folderType: string) {
    accountStore.selectFolder(accountId, folderId, folderPath, folderName)
    onFolderSelect?.(accountId, folderId, folderPath, folderName, folderType)
  }

  // Open an account's Inbox (account-name click): navigates to the Inbox
  // only. It deliberately does NOT expand the account — expansion happens
  // only on the chevron. When the account has no Inbox folder synced yet,
  // there is nothing to open; the chevron still reveals its state.
  function openAccountInbox(accountId: string) {
    const accWithFolders = accountStore.accounts.find((a) => a.account.id === accountId)
    if (!accWithFolders) return
    const inbox = findInboxFolder(accWithFolders.folders || [])
    if (inbox) {
      handleFolderSelect(accountId, inbox.id, inbox.path, inbox.name, 'inbox')
    }
  }

  // Does this account's folder tree contain the open folder?
  function treeContainsFolder(trees: folder.FolderTree[], folderId: string): boolean {
    for (const tree of trees) {
      if (tree.folder?.id === folderId) return true
      if (tree.children && treeContainsFolder(tree.children, folderId)) return true
    }
    return false
  }

  function accountContainsSelection(accountId: string): boolean {
    if (!selectedFolderId) return false
    const acc = accountStore.accounts.find((a) => a.account.id === accountId)
    if (!acc) return false
    return treeContainsFolder(acc.folders || [], selectedFolderId)
  }

  function accountInboxUnread(accountId: string): number {
    const acc = accountStore.accounts.find((a) => a.account.id === accountId)
    if (!acc) return 0
    return findInboxFolder(acc.folders || [])?.unreadCount ?? 0
  }

  // Open add account dialog
  function openAddAccount() {
    editingAccount = null
    showAccountDialog = true
  }

  // Open edit account dialog
  function openEditAccount(acc: account.Account) {
    editingAccount = acc
    showAccountDialog = true
  }

  // Open delete confirmation
  function openDeleteAccount(acc: account.Account) {
    deletingAccount = acc
    showDeleteDialog = true
  }

  // Sync all accounts (comprehensive sync)
  export async function syncAllAccounts() {
    try {
      await accountStore.syncAllComplete()
    } catch (err) {
      console.error('Sync failed:', err)
      // Error is already stored in account store
    }
  }

  // Cancel all running syncs
  export async function cancelSync() {
    try {
      await accountStore.cancelAllSyncs()
    } catch (err) {
      console.error('Failed to cancel sync:', err)
    }
  }

  // Toggle sync (start if not running, cancel if running) - for keyboard shortcut
  export async function toggleSync() {
    if (accountStore.isAnySyncing) {
      await cancelSync()
    } else {
      await syncAllAccounts()
    }
  }

  // Build flat list of all navigable rows in exact visual order:
  // Unified zone (inbox, attention, type groups + their per-account rows),
  // then the accounts block (header + folders when expanded).
  function buildFolderNavList(): FolderNavItem[] {
    const items: FolderNavItem[] = []

    if (showUnified && accountStore.accounts.length > 0) {
      // The seven unified folders, in the persisted display order.
      for (const type of unifiedFolderOrder) {
        items.push({
          type: 'unified-folder',
          folderName: type,
          folderType: type,
        })
      }

      if (attentionCount > 0) {
        items.push({
          type: 'attention',
          folderName: 'Needs attention',
        })
      }
    }

    // Accounts block, in store order (first account on top)
    for (const accWithFolders of accountStore.accounts) {
      // Skip if account is not fully loaded yet (can happen during reauth)
      if (!accWithFolders.account) continue

      // Always add the account header (so user can navigate to it and expand)
      items.push({
        type: 'account-header',
        accountId: accWithFolders.account.id,
        folderName: accWithFolders.account.name,
      })

      // Only add folders if the account is expanded
      if (expandedAccounts[accWithFolders.account.id] ?? false) {
        const flattenFolders = (trees: folder.FolderTree[]) => {
          for (const tree of trees) {
            if (tree.folder) {
              items.push({
                type: 'folder',
                accountId: accWithFolders.account.id,
                folderId: tree.folder.id,
                folderPath: tree.folder.path,
                folderName: tree.folder.name,
                folderType: tree.folder.type,
              })
            }
            // Skip children of collapsed folders
            if (tree.children && tree.children.length > 0 && tree.folder && collapsedFolders[tree.folder.id] === false) {
              flattenFolders(tree.children)
            }
          }
        }
        flattenFolders(accWithFolders.folders || [])
      }
    }

    return items
  }

  // Get current folder index in navigation list
  function getCurrentFolderIndex(): number {
    const navList = buildFolderNavList()

    // Check if an account header is focused
    if (focusedAccountId) {
      return navList.findIndex(item =>
        item.type === 'account-header' && item.accountId === focusedAccountId
      )
    }

    // Check selectionSource to find the correct item
    if (selectionSource === 'unified') {
      // Looking for a unified folder row (matched by folder type)
      return navList.findIndex(item =>
        item.type === 'unified-folder' && item.folderType === selectedFolderId
      )
    } else {
      // Looking for regular folder item
      return navList.findIndex(item =>
        item.type === 'folder' && item.folderId === selectedFolderId
      )
    }
  }

  // Navigate to previous folder (exposed for keyboard navigation)
  export function selectPreviousFolder() {
    const navList = buildFolderNavList()
    if (navList.length === 0) return

    const currentIndex = getCurrentFolderIndex()
    const newIndex = currentIndex <= 0 ? 0 : currentIndex - 1

    selectFolderByIndex(navList, newIndex)
  }

  // Navigate to next folder (exposed for keyboard navigation)
  export function selectNextFolder() {
    const navList = buildFolderNavList()
    if (navList.length === 0) return

    const currentIndex = getCurrentFolderIndex()
    const newIndex = currentIndex >= navList.length - 1 ? navList.length - 1 : currentIndex + 1

    selectFolderByIndex(navList, newIndex)
  }

  // Jump to the top sidebar item — Unified Inbox when shown (Alt+G)
  export function selectFirstFolder() {
    const navList = buildFolderNavList()
    if (navList.length === 0) return
    selectFolderByIndex(navList, 0)
  }

  // Jump to the last visible item: last folder of the last expanded account,
  // or the last account header when all trees are collapsed (Alt+Shift+G)
  export function selectLastFolder() {
    const navList = buildFolderNavList()
    if (navList.length === 0) return
    selectFolderByIndex(navList, navList.length - 1)
  }

  // Scroll an item into view
  function scrollItemIntoView(item: FolderNavItem) {
    if (!scrollContainer) return

    // Build selector based on item type
    let selector: string | null = null
    if (item.type === 'attention') {
      selector = '[data-sidebar-item="attention"]'
    } else if (item.type === 'unified-folder' && item.folderType) {
      selector = `[data-sidebar-item="unified-folder"][data-folder-type="${item.folderType}"]`
    } else if (item.type === 'account-header' && item.accountId) {
      selector = `[data-sidebar-item="account-header"][data-account-id="${item.accountId}"]`
    } else if (item.type === 'folder' && item.folderId) {
      selector = `[data-sidebar-item="folder"][data-folder-id="${item.folderId}"]`
    }

    if (selector) {
      const element = scrollContainer.querySelector(selector)
      element?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
    }
  }

  // Clear keyboard focus flags (moving to a real folder)
  function clearRowFocus() {
    focusedAccountId = null
  }

  // Select folder by index in nav list
  function selectFolderByIndex(navList: FolderNavItem[], index: number) {
    const item = navList[index]
    if (!item) return

    if (item.type === 'attention') {
      clearRowFocus()
      handleAttentionSelect()
    } else if (item.type === 'unified-folder' && item.folderType) {
      // Select a unified folder - merges that folder across all accounts
      clearRowFocus()
      const def = UNIFIED_FOLDER_DEFS.find((d) => d.type === item.folderType)
      onUnifiedFolderSelect?.(item.folderType, def ? $_(def.labelKey) : (item.folderType ?? ''))
    } else if (item.type === 'account-header' && item.accountId) {
      // Focus on account header (Enter opens its Inbox, Space/→/← expand/collapse)
      focusedAccountId = item.accountId
    } else if (item.type === 'folder' && item.accountId && item.folderId && item.folderPath) {
      // Select from account tree - uses handleFolderSelect
      clearRowFocus()
      handleFolderSelect(item.accountId, item.folderId, item.folderPath, item.folderName, item.folderType || 'folder')
    }

    // Scroll the selected item into view
    scrollItemIntoView(item)
  }

  // Toggle expand/collapse for the focused account (called on Space/Alt+Enter)
  export function toggleFocusedAccount() {
    if (focusedAccountId) {
      toggleAccountExpanded(focusedAccountId)
    }
  }

  // Check if an account header is focused
  export function hasFocusedAccount(): boolean {
    return focusedAccountId !== null
  }

  // Open the focused account's Inbox (called on Enter)
  export function openFocusedAccountInbox() {
    if (focusedAccountId) {
      openAccountInbox(focusedAccountId)
    }
  }

  // Expand the focused row: account header, or the selected
  // folder when it has children (called on ArrowRight)
  export function expandFocusedRow() {
    if (focusedAccountId) {
      setAccountExpandedValue(focusedAccountId, true)
      return
    }
    if (selectedFolderId && selectionSource === 'account' && folderHasChildren(selectedFolderId)) {
      setFolderCollapsedValue(selectedFolderId, false)
    }
  }

  // Collapse the focused row (called on ArrowLeft)
  export function collapseFocusedRow() {
    if (focusedAccountId) {
      setAccountExpandedValue(focusedAccountId, false)
      return
    }
    if (selectedFolderId && selectionSource === 'account' && folderHasChildren(selectedFolderId)) {
      setFolderCollapsedValue(selectedFolderId, true)
    }
  }

  // Check if the currently selected folder has children
  export function hasSelectedFolderWithChildren(): boolean {
    if (!selectedFolderId || selectionSource !== 'account') return false
    return folderHasChildren(selectedFolderId)
  }

  // Toggle collapse for the currently selected folder
  export function toggleSelectedFolderCollapse(): void {
    if (!selectedFolderId || selectionSource !== 'account') return
    if (!folderHasChildren(selectedFolderId)) return
    toggleFolderCollapsed(selectedFolderId)
  }

  // Check if a folder has children by searching the account folder trees
  function folderHasChildren(folderId: string): boolean {
    for (const acc of accountStore.accounts) {
      const found = findTreeNode(acc.folders || [], folderId)
      if (found) return (found.children && found.children.length > 0) || false
    }
    return false
  }

  // Find a FolderTree node by folder ID
  function findTreeNode(trees: folder.FolderTree[], folderId: string): folder.FolderTree | null {
    for (const tree of trees) {
      if (tree.folder?.id === folderId) return tree
      if (tree.children) {
        const found = findTreeNode(tree.children, folderId)
        if (found) return found
      }
    }
    return null
  }
</script>

<svelte:window
  onmousemove={handleDividerMouseMove}
  onmouseup={handleDividerMouseUp}
  onresize={() => measureDividerBounds()}
/>

<div class="flex flex-col h-full {isFlashing ? 'pane-focus-flash' : ''}">
  <!-- Header with Compose Button -->
  <div class="px-4 py-3 border-b border-border">
    <div class="flex items-center gap-2">
      <button
        class="flex-1 flex items-center justify-center gap-2 px-3 py-2 bg-primary text-primary-foreground rounded-md text-sm font-medium hover:bg-primary/90 transition-colors"
        onclick={onCompose}
      >
        <Icon icon="mdi:pencil" class="w-4 h-4" />
        <span>{$_('sidebar.compose')}</span>
      </button>
      {#if showBackButton}
        <button
          class="p-2 rounded-md hover:bg-muted transition-colors flex-shrink-0"
          title={$_('responsive.back')}
          aria-label={$_('aria.closeSidebar')}
          onclick={onBack}
        >
          <Icon icon="mdi:close" class="w-5 h-5 text-muted-foreground" />
        </button>
      {/if}
    </div>
  </div>

  <!-- Sidebar column: two panes split by a draggable divider. The Unified
       pane fills the space above the divider and scrolls internally; the
       Accounts pane below the divider is sized by the divider position
       (persisted accountsHeight, 0 = size to content) and scrolls
       internally when the window is too short to show everything. -->
  <div class="flex-1 min-h-0 flex flex-col overflow-hidden" bind:this={scrollContainer}>
      {#if accountStore.loading}
        <div class="flex items-center justify-center py-8">
          <Icon icon="mdi:loading" class="w-6 h-6 animate-spin text-muted-foreground" />
        </div>
      {:else if accountStore.accounts.length === 0}
        <!-- Empty State -->
        <div class="flex flex-col items-center justify-center py-8 px-4 text-center">
          <Icon icon="mdi:email-plus-outline" class="w-12 h-12 text-muted-foreground mb-3" />
          <h3 class="text-sm font-medium mb-1">{$_('sidebar.noAccountsYet')}</h3>
          <p class="text-xs text-muted-foreground mb-4">
            {$_('sidebar.addFirstAccount')}
          </p>
          <Button size="sm" onclick={openAddAccount}>
            <Icon icon="mdi:plus" class="w-4 h-4 mr-1" />
            {$_('sidebar.addAccount')}
          </Button>
        </div>
      {:else}
        <!-- Zone 1 — Unified: always at the top. The section collapses
             via its own chevron (persisted unifiedInboxExpanded). Hidden
             only with a single account when the
             sidebar_hide_unified_single_account setting is on. -->
        {#if showUnified}
          <!-- Zone 1 — Unified: fills the space above the divider. -->
          <div
            class="flex-1 min-h-0 overflow-y-auto scrollbar-thin"
            onscroll={(e) => handlePaneScroll(e, 'unified')}
          >
            <div class="py-2" bind:this={unifiedContent}>
              <UnifiedSection
                {attentionCount}
                unreadCounts={unifiedUnreadCounts}
                {outboxCount}
                folderOrder={unifiedFolderOrder}
                selectedFolderType={selectionSource === 'unified' ? selectedFolderId : null}
                {selectionSource}
                onSelectAttention={handleAttentionSelect}
                onSelectUnifiedFolder={handleUnifiedFolderSelect}
                onSelectUnifiedInbox={handleUnifiedInboxSelect}
                onReorderFolders={handleUnifiedFolderReorder}
              />
            </div>
          </div>

          <!-- Divider: draggable up/down; the Accounts block moves with
               it. It moves freely past both section boundaries, but snaps
               at each one — the drag sticks at the boundary until the
               pointer pushes a little further (the divider highlights
               while snapped). Past a detent the clipped section scrolls
               internally with a visible scrollbar. A focusable separator
               with value attributes is the splitter pattern, and
               ArrowUp/ArrowDown move it. svelte-ignore: the checker does
               not treat role=separator as interactive, but this element
               has mouse + keyboard handlers by design. -->
          <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
          <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
          <div
            class="flex-shrink-0 relative cursor-row-resize group select-none"
            style="height: {DIVIDER_HEIGHT}px"
            role="separator"
            aria-orientation="horizontal"
            aria-label={$_('sidebar.dividerLabel')}
            aria-valuenow={Math.round(accountsHeightPx)}
            aria-valuemin={Math.round(accountsMin)}
            aria-valuemax={Math.round(accountsMax)}
            aria-valuetext={dividerSnapped ? dividerSnapLabel : undefined}
            tabindex="0"
            onmousedown={startResizeDivider}
            onkeydown={handleDividerKeyDown}
          >
            <div class="absolute left-0 right-0 top-1/2 -translate-y-1/2 h-px bg-border group-hover:bg-primary/50 {dividerDragging || dividerSnapped ? 'bg-primary/50' : ''}"></div>
            <div class="absolute left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2 w-8 h-1 rounded-full bg-border group-hover:bg-primary/70 {dividerDragging || dividerSnapped ? 'bg-primary/70' : ''}"></div>
          </div>
        {/if}

        <!-- Zone 2 — Accounts: docked at the bottom of the column, in
             normal order (first account on top, second below it). -->
        <div
          class="shrink-0 overflow-y-auto scrollbar-thin {showUnified ? '' : 'flex-1 min-h-0'}"
          style={showUnified && accountsHeightPx > 0 ? `height: ${accountsHeightPx}px` : ''}
          onscroll={(e) => handlePaneScroll(e, 'accounts')}
        >
          <div class="pt-1" bind:this={accountsContent}>
          <div class="px-4 pb-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
            {$_('sidebar.accounts')}
          </div>

          {#each accountStore.accounts as accWithFolders (accWithFolders.account.id)}
            <AccountSection
              account={accWithFolders.account}
              folders={accWithFolders.folders}
              loading={accWithFolders.loading}
              syncing={accWithFolders.syncing}
              error={accWithFolders.error}
              selectedFolderId={accountStore.selectedFolder?.folderId ?? ''}
              {selectionSource}
              isHeaderFocused={focusedAccountId === accWithFolders.account.id}
              isExpanded={expandedAccounts[accWithFolders.account.id] ?? false}
              syncError={accountStore.getSyncError(accWithFolders.account.id)}
              inboxUnreadCount={accountInboxUnread(accWithFolders.account.id)}
              containsSelection={accountContainsSelection(accWithFolders.account.id)}
              {collapsedFolders}
              {onMessagesMoved}
              onFolderSelect={handleFolderSelect}
              onToggleExpanded={() => toggleAccountExpanded(accWithFolders.account.id)}
              onSelectInbox={() => openAccountInbox(accWithFolders.account.id)}
              onEdit={() => openEditAccount(accWithFolders.account)}
              onDelete={() => openDeleteAccount(accWithFolders.account)}
              onSync={() => {
                // Clear any sync error before retrying
                accountStore.clearSyncError(accWithFolders.account.id)
                accountStore.syncAccount(accWithFolders.account.id)
              }}
            />
          {/each}

          <!-- Add Account Button -->
          <div class="px-3 py-2">
            <button
              class="w-full flex items-center gap-2 px-3 py-2 text-sm text-muted-foreground hover:text-foreground hover:bg-muted/50 rounded-md transition-colors"
              onclick={openAddAccount}
            >
              <Icon icon="mdi:plus" class="w-4 h-4" />
              <span>{$_('sidebar.addAccount')}</span>
            </button>
          </div>
        </div>
      </div>
    {/if}
  </div>

  <!-- Footer: Sync Status + Settings. Chrome (padding/border/min-height)
       lives in kit `SidebarFooter` so mail, calendar, and contacts all
       render the same strip height. Mail's progress bar overlay + 2-line
       leading content (account name + status label) pass through
       SidebarFooter's overlay/leading snippets unchanged. -->
  <SidebarFooter>
    {#snippet overlay()}
      {#if syncStatus.percentage !== null}
        <div class="absolute top-0 left-0 right-0 h-1 bg-muted overflow-hidden">
          <div
            class="h-full bg-primary transition-all duration-300 ease-out"
            style="width: {syncStatus.percentage}%"
          ></div>
        </div>
      {/if}
    {/snippet}
    {#snippet leading()}
      <button
        class="flex-1 min-w-0 flex items-end gap-2 hover:text-foreground transition-colors text-left"
        onclick={accountStore.isAnySyncing ? cancelSync : syncAllAccounts}
        title={$_(accountStore.isAnySyncing ? 'sidebar.clickToCancel' : 'sidebar.syncAllAccounts')}
      >
        <Icon
          icon="mdi:sync"
          class="w-4 h-4 flex-shrink-0 {accountStore.isAnySyncing ? 'animate-spin' : ''}"
        />
        <div class="flex-1 min-w-0">
          {#if syncStatus.accountName}
            <div class="truncate text-foreground font-medium leading-tight mb-0.5">{syncStatus.accountName}</div>
          {/if}
          <span class="block leading-tight">{syncStatus.label}</span>
        </div>
      </button>
    {/snippet}
    {#snippet trailing()}
      <button
        class="p-1 hover:text-foreground hover:bg-muted rounded transition-colors relative"
        onclick={() => showSettingsDialog = true}
        title={$_('sidebar.settings')}
      >
        <Icon icon="mdi:cog" class="w-4 h-4" />
        {#if contactSourcesStore.hasErrors}
          <span class="absolute -top-0.5 -right-0.5 w-2.5 h-2.5 bg-destructive rounded-full border border-background"></span>
        {/if}
      </button>
    {/snippet}
  </SidebarFooter>
</div>

<!-- Account Dialog -->
<AccountDialog
  bind:open={showAccountDialog}
  editAccount={editingAccount}
  onClose={() => {
    showAccountDialog = false
    editingAccount = null
    setFocusedPane('messageList')
  }}
/>

<!-- Delete Confirmation Dialog -->
<DeleteAccountDialog
  bind:open={showDeleteDialog}
  account={deletingAccount}
  onClose={() => {
    showDeleteDialog = false
    deletingAccount = null
    setFocusedPane('messageList')
  }}
/>

<!-- Settings Dialog -->
<SettingsDialog
  bind:open={showSettingsDialog}
  onClose={() => {
    showSettingsDialog = false
    setFocusedPane('messageList')
  }}
/>
