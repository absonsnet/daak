<script lang="ts">
  // Load offline icon data before anything else
  import './lib/iconify-offline'

  import { onMount, untrack } from 'svelte'
  import Icon from '@iconify/svelte'
  import TitleBar from './lib/components/common/TitleBar.svelte'
  import Sidebar from './lib/components/sidebar/Sidebar.svelte'
  import MessageList from './lib/components/list/MessageList.svelte'
  import ConversationViewer from './lib/components/viewer/ConversationViewer.svelte'
  import ViewSwitcher from './lib/components/viewer/ViewSwitcher.svelte'
  import Composer from './lib/components/composer/Composer.svelte'
  import ToastContainer from './lib/components/ui/toast/ToastContainer.svelte'
  import UndoSendToast from './lib/components/ui/toast/UndoSendToast.svelte'
  import SentSnackbar from './lib/components/ui/toast/SentSnackbar.svelte'
  import SpellSuggestionMenu from './lib/spellcheck/SpellSuggestionMenu.svelte'
  import TermsDialog from './lib/components/TermsDialog.svelte'
  import OAuthMissingDialog from './lib/components/OAuthMissingDialog.svelte'
  import WhatsNewDialog from './lib/components/WhatsNewDialog.svelte'
  import CertificateDialog from './lib/components/settings/CertificateDialog.svelte'
  import ExtensionSettingsDialog from './lib/components/settings/ExtensionSettingsDialog.svelte'
  import ExtensionRail from './lib/components/rail/ExtensionRail.svelte'
  import ContactsPane from '$extensions/contacts/frontend/components/ContactsPane.svelte'
  import CalendarPane from '$extensions/calendar/frontend/components/CalendarPane.svelte'
  import MailMergeQueuePane from '$extensions/mail-merge/frontend/components/MailMergeQueuePane.svelte'
  import { refreshExtensionRegistry, getRailTabs } from '$lib/stores/extensionRegistry.svelte'
  import { KEY } from '$lib/keyboard/shortcuts'
  import * as AlertDialog from '$lib/components/ui/alert-dialog'
  import { accountStore } from '$lib/stores/accounts.svelte'
  import { addToast } from '$lib/stores/toast'
  import { startUndoSendCountdown, removeUndoSendCountdown, type UndoSendCountdown } from '$lib/stores/undoSend'
  import { loadSettings, getThemeMode, getShowTitleBar, getNativeTitleBar, getComposerMode, getMailtoMode, getShowMessagePreviewPane, setShowMessagePreviewPane, getMessageOpenMode, type MessageOpenMode } from '$lib/stores/settings.svelte'
  import { loadWindowControlsLayout } from '$lib/stores/windowControls.svelte'
  import { loadImageAllowlist } from '$lib/stores/imageAllowlist.svelte'
  import { initTheme, applyThemeFromMode, handleSystemThemeEvent, handleMediaQueryChange } from '$lib/stores/theme.svelte'
  import { loadUIState, saveUIState, paneConstraints, getActiveExtension, setActiveExtension } from '$lib/stores/uiState.svelte'
  import { setPendingDeepLink } from '$lib/stores/extensionDeepLink.svelte'
  import {
    type FocusablePane,
    getFocusedPane,
    setFocusedPane,
    focusPreviousPane,
    focusNextPane,
    isPaneFlashing,
    isInputElement,
    setComposerOpen,
    getPaneNav
  } from '$lib/stores/keyboard.svelte'
  import { isDialogGuardActive } from '$lib/stores/dialogGuard'
  import { dispatchExtensionShortcut } from '$lib/stores/extensionShortcuts.svelte'
  import { initLayout, getLayoutMode, getResponsiveView, showViewer, hideViewer, showSidebar, hideSidebar, isResponsive } from '$lib/stores/layout.svelte'
  // @ts-ignore - wailsjs path
  import { PrepareReply, GetPendingMailto, GetDraft, MarkAsRead, MarkAsUnread, Star, Unstar, Archive, MarkAsSpam, MarkAsNotSpam, Undo, GetTermsAccepted, SetTermsAccepted, RefreshWindowConstraints, AcceptCertificate, GetStartHiddenActive, CloseWindow, QuitApp, OpenComposerWindow, OpenViewerWindow, GetSystemTheme, NotifyStartupComplete, GetOAuthBuildStatus, GetOAuthWarningDisabled, SetOAuthWarningDisabled, GetLastSeenVersion, SetLastSeenVersion, GetAppInfo, SetShowMessagePreviewPane, GetConversation, GetSpecialFolder, UndoSend } from '../wailsjs/go/app/App.js'
  // @ts-ignore - wailsjs path
  import { smtp, folder, certificate } from '../wailsjs/go/models'
  // @ts-ignore - wailsjs runtime
  import { WindowShow, WindowHide, EventsOn } from '../wailsjs/runtime/runtime'
  import { _ } from '$lib/i18n'
  import { logger } from '$lib/logger' // TEMP: keypress-gate audit — remove in the fix commit

  // Component refs for keyboard navigation. Plain `let` (not $state) is
  // intentional: svelte-check warns "Changing its value will not correctly
  // trigger updates" but nothing here actually reads these refs in a reactive
  // context — they're only used inside event handlers. Making them $state
  // added bookkeeping cost (visible in idle-CPU profiling) without any benefit.
  let sidebarRef: Sidebar | null = null
  let messageListRef: MessageList | null = null
  let viewerRef: ConversationViewer | null = null
  let messageListContainerRef: HTMLElement | null = null

  // React to theme mode changes from settings store
  $effect(() => {
    const mode = getThemeMode()
    applyThemeFromMode(mode)
  })

  // Selected folder state
  let selectedAccountId = $state<string | null>(null)
  let selectedFolderId = $state<string | null>(null)
  let selectedFolderName = $state('Inbox')
  let selectedFolderType = $state<string | null>(null)
  // Track where the selection came from: 'unified' for unified section, 'account' for account tree
  let selectionSource = $state<'unified' | 'account' | null>(null)
  // True once onMount has restored the persisted folder selection (or
  // found none). Gates the first-run default below.
  let uiStateRestored = $state(false)
  // First-run default selection runs at most once per launch.
  let didDefaultFolderSelect = false

  // Selected conversation state
  let selectedThreadId = $state<string | null>(null)
  let selectedConversationFolderId = $state<string | null>(null)
  let selectedConversationAccountId = $state<string | null>(null)

  // Composer state
  let showComposer = $state(false)
  let composerAccountId = $state<string | null>(null)
  let composerInitialMessage = $state<smtp.ComposeMessage | null>(null)
  let composerDraftId = $state<string | null>(null)
  let composerImagesLoaded = $state(false)

  // Mirror composer visibility into the keyboard store so the viewer can
  // suppress its Delete/Backspace shortcut during the composer's mount→focus race.
  $effect(() => {
    setComposerOpen(showComposer)
  })

  // Message tabs — active when the preview pane is off in full layout
  // mode. The folder tab (activeMessageTabId === null) shows the message
  // list; each message tab shows one ConversationViewer. Only one viewer
  // is ever mounted: the active tab's. The list stays mounted across tab
  // switches (hidden with display:none) so scroll/selection survive.
  interface MessageTab {
    threadId: string
    folderId: string
    accountId: string
    subject: string
  }
  let messageTabs = $state<MessageTab[]>([])
  let activeMessageTabId = $state<string | null>(null)
  const tabMode = $derived(getLayoutMode() === 'full' && !getShowMessagePreviewPane())
  const activeMessageTab = $derived(messageTabs.find(t => t.threadId === activeMessageTabId) ?? null)

  // Dialog mode: at most one message open in the modal. The list stays
  // mounted behind it, so closing returns to the folder/list view with
  // scroll and selection intact.
  let dialogMessage = $state<MessageTab | null>(null)
  let dialogViewerRef: ConversationViewer | null = null

  function closeAllMessageTabs() {
    messageTabs = []
    activeMessageTabId = null
    dialogMessage = null
  }

  function openMessageTab(threadId: string, folderId: string, accountId: string, subject?: string) {
    const label = subject?.trim()
      ? subject
      : messageListRef?.getConversationSubject(threadId) || $_('viewer.noSubject')
    selectedThreadId = threadId
    selectedConversationFolderId = folderId
    selectedConversationAccountId = accountId
    const existing = messageTabs.find(t => t.threadId === threadId)
    if (existing) {
      if (existing.subject !== label) {
        messageTabs = messageTabs.map(t => (t.threadId === threadId ? { ...t, subject: label } : t))
      }
    } else {
      messageTabs = [...messageTabs, { threadId, folderId, accountId, subject: label }]
    }
    activeMessageTabId = threadId

    // Persist state
    saveUIState({
      selectedThreadId: threadId,
      selectedConversationAccountId: accountId,
      selectedConversationFolderId: folderId,
    })
  }

  function closeMessageTab(threadId: string) {
    messageTabs = messageTabs.filter(t => t.threadId !== threadId)
    if (activeMessageTabId === threadId) {
      // Closing a tab returns to the folder tab (list stays as it was).
      activeMessageTabId = null
    }
  }

  function activateMessageTab(threadId: string | null) {
    activeMessageTabId = threadId
    if (threadId) {
      const tab = messageTabs.find(t => t.threadId === threadId)
      if (tab) {
        selectedThreadId = threadId
        selectedConversationFolderId = tab.folderId
        selectedConversationAccountId = tab.accountId
        messageListRef?.selectThread(threadId)
      }
    }
  }

  // Single dispatch point for "open message". The open mode setting
  // decides where the message opens: a tab, a large modal dialog, or a
  // separate popup window. At most one open instance per message — opening
  // in a new mode closes the old one.
  function openMessage(threadId: string, folderId: string, accountId: string, subject?: string) {
    const mode = getMessageOpenMode()
    if (mode === 'dialog') {
      openMessageDialog(threadId, folderId, accountId, subject)
      return
    }
    if (mode === 'popup') {
      closeMessageTab(threadId)
      if (dialogMessage?.threadId === threadId) dialogMessage = null
      OpenViewerWindow(accountId, folderId, threadId)
      return
    }
    openMessageTab(threadId, folderId, accountId, subject)
  }

  function resolveMessageLabel(threadId: string, subject?: string): string {
    return subject?.trim()
      ? subject
      : messageListRef?.getConversationSubject(threadId) || $_('viewer.noSubject')
  }

  function openMessageDialog(threadId: string, folderId: string, accountId: string, subject?: string) {
    closeMessageTab(threadId)
    selectedThreadId = threadId
    selectedConversationFolderId = folderId
    selectedConversationAccountId = accountId
    dialogMessage = { threadId, folderId, accountId, subject: resolveMessageLabel(threadId, subject) }
    saveUIState({
      selectedThreadId: threadId,
      selectedConversationAccountId: accountId,
      selectedConversationFolderId: folderId,
    })
  }

  function closeMessageDialog() {
    dialogMessage = null
  }

  // A deleted message must close every open view showing it — including
  // INACTIVE tabs. A tab's viewer only mounts while the tab is active, so
  // the viewer itself cannot hear a delete issued elsewhere; the app
  // listens for the delete/move events here and closes any tab/dialog
  // whose conversation is gone. Archive, spam and other moves never close
  // a view — only a move to Trash (or a permanent delete) does.
  async function conversationIsGone(
    threadId: string,
    folderId: string,
    accountId: string,
    deletedIds: Set<string>,
    destFolderId: string | null
  ): Promise<boolean> {
    if (destFolderId !== null) {
      // A move only counts as a delete when the destination is Trash.
      try {
        const trashFolder = await GetSpecialFolder(accountId, 'trash')
        if (!trashFolder?.id || destFolderId !== trashFolder.id) return false
      } catch (err) {
        console.error('Failed to resolve Trash folder for delete check:', err)
        return false
      }
    }
    try {
      const conv = await GetConversation(threadId, folderId)
      const remaining = (conv?.messages ?? []).filter(m => !deletedIds.has(m.id))
      return remaining.length === 0
    } catch {
      // Conversation no longer loadable — gone.
      return true
    }
  }

  async function closeViewsForDeletedMessages(messageIds: string[], destFolderId: string | null) {
    if (messageIds.length === 0) return
    const deletedIds = new Set(messageIds)
    for (const tab of [...messageTabs]) {
      if (await conversationIsGone(tab.threadId, tab.folderId, tab.accountId, deletedIds, destFolderId)) {
        closeMessageTab(tab.threadId)
      }
    }
    if (
      dialogMessage &&
      (await conversationIsGone(dialogMessage.threadId, dialogMessage.folderId, dialogMessage.accountId, deletedIds, destFolderId))
    ) {
      closeMessageDialog()
    }
  }

  function moveDialogMessageTo(mode: MessageOpenMode) {
    if (!dialogMessage) return
    moveMessageTo(mode, dialogMessage)
  }

  function moveActiveTabTo(mode: MessageOpenMode) {
    if (!activeMessageTab) return
    moveMessageTo(mode, activeMessageTab)
  }

  // View switcher: move an already-open message to another mode. Moving
  // to tab opens/activates its tab and closes the dialog/popup; moving to
  // dialog/popup closes its tab. The popup case is handled by the popup
  // itself via SwitchView + the viewer:switch IPC event below.
  function moveMessageTo(mode: MessageOpenMode, tab: MessageTab) {
    if (mode === 'tab') {
      closeMessageDialog()
      openMessageTab(tab.threadId, tab.folderId, tab.accountId, tab.subject)
      return
    }
    if (mode === 'dialog') {
      closeMessageTab(tab.threadId)
      openMessageDialog(tab.threadId, tab.folderId, tab.accountId, tab.subject)
      return
    }
    closeMessageTab(tab.threadId)
    closeMessageDialog()
    OpenViewerWindow(tab.accountId, tab.folderId, tab.threadId)
  }

  // Turning the preview pane back on closes message tabs and restores the
  // classic viewer with the active tab's thread selected, if any.
  let tabModeBefore = false
  $effect(() => {
    const tm = tabMode
    if (!tabModeBefore && tm) {
      // Entering tab mode: focus mode belongs to the classic viewer.
      focusMode = 'off'
      focusedMessageIdInFocus = null
    }
    if (tabModeBefore && !tm) {
      const active = messageTabs.find(t => t.threadId === activeMessageTabId)
      closeAllMessageTabs()
      if (active) {
        selectedThreadId = active.threadId
        selectedConversationFolderId = active.folderId
        selectedConversationAccountId = active.accountId
      }
    }
    tabModeBefore = tm
  })

  // Focus mode state — viewer (or single message) takes the whole window.
  // Always resets on conversation change, on Esc, on back-arrow, and on app reload.
  let focusMode = $state<'off' | 'thread' | 'message'>('off')
  let focusedMessageIdInFocus = $state<string | null>(null)
  const viewerIsOverlay = $derived(isResponsive() || focusMode !== 'off')
  const viewerIsVisible = $derived(getResponsiveView() === 'viewer' || focusMode !== 'off')

  function toggleThreadFocus() {
    if (focusMode === 'thread') {
      focusMode = 'off'
      focusedMessageIdInFocus = null
      return
    }
    focusMode = 'thread'
    focusedMessageIdInFocus = null
  }

  function toggleMessageFocus(messageId: string) {
    if (focusMode === 'message' && focusedMessageIdInFocus === messageId) {
      focusMode = 'off'
      focusedMessageIdInFocus = null
      return
    }
    focusMode = 'message'
    focusedMessageIdInFocus = messageId
  }

  // Auto-reset focus mode when the conversation changes (or is closed).
  // Prevents focus state from leaking across navigation.
  $effect(() => {
    void selectedThreadId
    focusMode = 'off'
    focusedMessageIdInFocus = null
  })

  // Route keyboard pane focus to the viewer while in focus mode so j/k/arrow
  // shortcuts scroll the viewer, then restore the prior pane on exit.
  let focusedPaneBeforeFocusMode: FocusablePane | null = null
  $effect(() => {
    const mode = focusMode
    if (mode !== 'off' && focusedPaneBeforeFocusMode === null) {
      focusedPaneBeforeFocusMode = untrack(() => getFocusedPane())
      setFocusedPane('viewer')
      return
    }
    if (mode === 'off' && focusedPaneBeforeFocusMode !== null) {
      setFocusedPane(focusedPaneBeforeFocusMode)
      focusedPaneBeforeFocusMode = null
    }
  })

  // Shutdown state
  let isShuttingDown = $state(false)

  // Terms acceptance state
  let showTermsDialog = $state(false)

  // Launch-time OAuth credentials warning state
  let showOAuthMissingDialog = $state(false)
  let oauthBuildStatus = $state({ google: true, microsoft: true })
  let pendingOAuthWarning = $state(false)

  // What's New (per-version) dialog state
  let showWhatsNewDialog = $state(false)
  let whatsNewVersion = $state('')
  let pendingWhatsNew = $state(false)

  // Certificate TOFU state (for background sync cert errors)
  let showCertDialog = $state(false)
  let pendingCertificate = $state<certificate.CertificateInfo | null>(null)
  let pendingCertAccountId = $state<string | null>(null)

  // Flatpak filesystem permission dialog state
  let showFlatpakFsDialog = $state(false)
  let flatpakFsDialogContext = $state<'open' | 'saveAll'>('open')

  // Handle window close button (title bar X) — hides if background mode, quits if not
  function handleClose() {
    CloseWindow()
  }

  // Handle forced quit (Ctrl+Q) — always quits regardless of background mode
  function handleQuit() {
    isShuttingDown = true
    setTimeout(() => QuitApp(), 100)
  }

  // Handle terms acceptance
  async function handleTermsAccepted() {
    try {
      await SetTermsAccepted(true)
      showTermsDialog = false
    } catch (err) {
      console.error('Failed to save terms acceptance:', err)
    }
  }

  // OAuth warning dismiss — optionally persists the opt-out so the warning
  // stops firing on future launches even when credentials remain missing.
  async function dismissOAuthWarning(dontShowAgain: boolean) {
    if (dontShowAgain) {
      try {
        await SetOAuthWarningDisabled(true)
      } catch (err) {
        console.error('Failed to persist OAuth warning preference:', err)
      }
    }
    showOAuthMissingDialog = false
  }

  // What's New acknowledgement — records the current version as seen.
  // Called ONLY on explicit OK click; ESC/outside-click leaves the version
  // unrecorded so the dialog fires again on next launch.
  async function acknowledgeWhatsNew() {
    try {
      await SetLastSeenVersion(whatsNewVersion)
    } catch (err) {
      console.error('Failed to persist last-seen version:', err)
    }
    showWhatsNewDialog = false
  }

  // Reactive sequencing: Terms → OAuth warning → What's New.
  // Each gates on the previous being closed so users see them one at a
  // time, never stacked.
  $effect(() => {
    if (!showTermsDialog && pendingOAuthWarning && !showOAuthMissingDialog) {
      showOAuthMissingDialog = true
      pendingOAuthWarning = false
    }
  })

  $effect(() => {
    if (!showTermsDialog && !showOAuthMissingDialog && pendingWhatsNew && !showWhatsNewDialog) {
      showWhatsNewDialog = true
      pendingWhatsNew = false
    }
  })

  // Certificate TOFU handlers for background sync
  async function handleBgCertAcceptOnce() {
    if (!pendingCertificate || !pendingCertAccountId) return
    try {
      // Look up the account's IMAP host for the accept call
      const acc = accountStore.accounts.find(a => a.account.id === pendingCertAccountId)
      const host = acc?.account.imapHost || ''
      await AcceptCertificate(host, pendingCertificate, false)
    } catch (err) {
      console.error('Failed to accept certificate:', err)
    }
    showCertDialog = false
    pendingCertificate = null
    pendingCertAccountId = null
  }

  async function handleBgCertAcceptPermanently() {
    if (!pendingCertificate || !pendingCertAccountId) return
    try {
      const acc = accountStore.accounts.find(a => a.account.id === pendingCertAccountId)
      const host = acc?.account.imapHost || ''
      await AcceptCertificate(host, pendingCertificate, true)
    } catch (err) {
      console.error('Failed to accept certificate:', err)
    }
    showCertDialog = false
    pendingCertificate = null
    pendingCertAccountId = null
  }

  function handleBgCertDecline() {
    showCertDialog = false
    pendingCertificate = null
    pendingCertAccountId = null
  }

  // Helper to find folder info by ID from account store
  function findFolderById(accountId: string, folderId: string): { name: string; type: string; path: string } | null {
    const acc = accountStore.accounts.find(a => a.account.id === accountId)
    if (!acc) return null

    function searchTree(trees: folder.FolderTree[]): { name: string; type: string; path: string } | null {
      for (const tree of trees) {
        if (tree.folder?.id === folderId) {
          return { name: tree.folder.name, type: tree.folder.type, path: tree.folder.path }
        }
        if (tree.children) {
          const found = searchTree(tree.children)
          if (found) return found
        }
      }
      return null
    }

    // Check if folders are loaded before searching
    if (!acc.folders || acc.folders.length === 0) return null
    return searchTree(acc.folders)
  }

  onMount(async () => {
    // Listen for notification click events from backend
    EventsOn('notification:clicked', (data: { accountId: string; folderId: string; threadId: string }) => {
      // Find folder info for display
      const folderInfo = findFolderById(data.accountId, data.folderId)

      // Switch the rail back to mail in case the user was on an extension
      // tab when the notification fired — without this the message-list
      // state below would update but stay hidden behind the extension pane.
      setActiveExtension('mail')

      // Navigate to the folder (use 'unified' source to highlight under Unified Inbox)
      selectedAccountId = data.accountId
      selectedFolderId = data.folderId
      selectedFolderName = folderInfo?.name || 'Inbox'
      selectedFolderType = folderInfo?.type || 'inbox'
      selectionSource = 'unified'

      // Close tabs from the previous folder, then open the conversation
      // through the single open dispatch (tab in tab mode, viewer otherwise).
      closeAllMessageTabs()
      handleConversationSelect(data.threadId, data.folderId, data.accountId)

      // Highlight the thread in the message list. The list keeps the request
      // pending until its folder load lands, so no timing assumption here.
      messageListRef?.selectThread(data.threadId)

      // Persist state
      saveUIState({
        selectedAccountId: data.accountId,
        selectedFolderId: data.folderId,
        selectedFolderName: folderInfo?.name || 'Inbox',
        selectedFolderType: folderInfo?.type || 'inbox',
        selectedThreadId: data.threadId,
        selectedConversationAccountId: data.accountId,
        selectedConversationFolderId: data.folderId,
      })
    })

    // Generic extension-routed notification clicks. The host switches the
    // rail tab here AND stashes the path in a pending-deep-link buffer.
    // The target extension's pane drains the buffer on mount (it isn't
    // mounted yet at the moment we set the tab). Extension-specific path
    // parsing lives in each extension's own pane component.
    EventsOn('extension:open', (data: { extensionId: string; path: string }) => {
      if (!data.extensionId) return
      if (data.path) setPendingDeepLink(data.extensionId, data.path)
      setActiveExtension(data.extensionId)
    })

    // Listen for window show requests (from single-instance activation, notification clicks)
    EventsOn('window:show', () => {
      window.focus()
    })

    // Tray menu: open the composer (the Go side shows the window first).
    // handleCompose resolves the account and honours the detached
    // composer mode, exactly like the Compose button does.
    EventsOn('tray:compose', () => {
      handleCompose()
    })

    // Listen for shutdown event from backend (triggered by OS close signal)
    EventsOn('app:shutting-down', () => {
      isShuttingDown = true
    })

    // Listen for untrusted certificate events from background sync
    EventsOn('certificate:untrusted', (data: { accountId: string; certificate: certificate.CertificateInfo }) => {
      // Only show if not already showing a cert dialog
      if (!showCertDialog) {
        pendingCertificate = data.certificate
        pendingCertAccountId = data.accountId
        showCertDialog = true
      }
    })

    // Listen for Flatpak filesystem permission dialog event. Optional payload
    // picks the wording: 'saveAll' (bulk attachment save, #384) vs the
    // original toast-open case (no payload).
    EventsOn('flatpak:filesystem-dialog', (context?: string) => {
      flatpakFsDialogContext = context === 'saveAll' ? 'saveAll' : 'open'
      showFlatpakFsDialog = true
    })

    // Listen for external mailto from second instance (routed through backend)
    EventsOn('mailto:external', (data: MailtoData) => {
      handleMailtoData(data)
    })

    // Toast confirmation when a detached composer sends a message
    EventsOn('composer:messageSent', () => {
      addToast({
        type: 'success',
        message: $_('composer.messageSent'),
      })
    })

    // Undo Send: a detached composer queued a message — start the
    // countdown toast here (its window closes right away).
    EventsOn('sendqueue:queued', (data: { queueId: string, accountId: string, draftId: string, subject: string, recipient: string, delaySeconds: number }) => {
      startUndoSendCountdown({
        queueId: data.queueId,
        accountId: data.accountId,
        draftId: data.draftId,
        subject: data.subject ?? '',
        recipient: data.recipient ?? '',
        delaySeconds: data.delaySeconds ?? 5,
      })
    })

    // Undo Send: the worker dispatched a queued message — slim sent
    // snackbar with View message (opens unified Sent).
    EventsOn('sendqueue:sent', (data: { queueId: string, accountId: string, subject: string }) => {
      removeUndoSendCountdown(data.queueId)
      showSentSnackbar(data.subject ?? '')
    })

    // A viewer popup's switcher asks the main window to open its thread
    // in another mode; the popup closes itself after sending.
    EventsOn('viewer:switch', (data: { mode: string; accountId: string; folderId: string; threadId: string }) => {
      if (data.mode === 'tab') {
        openMessageTab(data.threadId, data.folderId, data.accountId)
      } else if (data.mode === 'dialog') {
        openMessageDialog(data.threadId, data.folderId, data.accountId)
      }
    })

    // Deleting a message closes every open view of it — tabs (including
    // inactive ones, whose viewers are not mounted) and the dialog. A
    // move only closes when the destination is Trash; archive, spam and
    // other moves leave the view open. See closeViewsForDeletedMessages.
    EventsOn('messages:deleted', (messageIds: string[]) => {
      closeViewsForDeletedMessages(messageIds, null)
    })
    EventsOn('messages:moved', (data: { messageIds: string[]; destFolderId: string }) => {
      closeViewsForDeletedMessages(data.messageIds, data.destFolderId)
    })

    // Listen for escape-iframe-focus event (from EmailBody when navigating away from iframe)
    const handleEscapeIframeFocus = () => {
      // Focus the message list container to take keyboard focus away from iframe
      messageListContainerRef?.focus()
    }
    window.addEventListener('escape-iframe-focus', handleEscapeIframeFocus)

    // Load application settings (including theme mode) and apply theme
    const storedThemeMode = await loadSettings()
    await initTheme(storedThemeMode, GetSystemTheme)

    // Detect the window-controls layout (side, order, style) once at
    // startup; the title bar reads it reactively from the store.
    await loadWindowControlsLayout()

    // Load image allowlist cache for synchronous checks in EmailBody
    loadImageAllowlist()

    // Check if terms have been accepted
    try {
      const termsAccepted = await GetTermsAccepted()
      if (!termsAccepted) {
        showTermsDialog = true
      }
    } catch (err) {
      console.error('Failed to check terms acceptance:', err)
      // Show dialog on error to be safe
      showTermsDialog = true
    }

    // OAuth credentials warning: surface missing provider creds on every
    // launch unless the user has explicitly opted out. The dialog shows
    // both providers with a missing/present indicator, so the
    // trigger fires when ANY of them is missing. Actual opening is
    // deferred via $effect so Terms can resolve first.
    try {
      const status = await GetOAuthBuildStatus()
      const anyMissing = !status.google || !status.microsoft
      if (anyMissing) {
        const disabled = await GetOAuthWarningDisabled()
        if (!disabled) {
          oauthBuildStatus = status
          pendingOAuthWarning = true
        }
      }
    } catch (err) {
      console.error('Failed to check OAuth build status:', err)
    }

    // What's New: fire whenever the stored last-seen version differs from
    // the current build's version — including the empty-string case so
    // existing users upgrading to v0.3.0 (or anyone whose DB predates the
    // last_seen_version key) see the release announcement on first launch.
    // The version isn't recorded until the user explicitly clicks OK in
    // the dialog (see acknowledgeWhatsNew); ESC/outside-click leaves it
    // unrecorded so the dialog fires again next launch.
    try {
      const appInfo = await GetAppInfo()
      const lastSeen = await GetLastSeenVersion()
      if (lastSeen !== appInfo.version) {
        whatsNewVersion = appInfo.version
        pendingWhatsNew = true
      }
    } catch (err) {
      console.error('Failed to check What\'s New state:', err)
    }

    // Load persisted UI state
    const uiState = await loadUIState()

    // Load extension registry (enabled extensions, rail tabs) so the rail can
    // render synchronously when the layout mounts.
    await refreshExtensionRegistry()

    // Restore pane widths (already validated/clamped by loadUIState)
    sidebarWidth = uiState.sidebarWidth
    listWidth = uiState.listWidth

    // Restore folder selection if valid
    if (uiState.selectedAccountId && uiState.selectedFolderId) {
      // Validate account still exists (unless unified inbox)
      const isUnified = uiState.selectedAccountId === 'unified'
      const accountExists = isUnified || accountStore.accounts.some(
        a => a.account.id === uiState.selectedAccountId
      )

      if (accountExists) {
        selectedAccountId = uiState.selectedAccountId
        selectedFolderId = uiState.selectedFolderId
        selectedFolderName = uiState.selectedFolderName || 'Inbox'
        selectedFolderType = uiState.selectedFolderType

        // Restore conversation selection
        if (uiState.selectedThreadId) {
          selectedThreadId = uiState.selectedThreadId
          selectedConversationAccountId = uiState.selectedConversationAccountId
          selectedConversationFolderId = uiState.selectedConversationFolderId
        }
      }
    }
    uiStateRestored = true

    // Listen for system theme changes from backend (XDG Settings Portal)
    EventsOn('theme:system-preference', (newTheme: string) => {
      handleSystemThemeEvent(newTheme)
    })

    // Listen for system theme changes via matchMedia (fallback when portal unavailable)
    const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)')
    mediaQuery.addEventListener('change', (e) => {
      handleMediaQueryChange(e.matches)
    })

    // App is fully initialized — dismiss the inline boot splash from
    // index.html. The CSS transition fades it out; we remove the element
    // shortly after to free the DOM. Done BEFORE the start-hidden check
    // so users in background mode don't see the splash linger on screen
    // before the window hides.
    const splash = document.getElementById('boot-splash')
    if (splash) {
      splash.hidden = true
      setTimeout(() => splash.remove(), 250)
    }

    // main.ts called WindowShow() at module load so the splash was visible
    // during slow startup work (migrations etc.). If the user has start-
    // hidden background mode, undo that now. Otherwise the window is already
    // visible — calling WindowShow again is harmless.
    const shouldStartHidden = await GetStartHiddenActive()
    if (shouldStartHidden) {
      WindowHide()
    }
    if (!shouldStartHidden) {
      WindowShow()
    }

    // Clear the desktop-environment startup indicator. Called after WindowShow()
    // so KDE/Plasma sees the placeholder → real window handoff cleanly (#154).
    // Fired unconditionally so the indicator clears even when starting hidden.
    NotifyStartupComplete()

    // Remove GTK max size constraints that Wails v2 sets at startup
    RefreshWindowConstraints()

    // Initialize responsive layout breakpoint listeners
    initLayout()

    // Check for pending mailto: URL from command line
    try {
      const mailtoData = await GetPendingMailto()
      if (mailtoData && (mailtoData.to?.length > 0 || mailtoData.subject || mailtoData.body)) {
        // Wait a moment for accounts to load
        await new Promise(resolve => setTimeout(resolve, 100))
        handleMailtoData(mailtoData)
      }
    } catch (err) {
      console.error('Failed to check pending mailto:', err)
    }
  })

  // Handle folder selection from sidebar (account tree)
  function handleFolderSelect(
    accountId: string,
    folderId: string,
    folderPath: string,
    folderName: string,
    folderType: string
  ) {
    selectedAccountId = accountId
    selectedFolderId = folderId
    selectedFolderName = folderName
    selectedFolderType = folderType
    selectionSource = 'account'
    closeAllMessageTabs()
    selectedThreadId = null // Clear conversation selection when changing folders
    selectedConversationFolderId = null
    selectedConversationAccountId = null
    hideSidebar()

    // Persist state
    saveUIState({
      selectedAccountId: accountId,
      selectedFolderId: folderId,
      selectedFolderName: folderName,
      selectedFolderType: folderType,
      selectedThreadId: null,
      selectedConversationAccountId: null,
      selectedConversationFolderId: null,
    })
  }

  // Handle folder selection from the Unified zone: one of the seven
  // unified folders. The view merges that folder across all accounts.
  function handleUnifiedFolderSelect(folderType: string, label: string) {
    selectedAccountId = 'unified'
    selectedFolderId = folderType
    selectedFolderName = label
    selectedFolderType = folderType
    selectionSource = 'unified'
    closeAllMessageTabs()
    selectedThreadId = null
    selectedConversationFolderId = null
    selectedConversationAccountId = null
    hideSidebar()

    // Persist state
    saveUIState({
      selectedAccountId: 'unified',
      selectedFolderId: folderType,
      selectedFolderName: label,
      selectedFolderType: folderType,
      selectedThreadId: null,
      selectedConversationAccountId: null,
      selectedConversationFolderId: null,
    })
  }

  // Handle unified inbox selection from sidebar (Inbox)
  function handleUnifiedInboxSelect() {
    handleUnifiedFolderSelect('inbox', $_('sidebar.inbox'))
  }

  // First run after the upgrade (or fresh install): no persisted folder
  // selection, so open the Unified Inbox — the sidebar's first-run
  // default. Runs once, only after the restore above has run and the
  // accounts have loaded. An existing valid selection is never overridden.
  $effect(() => {
    if (didDefaultFolderSelect || !uiStateRestored) return
    if (accountStore.loading || accountStore.accounts.length === 0) return
    if (selectedAccountId !== null) return
    didDefaultFolderSelect = true
    handleUnifiedInboxSelect()
  })

  // Handle conversation selection from list — the single dispatch point
  // for opening a message (Phase 5 reuses it for dialog/popup). In tab
  // mode (preview pane off, full layout) this opens a tab; otherwise it
  // selects into the classic viewer beside the list.
  function handleConversationSelect(threadId: string, folderId: string, accountId: string, subject?: string) {
    if (tabMode) {
      openMessage(threadId, folderId, accountId, subject)
      return
    }
    selectedThreadId = threadId
    selectedConversationFolderId = folderId
    selectedConversationAccountId = accountId
    showViewer()

    // Persist state
    saveUIState({
      selectedThreadId: threadId,
      selectedConversationAccountId: accountId,
      selectedConversationFolderId: folderId,
    })
  }

  // Resolve an account ID that may be 'unified' to a real account ID.
  // Returns the first real account ID if the input is 'unified' or falsy.
  function resolveAccountId(id: string | null): string | undefined {
    if (id && id !== 'unified') return id
    return accountStore.accounts[0]?.account.id
  }

  // Undo Send: the sent snackbar (slim, bottom) shown when the
  // worker actually dispatches a queued message.
  let sentSnackbar = $state<{ subject: string } | null>(null)
  let sentSnackbarTimer: ReturnType<typeof setTimeout> | null = null

  function showSentSnackbar(subject: string) {
    sentSnackbar = { subject }
    if (sentSnackbarTimer) clearTimeout(sentSnackbarTimer)
    sentSnackbarTimer = setTimeout(() => { sentSnackbar = null }, 6000)
  }

  // Undo from the countdown toast or the Outbox row: cancel the
  // queued send and reopen the composer with the draft, intact.
  // A reaction undo has no draft — there is nothing to reopen.
  async function handleUndoSend(countdown: UndoSendCountdown) {
    try {
      const res = await UndoSend(countdown.queueId)
      removeUndoSendCountdown(countdown.queueId)
      if (res.draftId) await handleEditDraft(res.draftId, res.accountId)
    } catch (err) {
      // Already sent: the worker beat the click. The mail is gone;
      // say so plainly instead of reopening a stale draft.
      removeUndoSendCountdown(countdown.queueId)
      console.error('Undo send failed:', err)
      addToast({ type: 'error', message: $_('undoSend.alreadySent') })
    }
  }

  // Undo from an Outbox row (same as the toast, minus the countdown).
  async function handleOutboxUndoSend(queueId: string, draftId: string, accountId: string) {
    await handleUndoSend({ queueId, accountId, draftId, subject: '', recipient: '', totalSeconds: 0, remainingSeconds: 0, sendAt: 0 })
  }

  // Handle compose button click (new message)
  function handleCompose() {
    // Use the selected account, or the first account if none selected
    const accountId = resolveAccountId(selectedAccountId)
    if (!accountId) return

    // Check if detached mode is preferred
    if (getComposerMode() === 'detached') {
      OpenComposerWindow(accountId, 'new', '', '', '')
      return
    }

    composerAccountId = accountId
    composerInitialMessage = null
    composerDraftId = null
    showComposer = true
  }

  // Handle edit draft (opens composer with existing draft)
  async function handleEditDraft(draftId: string, accountIdOverride?: string) {
    // Use the override (Undo Send knows the draft's account), else the
    // conversation's account ID, fall back to selected account or first account
    const accountId = accountIdOverride || resolveAccountId(selectedConversationAccountId) || resolveAccountId(selectedAccountId)
    if (!accountId) return

    try {
      // Load the draft content from backend
      const draftMessage = await GetDraft(draftId)

      composerAccountId = accountId
      composerInitialMessage = draftMessage || null
      composerDraftId = draftId
      showComposer = true
    } catch (err) {
      console.error('Failed to load draft:', err)
      addToast({
        type: 'error',
        message: $_('composer.failedToLoadDraft'),
      })
    }
  }

  // Handle compose to a specific email address (from mailto: links in emails)
  function handleComposeToAddress(toAddress: string) {
    // Use conversation's account ID, or selected account, or first account
    const accountId = resolveAccountId(selectedConversationAccountId) || resolveAccountId(selectedAccountId)
    if (!accountId) return

    composerAccountId = accountId
    composerDraftId = null
    // Create a minimal ComposeMessage with just the To address
    composerInitialMessage = new smtp.ComposeMessage({
      from: new smtp.Address({ name: '', address: '' }),
      to: [new smtp.Address({ name: '', address: toAddress })],
      cc: [],
      bcc: [],
      subject: '',
      text_body: '',
      html_body: '',
      attachments: [],
      request_read_receipt: false,
    })
    showComposer = true
  }

  // Handle mailto: URL data (from command line launch)
  interface MailtoData {
    to?: string[]
    cc?: string[]
    bcc?: string[]
    subject?: string
    body?: string
  }

  function handleMailtoData(data: MailtoData, rawMailtoURL?: string) {
    // Use selected account or first account (resolve 'unified' to real account)
    const accountId = resolveAccountId(selectedAccountId)
    if (!accountId) {
      // No accounts available, can't compose
      addToast({
        type: 'error',
        message: $_('toast.noAccountConfigured'),
      })
      return
    }

    // Check if detached mode is preferred for mailto
    if (getMailtoMode() === 'detached' && rawMailtoURL) {
      OpenComposerWindow(accountId, 'new', '', '', rawMailtoURL)
      return
    }

    composerAccountId = accountId
    composerDraftId = null
    composerInitialMessage = new smtp.ComposeMessage({
      from: new smtp.Address({ name: '', address: '' }),
      to: (data.to || []).map(addr => new smtp.Address({ name: '', address: addr })),
      cc: (data.cc || []).map(addr => new smtp.Address({ name: '', address: addr })),
      bcc: (data.bcc || []).map(addr => new smtp.Address({ name: '', address: addr })),
      subject: data.subject || '',
      text_body: data.body || '',
      html_body: '',
      attachments: [],
      request_read_receipt: false,
    })
    showComposer = true
  }

  // Handle reply/reply-all/forward - calls backend API
  async function handleReply(mode: 'reply' | 'reply-all' | 'forward', messageId: string, imagesLoaded?: boolean) {
    // Use conversation's account ID (important for unified inbox), fall back to selected account or first account
    const accountId = resolveAccountId(selectedConversationAccountId) || resolveAccountId(selectedAccountId)
    if (!accountId) return

    // Force detached composer when in focus mode — preserves the focused view
    if (focusMode !== 'off') {
      OpenComposerWindow(accountId, mode, messageId, '', '')
      return
    }

    try {
      // Call backend to prepare the reply message (backend gets account from message)
      const composeMessage = await PrepareReply(messageId, mode)
      composerAccountId = accountId
      composerDraftId = null
      composerInitialMessage = composeMessage
      composerImagesLoaded = imagesLoaded || false
      showComposer = true
    } catch (err) {
      console.error(`Failed to prepare ${mode}:`, err)
      addToast({
        type: 'error',
        message: $_('toast.failedToPrepare', { values: { mode } }),
      })
      // Fallback: open blank composer
      composerAccountId = accountId
      composerDraftId = null
      composerInitialMessage = null
      showComposer = true
    }
  }

  // Close composer
  function closeComposer() {
    showComposer = false
    composerAccountId = null
    composerInitialMessage = null
  }

  // Pane sizing state
  let sidebarWidth = $state(240)
  let listWidth = $state(420)

  // Resizing state
  let isResizingSidebar = $state(false)
  let isResizingList = $state(false)
  // Width the list had when a list drag started. Dragging the divider to
  // the far right edge turns the preview pane off, and the stored width
  // must stay as it was so re-enabling the pane restores it.
  let listWidthBeforeDrag = 420
  // How close to the window's right edge (px) the pointer must get during
  // a list drag to close the preview pane.
  const LIST_DRAG_CLOSE_EDGE_PX = 6

  function startResizeSidebar(e: MouseEvent) {
    if (isResponsive()) return
    isResizingSidebar = true
    e.preventDefault()
  }

  function startResizeList(e: MouseEvent) {
    if (isResponsive()) return
    listWidthBeforeDrag = listWidth
    isResizingList = true
    e.preventDefault()
  }

  // Turn the preview pane off through the same setting the Settings
  // toggle uses, so it is persisted and survives a restart.
  async function turnOffPreviewPane() {
    try {
      await SetShowMessagePreviewPane(false)
      setShowMessagePreviewPane(false)
    } catch (err) {
      console.error('Failed to turn off preview pane:', err)
    }
  }

  function handleMouseMove(e: MouseEvent) {
    if (isResizingSidebar) {
      sidebarWidth = Math.max(paneConstraints.sidebar.min, Math.min(paneConstraints.sidebar.max, e.clientX))
    } else if (isResizingList) {
      // Dragging the divider to the far right edge of the window closes
      // the preview pane directly. Only while the pane is on and the
      // layout is the list+preview (full) mode — the divider only exists
      // in that mode, but check anyway. The stored width is left as it
      // was, so turning the pane back on restores the previous width.
      if (
        getShowMessagePreviewPane() &&
        getLayoutMode() === 'full' &&
        e.clientX >= window.innerWidth - LIST_DRAG_CLOSE_EDGE_PX
      ) {
        void turnOffPreviewPane()
        listWidth = listWidthBeforeDrag
        isResizingList = false
        return
      }
      listWidth = Math.max(paneConstraints.list.min, Math.min(paneConstraints.list.max, e.clientX - sidebarWidth))
    }
  }

  function handleMouseUp() {
    // Save pane widths if we were resizing
    if (isResizingSidebar || isResizingList) {
      saveUIState({ sidebarWidth, listWidth })
    }
    isResizingSidebar = false
    isResizingList = false
  }

  // After a synthetic contextmenu event, bits-ui mounts the portal asynchronously.
  // Poll until [role="menu"] appears, then focus the first menuitem.
  function focusContextMenu() {
    let attempts = 0
    const tryFocus = () => {
      const menu = document.querySelector('[role="menu"]') as HTMLElement | null
      if (menu) {
        const firstItem = menu.querySelector('[role="menuitem"]:not([data-disabled])') as HTMLElement | null
        ;(firstItem || menu).focus()
        return
      }
      if (attempts++ < 10) {
        requestAnimationFrame(tryFocus)
      }
    }
    requestAnimationFrame(tryFocus)
  }

  // Track Left Alt held state for Left Alt + Right Alt combo
  let leftAltHeld = false

  function handleGlobalKeyUp(e: KeyboardEvent) {
    if (e.code === 'AltLeft') {
      leftAltHeld = false
    }
  }

  // Global keyboard shortcut handler
  function handleGlobalKeyDown(e: KeyboardEvent) {
    // Track Left Alt press
    if (e.code === 'AltLeft') {
      leftAltHeld = true
    }
    const inInput = isInputElement(e.target)
    const focusedPane = getFocusedPane()
    const hasConversation = selectedThreadId !== null

    // TEMP: keypress-gate audit — remove in the fix commit. Names which gate
    // swallowed a silent first `d`/`j`/`k` after a long session. Logged at
    // error so `--log-level error --log-file <path>` captures only these
    // lines plus genuine failures over a multi-day session.
    if (e.key === 'j' || e.key === 'k' || e.key === 'd' || e.key === 'Delete' || e.key === 'Backspace') {
      const checkedCount = messageListRef?.getSelectedMessageIds().length ?? -1
      logger.error(
        `keygate key=${e.key} pane=${focusedPane} inInput=${inInput} target=${(e.target as HTMLElement)?.tagName ?? 'none'}` +
        ` menu=${!!document.querySelector('[role="menu"]')} guard=${isDialogGuardActive()} composer=${showComposer}` +
        ` ext=${getActiveExtension() ?? 'mail'} appThread=${selectedThreadId ?? 'null'} listThread=${messageListRef?.getSelectedThreadId() ?? 'null'}` +
        ` focusedIds=${checkedCount} viewerFocus=${viewerRef?.hasFocusedMessage() ?? 'n/a'}`
      )
    }

    // Don't intercept keyboard events when a context menu or dropdown is open
    // (bits-ui portals mount [role="menu"] only while open)
    if (document.querySelector('[role="menu"]')) return

    // Alt+M/Alt+C toggle the message list's folder picker. While it's open the
    // dialog guard below swallows all keys, so close/switch is handled here.
    if (messageListRef?.isFolderPickerOpen()) {
      if (KEY.LIST_MOVE_TO(e)) {
        e.preventDefault()
        messageListRef.toggleMoveToDialog()
        return
      }
      if (KEY.LIST_COPY_TO(e)) {
        e.preventDefault()
        messageListRef.toggleCopyToDialog()
        return
      }
    }

    // Don't intercept while a modal dialog has the guard active — keystrokes
    // (especially Ctrl+A) should target dialog inputs, not the background.
    if (isDialogGuardActive()) return

    // Extension shortcut dispatch: when the active rail pane is NOT mail, let
    // the extension's registered shortcuts run first. dispatchExtensionShortcut
    // returns true when a handler matched — in that case we treat the event as
    // handled and skip mail's downstream dispatch. Skipped while typing in an
    // input element (consistent with mail's inInput guard below) so extension
    // shortcuts don't fire from inside text fields. See [[extension-sdk-pattern]]
    // and frontend/src/lib/stores/extensionShortcuts.svelte.ts.
    if (!inInput && dispatchExtensionShortcut(e)) {
      e.preventDefault()
      e.stopPropagation()
      return
    }

    // When composer is open, only handle Escape (composer handles its own shortcuts)
    if (showComposer) {
      // Block compose/reply shortcuts that might conflict
      if ((e.ctrlKey || e.metaKey) && ['r', 'f'].includes(e.key.toLowerCase())) {
        e.preventDefault()
        return
      }
      return
    }

    // Handle Ctrl/Cmd shortcuts.
    //
    // Layout: GLOBAL cases first (fire regardless of which rail pane is active),
    // then a guard that returns when an extension is the active rail pane, then
    // MAIL-DOMAIN cases (fire only on mail). The kit's components (extension UI)
    // handle their own list/sidebar shortcuts via local tabindex+keydown +
    // stopPropagation, so those events never reach this handler in the first
    // place. The guard catches the case where Ctrl+R / Ctrl+K / etc. fire
    // with no kit pane DOM-focused but an extension is the active rail pane —
    // we don't want those acting on the (hidden) mail UI.
    const isMailActive = () => getActiveExtension() === 'mail'
    if (e.ctrlKey || e.metaKey) {
      // GLOBAL Ctrl/Cmd shortcuts — fire regardless of active rail pane.
      switch (e.key.toLowerCase()) {
        case 'q':
          e.preventDefault()
          handleQuit()
          return
        case 'tab':
        case '`': {
          // Cycle through rail items: Mail + enabled extensions.
          // Ctrl+Tab        → forward
          // Ctrl+`          → backward
          // (Ctrl+Shift+Tab is intercepted by webkit2gtk before the
          //  keydown event reaches us, so we use Ctrl+` for backward.)
          e.preventDefault()
          const tabs = getRailTabs()
          const order = ['mail', ...tabs.map(t => t.extensionId)]
          if (order.length <= 1) return // only Mail — nothing to cycle
          const current = getActiveExtension()
          const idx = order.indexOf(current)
          const step = e.key === '`' ? -1 : 1
          const next = (idx + step + order.length) % order.length
          setActiveExtension(order[next])
          return
        }
      }

      // Ctrl+S (no shift) — focus the active pane's search input.
      // Mail dispatches to messageListRef; extensions dispatch via the
      // pane-nav registry. Ctrl+Shift+S (sync folder) is mail-only and
      // stays in the mail-domain switch below.
      if (e.key.toLowerCase() === 's' && !e.shiftKey) {
        e.preventDefault()
        if (isMailActive()) {
          messageListRef?.toggleSearchFocus()
          setFocusedPane('messageList')
          return
        }
        getPaneNav('messageList')?.focusSearch?.()
        setFocusedPane('messageList')
        return
      }

      // Below: MAIL-DOMAIN Ctrl/Cmd shortcuts. Guarded so they no-op when an
      // extension is the active rail pane (otherwise Ctrl+R would silently
      // reply to the hidden mail thread while the user is browsing contacts).
      if (!isMailActive()) return

      // MAIL-DOMAIN Ctrl/Cmd cases (guarded above).
      switch (e.key.toLowerCase()) {
        case 'n':
          // Ctrl/Cmd+N — new mail composer. Mail-domain only: when an
          // extension rail is active (e.g., calendar), that extension's
          // shortcut registry handles Ctrl+N before we reach the global
          // switch, opening its own "new X" dialog.
          e.preventDefault()
          handleCompose()
          return
        case 'r': {
          if (!hasConversation) return
          e.preventDefault()
          if (focusedPane === 'viewer' && viewerRef?.hasFocusedMessage()) {
            if (e.shiftKey) {
              viewerRef.replyAll()
              return
            }
            viewerRef.reply()
            return
          }
          const msgId = getLastMessageId()
          if (!msgId) return
          handleReply(e.shiftKey ? 'reply-all' : 'reply', msgId, viewerRef?.isImagesLoaded(msgId) || false)
          return
        }
        case 'f': {
          if (!hasConversation) return
          e.preventDefault()
          if (focusedPane === 'viewer' && viewerRef?.hasFocusedMessage()) {
            viewerRef.forward()
            return
          }
          const msgId = getLastMessageId()
          if (msgId) handleReply('forward', msgId, viewerRef?.isImagesLoaded(msgId) || false)
          return
        }
        case 's':
          // Ctrl+S (no shift) is handled globally above. Only Ctrl+Shift+S
          // (sync current folder) is mail-domain and lives here.
          if (!e.shiftKey) return
          e.preventDefault()
          messageListRef?.toggleFolderSync()
          return
        case 'a':
          // Ctrl-A must not hijack text selection inside inputs (e.g. the
          // list search box) — there the browser's native select-all-text
          // applies. Same guard for Ctrl-Shift-A (sync all).
          if (inInput) return
          if (e.shiftKey) {
            // Ctrl-Shift-A: Toggle sync all accounts (start sync or cancel if already running)
            e.preventDefault()
            sidebarRef?.toggleSync()
            return
          }
          // Ctrl-A: select all text in the viewer, or select all LOADED
          // messages when the list is focused. Any other pane: no-op, so a
          // stray Ctrl-A never bulk-selects messages out of context.
          if (focusedPane === 'viewer') {
            e.preventDefault()
            viewerRef?.selectAllText()
            return
          }
          if (focusedPane === 'messageList') {
            e.preventDefault()
            messageListRef?.selectAll()
            return
          }
          return
        case 'l':
          e.preventDefault()
          if (e.shiftKey) {
            // Ctrl-Shift-L: Open "Always Load" dropdown
            viewerRef?.openAlwaysLoadDropdown()
          } else {
            // Ctrl-L: Load images for this message
            viewerRef?.loadImages()
          }
          return
        case 'u':
          e.preventDefault()
          if (messageListRef?.hasCheckedMessages()) {
            const messageIds = messageListRef.getCheckedMessageIds()
            if (e.shiftKey) {
              handleBulkMarkUnread(messageIds)
            } else {
              handleBulkMarkRead(messageIds)
            }
          } else {
            // Mark the keyboard-focused message as read/unread
            const focusedIds = messageListRef?.getSelectedMessageIds() ?? []
            if (focusedIds.length > 0) {
              if (e.shiftKey) {
                handleBulkMarkUnread(focusedIds)
              } else {
                handleBulkMarkRead(focusedIds)
              }
            }
          }
          return
        case 'k':
          e.preventDefault()
          if (messageListRef?.hasCheckedMessages()) {
            handleBulkArchive(messageListRef.getCheckedMessageIds())
          } else {
            // Archive the keyboard-focused message
            const focusedIds = messageListRef?.getSelectedMessageIds() ?? []
            if (focusedIds.length > 0) {
              handleBulkArchive(focusedIds)
            }
          }
          return
        case 'j':
          e.preventDefault()
          if (messageListRef?.hasCheckedMessages()) {
            handleBulkSpam(messageListRef.getCheckedMessageIds())
          } else {
            // Spam the keyboard-focused message
            const focusedIds = messageListRef?.getSelectedMessageIds() ?? []
            if (focusedIds.length > 0) {
              handleBulkSpam(focusedIds)
            }
          }
          return
      }
      return
    }

    // Right Alt or ContextMenu key: open context menu for focused item in current pane
    // Left Alt + Right Alt: always open folder context menu regardless of pane
    if (e.key === 'ContextMenu' || (e.key === 'Alt' && e.code === 'AltRight')) {
      e.preventDefault()

      // Left Alt + Right Alt combo: always target the selected folder
      if (leftAltHeld || focusedPane === 'sidebar') {
        if (!selectedFolderId) return
        const folderEl = document.querySelector(
          `[data-sidebar-item="folder"][data-folder-id="${selectedFolderId}"], ` +
          `[data-sidebar-item="unified-account"][data-folder-id="${selectedFolderId}"]`
        ) as HTMLElement | null
        if (!folderEl) return
        const rect = folderEl.getBoundingClientRect()
        folderEl.dispatchEvent(new MouseEvent('contextmenu', {
          bubbles: true,
          clientX: rect.right,
          clientY: rect.top + rect.height / 2,
        }))
        focusContextMenu()
        return
      }

      switch (focusedPane) {
        case 'messageList': {
          messageListRef?.openContextMenu()
          focusContextMenu()
          return
        }
        case 'viewer': {
          viewerRef?.openContextMenu()
          focusContextMenu()
          return
        }
      }
      return
    }

    // Handle Alt shortcuts (pane/folder navigation, always work)
    if (e.altKey) {
      // Pane navigation is meaningless in focus mode (other panes hidden)
      if (focusMode !== 'off') return

      // Pane focus cycling (shared predicate — kit's pane components react to
      // the same focusedPane store, so cycling works uniformly across mail
      // and extensions).
      if (KEY.PANE_FOCUS_PREV(e)) {
        e.preventDefault()
        if (isResponsive()) {
          const view = getResponsiveView()
          const mode = getLayoutMode()
          if (view === 'viewer') {
            hideViewer()
            return
          }
          if (mode === 'narrow' && view === 'default') {
            showSidebar()
            return
          }
        }
        focusPreviousPane()
        return
      }
      if (KEY.PANE_FOCUS_NEXT(e)) {
        e.preventDefault()
        if (isResponsive()) {
          const view = getResponsiveView()
          const mode = getLayoutMode()
          if (mode === 'narrow' && view === 'sidebar') {
            hideSidebar()
            return
          }
          if (view === 'default' && selectedThreadId) {
            showViewer()
            return
          }
        }
        focusNextPane()
        return
      }

      // Sidebar item navigation (Alt+Up/Down/J/K). Dispatches to mail's
      // concrete ref when mail is active; otherwise to the kit pane that
      // registered as 'sidebar' via registerPaneNav. This way extensions
      // get the same "global Alt+J/K navigates the sidebar regardless of
      // which pane is currently DOM-focused" behavior mail has.
      if (KEY.SIDEBAR_PREV(e)) {
        e.preventDefault()
        if (isMailActive()) {
          sidebarRef?.selectPreviousFolder()
          return
        }
        getPaneNav('sidebar')?.navigatePrev?.()
        return
      }
      if (KEY.SIDEBAR_NEXT(e)) {
        e.preventDefault()
        if (isMailActive()) {
          sidebarRef?.selectNextFolder()
          return
        }
        getPaneNav('sidebar')?.navigateNext?.()
        return
      }

      // Alt+G / Alt+Shift+G — jump to the first sidebar item (All Inboxes
      // when present) or the last visible one (last account header when all
      // folder trees are collapsed).
      if (KEY.SIDEBAR_FIRST(e)) {
        e.preventDefault()
        if (isMailActive()) {
          sidebarRef?.selectFirstFolder()
          return
        }
        getPaneNav('sidebar')?.navigateFirst?.()
        return
      }
      if (KEY.SIDEBAR_LAST(e)) {
        e.preventDefault()
        if (isMailActive()) {
          sidebarRef?.selectLastFolder()
          return
        }
        getPaneNav('sidebar')?.navigateLast?.()
        return
      }

      // Alt+M / Alt+C — move/copy folder picker for the keyboard-focused
      // message. Open path only: the picker sets the dialog guard, so the
      // close/switch press is handled pre-guard near the top of this handler.
      if (KEY.LIST_MOVE_TO(e)) {
        if (!isMailActive()) return
        e.preventDefault()
        messageListRef?.toggleMoveToDialog()
        return
      }
      if (KEY.LIST_COPY_TO(e)) {
        if (!isMailActive()) return
        e.preventDefault()
        messageListRef?.toggleCopyToDialog()
        return
      }

      // Alt+Enter — mail sidebar expand/collapse. Keep inline switch for
      // single residual case.
      switch (e.key) {
        case 'Enter':
          // Toggle expand/collapse for focused account header or
          // selected folder with children
          if (sidebarRef?.hasFocusedAccount()) {
            e.preventDefault()
            sidebarRef.toggleFocusedAccount()
          } else if (sidebarRef?.hasSelectedFolderWithChildren()) {
            e.preventDefault()
            sidebarRef.toggleSelectedFolderCollapse()
          }
          return
      }
      return
    }

    // Skip single-key shortcuts if in input field
    if (inInput) return

    // Handle Escape (context-dependent, progressive)
    // Focus mode first, then responsive overlays, then checkboxes, then conversation
    if (e.key === 'Escape') {
      if (focusMode !== 'off') {
        focusMode = 'off'
        focusedMessageIdInFocus = null
        return
      }
      if (isResponsive() && getResponsiveView() === 'viewer') {
        hideViewer()
        return
      }
      if (isResponsive() && getResponsiveView() === 'sidebar') {
        hideSidebar()
        return
      }
      if (tabMode && dialogMessage) {
        // First: close the open message dialog, back to the folder/list view
        closeMessageDialog()
        return
      }
      if (tabMode && activeMessageTabId) {
        // First: close the open message tab, back to the folder tab
        closeMessageTab(activeMessageTabId)
        return
      }
      if (messageListRef?.hasCheckedMessages()) {
        // First: clear checkboxes
        messageListRef.clearChecked()
      } else if (selectedThreadId) {
        // Second: close conversation viewer
        selectedThreadId = null
        selectedConversationFolderId = null
        selectedConversationAccountId = null
      }
      return
    }

    // Handle pane-focused navigation shortcuts.
    //
    // Mail-domain: these run for the mail UI's focused pane. The kit's
    // components handle their own list/sidebar navigation via local
    // keydown + stopPropagation (so the events never reach this handler).
    // Guard for safety in case an event slips through while an extension
    // is the active rail pane.
    if (KEY.LIST_PREV(e) || KEY.LIST_PREV_CHECK(e)) {
      if (!isMailActive()) return
      e.preventDefault()
      if (focusedPane === 'sidebar') {
        sidebarRef?.selectPreviousFolder()
      } else if (focusedPane === 'messageList') {
        if (e.shiftKey) {
          messageListRef?.selectPreviousWithCheck()
        } else {
          messageListRef?.selectPrevious()
        }
      } else if (focusedPane === 'viewer') {
        viewerRef?.scrollUp()
      }
      return
    }
    if (KEY.LIST_NEXT(e) || KEY.LIST_NEXT_CHECK(e)) {
      if (!isMailActive()) return
      e.preventDefault()
      if (focusedPane === 'sidebar') {
        sidebarRef?.selectNextFolder()
      } else if (focusedPane === 'messageList') {
        if (e.shiftKey) {
          messageListRef?.selectNextWithCheck()
        } else {
          messageListRef?.selectNext()
        }
      } else if (focusedPane === 'viewer') {
        viewerRef?.scrollDown()
      }
      return
    }

    // ArrowRight / ArrowLeft in the sidebar — expand / collapse the focused
    // row (account header, or selected folder with children). Plain arrows
    // only; Alt+arrows cycle panes.
    if (e.key === 'ArrowRight' && !e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey) {
      if (isMailActive() && focusedPane === 'sidebar') {
        e.preventDefault()
        sidebarRef?.expandFocusedRow()
        return
      }
    }
    if (e.key === 'ArrowLeft' && !e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey) {
      if (isMailActive() && focusedPane === 'sidebar') {
        e.preventDefault()
        sidebarRef?.collapseFocusedRow()
        return
      }
    }

    // Enter and Space — domain-specific button-vs-pane disambiguation logic
    // stays as inline switch (kit components handle their own Enter/Space
    // locally so they don't depend on this dispatch).
    switch (e.key) {
      case 'Enter':
        // Only let buttons handle Enter if they're in the focused pane
        // This prevents sidebar buttons from intercepting Enter when messageList is focused
        if (document.activeElement?.tagName === 'BUTTON') {
          const btn = document.activeElement as HTMLElement
          const inMessageList = btn.closest('[data-pane="messageList"]')
          const inViewer = btn.closest('[data-pane="viewer"]')
          // Only let button handle Enter if it's in the currently focused pane
          if ((focusedPane === 'messageList' && inMessageList) ||
              (focusedPane === 'viewer' && inViewer)) {
            return
          }
          // Otherwise, prevent button click and handle with our logic
          e.preventDefault()
        }
        if (focusedPane === 'sidebar' && sidebarRef?.hasFocusedAccount()) {
          e.preventDefault()
          sidebarRef.openFocusedAccountInbox()
        } else if (focusedPane === 'sidebar' && sidebarRef?.hasSelectedFolderWithChildren()) {
          e.preventDefault()
          sidebarRef.toggleSelectedFolderCollapse()
        } else if (focusedPane === 'messageList') {
          e.preventDefault()
          messageListRef?.openSelected()
        }
        return
      case ' ':  // Space - toggle checkbox on focused message, or expand/collapse account
        // Only let buttons handle Space if they're in the focused pane
        if (document.activeElement?.tagName === 'BUTTON') {
          const btn = document.activeElement as HTMLElement
          const inMessageList = btn.closest('[data-pane="messageList"]')
          const inViewer = btn.closest('[data-pane="viewer"]')
          if ((focusedPane === 'messageList' && inMessageList) ||
              (focusedPane === 'viewer' && inViewer)) {
            return
          }
          e.preventDefault()
        }
        e.preventDefault()
        if (focusedPane === 'sidebar' && sidebarRef?.hasFocusedAccount()) {
          sidebarRef.toggleFocusedAccount()
        } else if (focusedPane === 'sidebar' && sidebarRef?.hasSelectedFolderWithChildren()) {
          sidebarRef.toggleSelectedFolderCollapse()
        } else if (focusedPane === 'messageList') {
          messageListRef?.toggleCheck()
        }
        return
    }

    // g / Shift+G — jump to first / last loaded message (shared predicates —
    // kit list panes consume the same KEY.LIST_FIRST/LIST_LAST locally)
    if (KEY.LIST_FIRST(e)) {
      if (!isMailActive()) return
      if (focusedPane !== 'messageList') return
      e.preventDefault()
      messageListRef?.selectFirst()
      return
    }
    if (KEY.LIST_LAST(e)) {
      if (!isMailActive()) return
      if (focusedPane !== 'messageList') return
      e.preventDefault()
      messageListRef?.selectLast()
      return
    }

    // V — open the keyboard-focused conversation in the viewer (alias of
    // Enter; shared predicate — kit list panes consume KEY.LIST_VIEW locally)
    if (KEY.LIST_VIEW(e)) {
      if (focusedPane === 'messageList') {
        e.preventDefault()
        messageListRef?.openSelected()
      }
      return
    }

    // Single-key shortcuts
    switch (e.key) {
      case 's':
        if (messageListRef?.hasCheckedMessages()) {
          handleBulkToggleStar(messageListRef.getCheckedMessageIds(), messageListRef.getCheckedHasUnstarred())
        } else {
          // Toggle star on the keyboard-focused message
          const focusedIds = messageListRef?.getSelectedMessageIds() ?? []
          if (focusedIds.length > 0) {
            const isStarred = messageListRef?.isSelectedStarred() ?? false
            handleBulkToggleStar(focusedIds, !isStarred)
          }
        }
        return
      case 'f':
        // Toggle thread focus mode (only with a conversation open)
        if (!hasConversation) return
        e.preventDefault()
        toggleThreadFocus()
        return
      case 'F': {
        // Shift+F: toggle message focus on the currently Tab-focused message
        // (falls back to last message if none focused)
        if (!hasConversation) return
        e.preventDefault()
        if (focusMode === 'message') {
          focusMode = 'off'
          focusedMessageIdInFocus = null
          return
        }
        const targetId = (focusedPane === 'viewer' && viewerRef?.hasFocusedMessage())
          ? viewerRef.getFocusedMessageId()
          : getLastMessageId()
        if (!targetId) return
        focusMode = 'message'
        focusedMessageIdInFocus = targetId
        return
      }
      case 'd': // alias of Delete: move focused/checked message(s) to Trash
      case 'Backspace':
      case 'Delete': {
        if (focusedPane === 'viewer' && viewerRef?.hasFocusedMessage()) {
          if (e.shiftKey) {
            viewerRef.deletePermanently()
            return
          }
          viewerRef.trash()
          return
        }
        if (messageListRef?.hasCheckedMessages()) {
          messageListRef.requestDelete(messageListRef.getCheckedMessageIds(), e.shiftKey)
          return
        }
        const focusedMessageIds = messageListRef?.getSelectedMessageIds() ?? []
        if (focusedMessageIds.length > 0) {
          messageListRef?.requestDelete(focusedMessageIds, e.shiftKey)
        }
        return
      }
    }
  }

  // Get the last message ID from the current conversation (for reply/forward)
  function getLastMessageId(): string | null {
    return viewerRef?.getLastMessageId() ?? null
  }

  // Handle click on pane to set focus
  function handlePaneClick(pane: FocusablePane) {
    setFocusedPane(pane)
  }

  // Bulk action handlers
  async function handleBulkArchive(messageIds: string[]) {
    try {
      await Archive(messageIds)
      addToast({ type: 'success', message: $_('toast.archived'), actions: [{ label: $_('common.undo'), onClick: handleUndo }] })
      messageListRef?.clearChecked()
      messageListRef?.handleActionComplete(true)
    } catch (err) {
      console.error('Archive failed:', err)
      addToast({ type: 'error', message: $_('toast.failedToArchive') })
    }
  }

  async function handleBulkSpam(messageIds: string[]) {
    try {
      const isSpamFolder = selectedFolderType === 'spam'

      if (isSpamFolder) {
        // If we're in spam folder, mark as NOT spam
        await MarkAsNotSpam(messageIds)
        addToast({ type: 'success', message: $_('toast.markedAsNotSpam'), actions: [{ label: $_('common.undo'), onClick: handleUndo }] })
      } else {
        // Otherwise, mark as spam
        await MarkAsSpam(messageIds)
        addToast({ type: 'success', message: $_('toast.markedAsSpam'), actions: [{ label: $_('common.undo'), onClick: handleUndo }] })
      }

      messageListRef?.clearChecked()
      messageListRef?.handleActionComplete(true)
    } catch (err) {
      const isSpamFolder = selectedFolderType === 'spam'
      console.error('Spam toggle failed:', err)
      addToast({ type: 'error', message: $_(isSpamFolder ? 'toast.failedToMarkAsNotSpam' : 'toast.failedToMarkAsSpam') })
    }
  }

  async function handleBulkMarkRead(messageIds: string[]) {
    try {
      await MarkAsRead(messageIds)
      addToast({ type: 'success', message: $_('toast.markedAsRead') })
      messageListRef?.clearChecked()
      messageListRef?.handleActionComplete()
    } catch (err) {
      console.error('Mark as read failed:', err)
      addToast({ type: 'error', message: $_('toast.failedToMarkAsRead') })
    }
  }

  async function handleBulkMarkUnread(messageIds: string[]) {
    try {
      await MarkAsUnread(messageIds)
      addToast({ type: 'success', message: $_('toast.markedAsUnread') })
      messageListRef?.clearChecked()
      messageListRef?.handleActionComplete()
    } catch (err) {
      console.error('Mark as unread failed:', err)
      addToast({ type: 'error', message: $_('toast.failedToMarkAsUnread') })
    }
  }

  async function handleBulkToggleStar(messageIds: string[], shouldStar: boolean) {
    try {
      if (shouldStar) {
        await Star(messageIds)
        addToast({ type: 'success', message: $_('toast.starred') })
      } else {
        await Unstar(messageIds)
        addToast({ type: 'success', message: $_('toast.starRemoved') })
      }
      messageListRef?.clearChecked()
      messageListRef?.handleActionComplete()
    } catch (err) {
      console.error('Star toggle failed:', err)
      addToast({ type: 'error', message: $_('toast.failedToUpdateStar') })
    }
  }

  async function handleUndo() {
    try {
      const description = await Undo()
      addToast({ type: 'success', message: $_('toast.undone', { values: { description } }) })
      messageListRef?.handleActionComplete()
    } catch (err) {
      console.error('Undo failed:', err)
      addToast({ type: 'error', message: $_('toast.undoFailed') })
    }
  }
</script>

<svelte:window onmousemove={handleMouseMove} onmouseup={handleMouseUp} onkeydown={handleGlobalKeyDown} onkeyup={handleGlobalKeyUp} />

<div class="flex flex-col h-full w-full overflow-hidden bg-background">
  <!-- Custom Title Bar -->
  {#if getShowTitleBar() && !getNativeTitleBar()}
    <TitleBar onClose={handleClose} />
  {/if}

  <!-- Main Content -->
  <div class="flex flex-1 min-h-0 overflow-hidden relative">
    <ExtensionRail />

    {#if getActiveExtension() === 'contacts'}
      <ContactsPane />
    {/if}

    {#if getActiveExtension() === 'calendar'}
      <CalendarPane />
    {/if}

    {#if getActiveExtension() === 'mail-merge'}
      <MailMergeQueuePane />
    {/if}

    <!-- Mail layout is ALWAYS mounted; only its visibility is toggled when an
         extension takes over the pane. Unmounting+remounting the mail tree on
         every extension switch was leaking state (zombie listeners) and pinning
         the main thread on the second mount. display:contents keeps the flex
         children as direct flex items so the layout doesn't shift. -->
    <div style:display={getActiveExtension() === 'mail' ? 'contents' : 'none'}>
    <!-- Sidebar (Folder List) -->
    <aside
      class="{getLayoutMode() === 'narrow' ? `responsive-sidebar-overlay w-72 border-r border-border bg-background ${getResponsiveView() === 'sidebar' ? 'responsive-sidebar-visible' : ''}` : 'flex-shrink-0 border-r border-border bg-muted/30'}"
      style="{getLayoutMode() === 'full' ? `width: ${sidebarWidth}px` : ''}"
      role="presentation"
      onclick={() => handlePaneClick('sidebar')}
    >
      <Sidebar
        bind:this={sidebarRef}
        onFolderSelect={handleFolderSelect}
        onUnifiedFolderSelect={handleUnifiedFolderSelect}
        onCompose={handleCompose}
        onUnifiedInboxSelect={handleUnifiedInboxSelect}
        onMessagesMoved={() => messageListRef?.handleActionComplete(false)}
        selectedAccountId={selectedAccountId}
        selectedFolderId={selectedFolderId}
        selectionSource={selectionSource}
        isFocused={getFocusedPane() === 'sidebar'}
        isFlashing={isPaneFlashing('sidebar')}
        showBackButton={getLayoutMode() === 'narrow'}
        onBack={hideSidebar}
      />
    </aside>

    <!-- Scrim for narrow sidebar overlay -->
    {#if getLayoutMode() === 'narrow'}
      <!-- svelte-ignore a11y_click_events_have_key_events -->
      <div
        role="button"
        tabindex="-1"
        class="responsive-scrim {getResponsiveView() === 'sidebar' ? 'responsive-scrim-visible' : ''}"
        onclick={hideSidebar}
        aria-label={$_('aria.closeSidebar')}
      ></div>
    {/if}

    <!-- Sidebar Resize Handle -->
    {#if getLayoutMode() === 'full'}
    <button
      type="button"
      class="w-1 cursor-col-resize hover:bg-primary/20 active:bg-primary/40 transition-colors border-0 p-0 {isResizingSidebar
        ? 'bg-primary/40'
        : ''}"
      onmousedown={startResizeSidebar}
      aria-label={$_('aria.resizeSidebar')}
    ></button>
    {/if}

    <!-- Message List -->
    <section
      bind:this={messageListContainerRef}
      class="{tabMode || isResponsive() ? 'flex-1 min-w-0 flex flex-col border-r border-border bg-background' : 'flex-shrink-0 flex flex-col border-r border-border bg-background'}"
      style="{getLayoutMode() === 'full' && !tabMode ? `width: ${listWidth}px` : ''}"
      role="presentation"
      data-pane="messageList"
      tabindex="-1"
      onclick={() => handlePaneClick('messageList')}
    >
      {#if tabMode}
        <!-- Tab strip: folder tab first, then one tab per open message -->
        <div class="flex flex-shrink-0 items-end gap-1 overflow-x-auto border-b border-border bg-muted/30 px-2 pt-1.5" role="tablist" aria-label={$_('messageTabs.tabs')}>
          <button
            type="button"
            role="tab"
            aria-selected={activeMessageTabId === null}
            class="max-w-48 truncate rounded-t-md border px-3 py-1.5 text-sm {activeMessageTabId === null ? 'border-border border-b-background bg-background font-medium' : 'border-transparent text-muted-foreground hover:text-foreground'}"
            onclick={() => activateMessageTab(null)}
          >
            {selectedFolderName}
          </button>
          {#each messageTabs as tab (tab.threadId)}
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <div
              role="tab"
              tabindex="0"
              aria-selected={activeMessageTabId === tab.threadId}
              class="flex max-w-56 flex-shrink-0 items-center gap-1 rounded-t-md border px-2 py-1.5 text-sm cursor-pointer {activeMessageTabId === tab.threadId ? 'border-border border-b-background bg-background font-medium' : 'border-transparent text-muted-foreground hover:text-foreground'}"
              onclick={() => activateMessageTab(tab.threadId)}
              onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); activateMessageTab(tab.threadId) } }}
            >
              <span class="truncate">{tab.subject}</span>
              <button
                type="button"
                class="flex-shrink-0 rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"
                aria-label={$_('messageTabs.closeTab')}
                onclick={(e) => { e.stopPropagation(); closeMessageTab(tab.threadId) }}
              >
                <Icon icon="mdi:close" class="w-3.5 h-3.5" />
              </button>
            </div>
          {/each}
        </div>
      {/if}

      <!-- The list stays mounted across tab switches (hidden with
           display:none while a message tab is active) so its scroll
           position and selection survive without a reload. -->
      <div class="flex-1 min-h-0 flex flex-col" style:display={tabMode && activeMessageTabId ? 'none' : ''}>
      <MessageList
        bind:this={messageListRef}
        accountId={selectedAccountId}
        folderId={selectedFolderId}
        folderName={selectedFolderName}
        folderType={selectedFolderType || 'inbox'}
        onConversationSelect={handleConversationSelect}
        onConversationOpen={handleConversationSelect}
        selectOnly={tabMode}
        openThreadId={tabMode ? null : selectedThreadId}
        onReply={handleReply}
        onRowActionComplete={() => viewerRef?.refreshFlags()}
        onUndoSend={handleOutboxUndoSend}
        isFocused={getFocusedPane() === 'messageList'}
        isFlashing={isPaneFlashing('messageList')}
        showFolderToggle={getLayoutMode() === 'narrow'}
        onToggleSidebar={showSidebar}
      />
      </div>

      {#if tabMode && activeMessageTab}
        <!-- One viewer at a time: only the active tab's is mounted -->
        <div
          class="flex-1 min-h-0 flex flex-col bg-background"
          role="presentation"
          data-pane="viewer"
          onclick={() => handlePaneClick('viewer')}
        >
          <!-- Tab header: subject + view switcher -->
          <div class="flex flex-shrink-0 items-center gap-2 border-b border-border bg-muted/30 px-3 py-1.5">
            <span class="min-w-0 flex-1 truncate text-sm font-medium" title={activeMessageTab.subject}>
              {activeMessageTab.subject}
            </span>
            <ViewSwitcher current="tab" onSwitch={moveActiveTabTo} />
          </div>
          <div class="flex-1 min-h-0">
          <ConversationViewer
            bind:this={viewerRef}
            threadId={activeMessageTab.threadId}
            folderId={activeMessageTab.folderId}
            folderType={selectedFolderType}
            accountId={activeMessageTab.accountId}
            onReply={handleReply}
            onComposeToAddress={handleComposeToAddress}
            onEditDraft={handleEditDraft}
            onActionComplete={(autoSelectNext) => messageListRef?.handleActionComplete(autoSelectNext)}
            onConversationDeleted={() => closeMessageTab(activeMessageTab.threadId)}
            isFocused={getFocusedPane() === 'viewer'}
            isFlashing={isPaneFlashing('viewer')}
            showBackButton={false}
            onBack={() => { activeMessageTabId = null }}
            inFocusMode={false}
            focusModeKind={null}
            focusedMessageIdInFocus={null}
            onToggleThreadFocus={toggleThreadFocus}
            onToggleMessageFocus={toggleMessageFocus}
          />
          </div>
        </div>
      {/if}
    </section>

    <!-- List Resize Handle -->
    {#if getLayoutMode() === 'full' && !tabMode}
    <button
      type="button"
      class="w-1 cursor-col-resize hover:bg-primary/20 active:bg-primary/40 transition-colors border-0 p-0 {isResizingList
        ? 'bg-primary/40'
        : ''}"
      onmousedown={startResizeList}
      aria-label={$_('aria.resizeMessageList')}
    ></button>
    {/if}

    <!-- Conversation Viewer (classic layout only — in tab mode the
         active tab's viewer lives inside the list section above) -->
    {#if !tabMode}
    <main
      class="{viewerIsOverlay ? `responsive-viewer-overlay bg-background ${viewerIsVisible ? 'responsive-viewer-visible' : ''}` : 'flex-1 min-w-0 bg-background'}"
      role="presentation"
      data-pane="viewer"
      onclick={() => handlePaneClick('viewer')}
    >
      <ConversationViewer
        bind:this={viewerRef}
        threadId={selectedThreadId}
        folderId={selectedConversationFolderId}
        folderType={selectedFolderType}
        accountId={selectedConversationAccountId}
        onReply={handleReply}
        onComposeToAddress={handleComposeToAddress}
        onEditDraft={handleEditDraft}
        onActionComplete={(autoSelectNext) => messageListRef?.handleActionComplete(autoSelectNext)}
        isFocused={getFocusedPane() === 'viewer'}
        isFlashing={isPaneFlashing('viewer')}
        showBackButton={isResponsive()}
        onBack={() => { focusMode = 'off'; focusedMessageIdInFocus = null; hideViewer() }}
        inFocusMode={focusMode !== 'off'}
        focusModeKind={focusMode === 'off' ? null : focusMode}
        focusedMessageIdInFocus={focusedMessageIdInFocus}
        onToggleThreadFocus={toggleThreadFocus}
        onToggleMessageFocus={toggleMessageFocus}
      />
    </main>
    {/if}
    </div>
  </div>
</div>

<!-- Resize cursor overlay when dragging -->
{#if isResizingSidebar || isResizingList}
  <div class="fixed inset-0 cursor-col-resize z-50"></div>
{/if}

<!-- Toast notifications -->
<ToastContainer />

<!-- Undo Send countdown toast (distinct from the usual toast stack) -->
<UndoSendToast onUndo={handleUndoSend} />

<!-- Slim sent snackbar with View message -->
<SentSnackbar
  visible={sentSnackbar !== null}
  subject={sentSnackbar?.subject ?? ''}
  onView={() => { sentSnackbar = null; handleUnifiedFolderSelect('sent', $_('sidebar.sent')) }}
  onClose={() => { sentSnackbar = null }}
/>

<!-- Spellcheck right-click suggestion menu (composer) -->
<SpellSuggestionMenu />

<!-- Per-extension settings dialog dispatcher (Settings → Extensions → Edit) -->
<ExtensionSettingsDialog />

<!-- Composer Modal -->
{#if showComposer && composerAccountId}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
    <div class="{getLayoutMode() === 'narrow' ? 'w-full h-full bg-background overflow-hidden' : 'w-full max-w-4xl h-[80vh] bg-background rounded-lg shadow-xl overflow-hidden'}">
      <Composer
        accountId={composerAccountId}
        initialMessage={composerInitialMessage}
        draftId={composerDraftId}
        imagesLoaded={composerImagesLoaded}
        onClose={closeComposer}
        onSent={closeComposer}
      />
    </div>
  </div>
{/if}

<!-- Message Dialog (preview pane off + open mode Dialog) -->
{#if tabMode && dialogMessage}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
    <div class="{getLayoutMode() === 'narrow' ? 'w-full h-full bg-background overflow-hidden flex flex-col' : 'w-full max-w-4xl h-[80vh] bg-background rounded-lg shadow-xl overflow-hidden flex flex-col'}">
      <!-- Dialog header: subject + view switcher + close -->
      <div class="flex flex-shrink-0 items-center gap-2 border-b border-border bg-muted/30 px-3 py-2">
        <span class="min-w-0 flex-1 truncate text-sm font-medium" title={dialogMessage.subject}>
          {dialogMessage.subject}
        </span>
        <ViewSwitcher current="dialog" onSwitch={moveDialogMessageTo} />
        <button
          type="button"
          class="flex-shrink-0 rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
          aria-label={$_('window.closeWindow')}
          title={$_('window.closeWindow')}
          onclick={closeMessageDialog}
        >
          <Icon icon="mdi:close" class="w-4 h-4" />
        </button>
      </div>
      <div class="flex-1 min-h-0">
        <ConversationViewer
          bind:this={dialogViewerRef}
          threadId={dialogMessage.threadId}
          folderId={dialogMessage.folderId}
          folderType={selectedFolderType}
          accountId={dialogMessage.accountId}
          onReply={handleReply}
          onComposeToAddress={handleComposeToAddress}
          onEditDraft={handleEditDraft}
          onActionComplete={(autoSelectNext) => {
            messageListRef?.handleActionComplete(autoSelectNext)
            if (autoSelectNext) closeMessageDialog()
          }}
          onConversationDeleted={closeMessageDialog}
          showBackButton={false}
          inFocusMode={false}
          focusModeKind={null}
          focusedMessageIdInFocus={null}
          onToggleThreadFocus={toggleThreadFocus}
          onToggleMessageFocus={toggleMessageFocus}
        />
      </div>
    </div>
  </div>
{/if}

<!-- Shutdown Overlay -->
{#if isShuttingDown}
  <div class="fixed inset-0 z-[100] flex items-center justify-center bg-black/80">
    <p class="text-white/90 text-sm font-medium">{$_('window.shuttingDown')}</p>
  </div>
{/if}

<!-- Terms Acceptance Dialog -->
<TermsDialog bind:open={showTermsDialog} onAccept={handleTermsAccepted} />

<!-- Launch-time OAuth credentials warning. Shows on every launch when one
     or more provider credentials weren't compiled in, unless the user
     opts out via "Don't show again". -->
<OAuthMissingDialog
  bind:open={showOAuthMissingDialog}
  oauthStatus={oauthBuildStatus}
  onDismiss={dismissOAuthWarning}
/>

<!-- Per-version release announcement. OK click records acknowledgement;
     closing without OK leaves the version unrecorded so the dialog
     fires again next launch. -->
<WhatsNewDialog
  bind:open={showWhatsNewDialog}
  onAcknowledge={acknowledgeWhatsNew}
/>

<!-- Certificate TOFU Dialog (for background sync cert errors) -->
<CertificateDialog
  bind:open={showCertDialog}
  certificate={pendingCertificate}
  onAcceptOnce={handleBgCertAcceptOnce}
  onAcceptPermanently={handleBgCertAcceptPermanently}
  onDecline={handleBgCertDecline}
/>

<!-- Flatpak Filesystem Permission Dialog -->
<AlertDialog.Root bind:open={showFlatpakFsDialog}>
  <AlertDialog.Content>
    <AlertDialog.Header>
      <AlertDialog.Title>{$_('attachment.flatpakOpenTitle')}</AlertDialog.Title>
      <AlertDialog.Description>
        <p class="mb-3">{flatpakFsDialogContext === 'saveAll' ? $_('attachment.flatpakSaveAllDescription') : $_('attachment.flatpakOpenDescription')}</p>
        <pre class="mb-3 rounded bg-muted p-2 text-sm overflow-x-auto"><code>flatpak override --user --filesystem=home net.absons.daak</code></pre>
        <p class="mb-3 text-sm text-destructive">{$_('attachment.flatpakOpenSecurityWarning')}</p>
        <p class="text-sm text-muted-foreground">{flatpakFsDialogContext === 'saveAll' ? $_('attachment.flatpakSaveAllAlternative') : $_('attachment.flatpakOpenAlternative')}</p>
      </AlertDialog.Description>
    </AlertDialog.Header>
    <AlertDialog.Footer>
      <AlertDialog.Action onclick={() => showFlatpakFsDialog = false}>{$_('common.ok')}</AlertDialog.Action>
    </AlertDialog.Footer>
  </AlertDialog.Content>
</AlertDialog.Root>
