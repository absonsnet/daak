<script lang="ts">
  import { onMount } from 'svelte'
  import Icon from '@iconify/svelte'
  import * as Dialog from '$lib/components/ui/dialog'
  import * as Tabs from '$lib/components/ui/tabs'
  import { Button } from '$lib/components/ui/button'
  // @ts-ignore - wailsjs path
  import { GetReadReceiptResponsePolicy, SetReadReceiptResponsePolicy, GetMarkAsReadDelay, SetMarkAsReadDelay, GetMessageListDensity, SetMessageListDensity, GetThemeMode, SetThemeMode, GetShowTitleBar, SetShowTitleBar, GetShowUnreadBadge, SetShowUnreadBadge, GetRunBackground, SetRunBackground, GetStartHidden, SetStartHidden, GetAutostart, SetAutostart, GetLanguage, SetLanguage, GetComposerMode, SetComposerMode, GetMailtoMode, SetMailtoMode, GetComposerFormat, SetComposerFormat, GetUndoSendDelay, SetUndoSendDelay, GetNativeTitleBar, SetNativeTitleBar, GetAlwaysLoadImages, SetAlwaysLoadImages, GetDarkMailContent, SetDarkMailContent, GetDarkComposerBody, SetDarkComposerBody, GetAccentBarUnread, SetAccentBarUnread, GetAccentUnreadStyle, SetAccentUnreadStyle, GetShowMessageListCircles, SetShowMessageListCircles, GetShowMessageListProfilePics, SetShowMessageListProfilePics, GetAlwaysShowMessageCheckbox, SetAlwaysShowMessageCheckbox, GetShowViewerCircles, SetShowViewerCircles, GetShowMessagePreviewPane, SetShowMessagePreviewPane, GetMessageOpenMode, SetMessageOpenMode, GetSpellcheckEnabled, SetSpellcheckEnabled, GetSpellcheckLanguages, SetSpellcheckLanguages, GetWindowControlsSide, SetWindowControlsSide, GetCodeChipClickMode, SetCodeChipClickMode, GetAttentionEnabled, SetAttentionEnabled, GetAttentionTypeEnabled, SetAttentionTypeEnabled, GetOtpClipboardClearSeconds, SetOtpClipboardClearSeconds, GetInboxTabDefault, SetInboxTabDefault, GetTranslationSettings, SetTranslationEnabled, SetTranslationMode, SetTranslationTargetLanguage, SetTranslationOnlineProvider, SetTranslationOnlineConsent, SetDeepLAPIKey, GetNotificationPermissionStatus, OpenNotificationSettings, QuitApp } from '../../../../wailsjs/go/app/App.js'
  import { addToast } from '$lib/stores/toast'
  import { setMessageListDensity as updateDensityStore, setThemeMode as updateThemeStore, setShowTitleBar as updateShowTitleBarStore, setShowUnreadBadge as updateShowUnreadBadgeStore, setRunBackground as updateRunBackgroundStore, setStartHidden as updateStartHiddenStore, setAutostart as updateAutostartStore, setLanguage as updateLanguageStore, setComposerMode as updateComposerModeStore, setMailtoMode as updateMailtoModeStore, setComposerFormat as updateComposerFormatStore, setNativeTitleBar as updateNativeTitleBarStore, setAlwaysLoadImages as updateAlwaysLoadImagesStore, setDarkMailContent as updateDarkMailContentStore, setDarkComposerBody as updateDarkComposerBodyStore, setAccentBarUnread as updateAccentBarUnreadStore, setAccentUnreadStyle as updateAccentUnreadStyleStore, setShowMessageListCircles as updateShowMessageListCirclesStore, setShowMessageListProfilePics as updateShowMessageListProfilePicsStore, setAlwaysShowMessageCheckbox as updateAlwaysShowMessageCheckboxStore, setShowViewerCircles as updateShowViewerCirclesStore, setShowMessagePreviewPane as updateShowMessagePreviewPaneStore, setMessageOpenMode as updateMessageOpenModeStore, setSpellcheckEnabled as updateSpellcheckEnabledStore, setSpellcheckLanguages as updateSpellcheckLanguagesStore, setWindowControlsSide as updateWindowControlsSideStore, setCodeChipClickMode as updateCodeChipClickModeStore, setAttentionEnabled as updateAttentionEnabledStore, setAttentionTypeEnabled as updateAttentionTypeEnabledStore, setOtpClipboardClearSeconds as updateOtpClipboardClearSecondsStore, type MessageListDensity, type ThemeMode, type ComposerMode, type ComposerFormat, type WindowControlsSide, type MessageOpenMode } from '$lib/stores/settings.svelte'
  import { syncSpellcheckLanguagesIfActive, defaultSpellcheckLanguages } from '$lib/spellcheck/settings'
  import { getHideUnifiedSingleAccount, setHideUnifiedSingleAccount } from '$lib/stores/sidebarSettings.svelte'
  import { applyThemeFromMode } from '$lib/stores/theme.svelte'
  import { dialogGuardOpen, dialogGuardClose } from '$lib/stores/dialogGuard'
  import { _ } from '$lib/i18n'
  import ConfirmDialog from '$lib/components/ui/confirm-dialog/ConfirmDialog.svelte'
  import GeneralTab from './GeneralTab.svelte'
  import ComposerTab from './ComposerTab.svelte'
  import ImagesTab from './ImagesTab.svelte'
  import AccountsTab from './AccountsTab.svelte'
  import ContactsTab from './ContactsTab.svelte'
  import ExtensionsTab from './ExtensionsTab.svelte'
  import AboutTab from './AboutTab.svelte'
  import MessagesTab from './MessagesTab.svelte'
  import AttentionTab from './AttentionTab.svelte'
  import CategoriesTab from './CategoriesTab.svelte'
  import TranslationTab from './TranslationTab.svelte'
  import SubscriptionsTab from './SubscriptionsTab.svelte'

  interface Props {
    /** Whether the dialog is open */
    open?: boolean
    /** Callback when dialog should close */
    onClose?: () => void
  }

  let {
    open = $bindable(false),
    onClose,
  }: Props = $props()

  // Settings state
  let readReceiptResponsePolicy = $state<string>('ask')
  let markAsReadDelaySeconds = $state<number>(1) // Display in seconds, store in ms
  let messageListDensity = $state<string>('standard')
  let themeMode = $state<string>('system')
  let showTitleBar = $state<boolean>(true)
  let runBackground = $state<boolean>(false)
  let startHidden = $state<boolean>(false)
  let showUnreadBadge = $state<boolean>(true)
  let autostart = $state<boolean>(false)
  let language = $state<string>('')
  let composerMode = $state<string>('inline')
  let mailtoMode = $state<string>('inline')
  let composerFormat = $state<string>('rich')
  let undoSendDelay = $state<number>(5)
  let spellcheckEnabled = $state<boolean>(true)
  let spellcheckLanguages = $state<string[]>([])
  let nativeTitleBar = $state<boolean>(false)
  let windowControlsSide = $state<string>('auto')
  let alwaysLoadImages = $state<boolean>(false)
  let darkMailContent = $state<boolean>(false)
  let darkComposerBody = $state<boolean>(false)
  let accentBarUnread = $state<boolean>(false)
  let accentUnreadStyle = $state<string>('dot')
  let showMessageListCircles = $state<boolean>(true)
  let showMessageListProfilePics = $state<boolean>(false)
  let alwaysShowMessageCheckbox = $state<boolean>(false)
  let showViewerCircles = $state<boolean>(true)
  let showMessagePreviewPane = $state<boolean>(true)
  let messageOpenMode = $state<string>('tab')
  let hideUnifiedSingleAccount = $state<boolean>(false)
  let codeChipClickMode = $state<string>('copy_mark_read_delete')
  let otpClipboardClearSeconds = $state<number>(60)
  let attentionEnabled = $state<boolean>(true)
  let attentionTypeVerificationCode = $state<boolean>(true)
  let attentionTypeFlightChanged = $state<boolean>(true)
  let attentionTypePaymentFailed = $state<boolean>(true)
  let attentionTypeDeliveryProblem = $state<boolean>(true)
  let inboxTabDefault = $state<string>('all')
  // macOS notification permission state for the General tab row (null
  // until loaded; the row stays hidden on non-macOS).
  let notificationPermission = $state<{ status: string; platform: string; canOpenSystemSettings: boolean } | null>(null)
  let translateEnabled = $state<boolean>(true)
  let translateMode = $state<string>('prefer_offline')
  let translateTargetLanguage = $state<string>('system')
  let translateOnlineProvider = $state<string>('google')
  let translateConsentGoogle = $state<boolean>(false)
  let translateConsentDeepL = $state<boolean>(false)
  let deeplKeyInput = $state<string>('')
  let deeplKeyCleared = $state<boolean>(false)
  let deeplKeySet = $state<boolean>(false)
  let originalNativeTitleBar = false
  // Snapshot of the saved theme at dialog open time. Used to revert live preview
  // if the dialog closes without Save (Cancel / ESC / click-outside).
  let originalThemeMode = ''
  let hasSaved = $state(false)
  let showRestartDialog = $state(false)
  let loading = $state(true)
  let saving = $state(false)
  let activeTab = $state('general')

  // Live theme preview: apply the picker's current value to the document
  // immediately so the user sees what each theme looks like before saving.
  // The revert path is in handleOpenChange when the dialog closes unsaved.
  $effect(() => {
    if (loading || !themeMode) return
    applyThemeFromMode(themeMode as ThemeMode)
  })

  // Load settings on mount
  onMount(async () => {
    await loadSettings()
  })

  // Also load when dialog opens
  $effect(() => {
    if (open) {
      loadSettings()
    }
  })

  // Activate the dialog guard while open: suppresses background refreshes
  // and routes global keyboard shortcuts (e.g. Ctrl+A) to the dialog inputs
  // instead of the message list / viewer behind it.
  $effect(() => {
    if (open) {
      dialogGuardOpen()
      return () => dialogGuardClose()
    }
  })

  async function loadSettings() {
    loading = true
    hasSaved = false
    try {
      const [policy, delayMs, density, theme, titleBar, runBg, startHid, showBadge, autoSt, lang, comp, mail, compFmt, undoDelay, nativeTB, alwaysImages, darkMail, darkComposer, accentBar, accentStyle, listCircles, listProfilePics, alwaysCheckbox, viewerCircles, scEnabled, scLangs, wcSide, previewPane, openMode, chipMode, otpClearSecs, attnEnabled, attnCode, attnFlight, attnPayment, attnDelivery, inboxTab, trSettings, notifPerm] = await Promise.all([
        GetReadReceiptResponsePolicy(),
        GetMarkAsReadDelay(),
        GetMessageListDensity(),
        GetThemeMode(),
        GetShowTitleBar(),
        GetRunBackground(),
        GetStartHidden(),
        GetShowUnreadBadge(),
        GetAutostart(),
        GetLanguage(),
        GetComposerMode(),
        GetMailtoMode(),
        GetComposerFormat(),
        GetUndoSendDelay(),
        GetNativeTitleBar(),
        GetAlwaysLoadImages(),
        GetDarkMailContent(),
        GetDarkComposerBody(),
        GetAccentBarUnread(),
        GetAccentUnreadStyle(),
        GetShowMessageListCircles(),
        GetShowMessageListProfilePics(),
        GetAlwaysShowMessageCheckbox(),
        GetShowViewerCircles(),
        GetSpellcheckEnabled(),
        GetSpellcheckLanguages(),
        GetWindowControlsSide(),
        GetShowMessagePreviewPane(),
        GetMessageOpenMode(),
        GetCodeChipClickMode(),
        GetOtpClipboardClearSeconds(),
        GetAttentionEnabled(),
        GetAttentionTypeEnabled('verification_code'),
        GetAttentionTypeEnabled('flight_changed'),
        GetAttentionTypeEnabled('payment_failed'),
        GetAttentionTypeEnabled('delivery_problem'),
        GetInboxTabDefault(),
        GetTranslationSettings(''),
        GetNotificationPermissionStatus(),
      ])
      readReceiptResponsePolicy = policy
      // Convert ms to seconds for display
      markAsReadDelaySeconds = delayMs < 0 ? -1 : delayMs / 1000
      messageListDensity = density
      themeMode = theme
      originalThemeMode = theme
      showTitleBar = titleBar
      runBackground = runBg
      startHidden = startHid
      showUnreadBadge = showBadge ?? true
      autostart = autoSt
      language = lang
      composerMode = comp || 'inline'
      mailtoMode = mail || 'inline'
      composerFormat = compFmt || 'rich'
      undoSendDelay = undoDelay ?? 5
      spellcheckEnabled = scEnabled ?? true
      // Empty stored list = "use defaults" — show the same set the engine uses.
      spellcheckLanguages = (scLangs && scLangs.length) ? scLangs : defaultSpellcheckLanguages()
      nativeTitleBar = nativeTB ?? false
      windowControlsSide = wcSide || 'auto'
      alwaysLoadImages = alwaysImages ?? false
      darkMailContent = darkMail ?? false
      darkComposerBody = darkComposer ?? false
      accentBarUnread = accentBar ?? false
      accentUnreadStyle = accentStyle || 'dot'
      showMessageListCircles = listCircles ?? true
      showMessageListProfilePics = listProfilePics ?? false
      alwaysShowMessageCheckbox = alwaysCheckbox ?? false
      showViewerCircles = viewerCircles ?? true
      showMessagePreviewPane = previewPane ?? true
      messageOpenMode = openMode || 'tab'
      hideUnifiedSingleAccount = getHideUnifiedSingleAccount()
      codeChipClickMode = chipMode || 'copy_mark_read_delete'
      otpClipboardClearSeconds = otpClearSecs ?? 60
      attentionEnabled = attnEnabled ?? true
      attentionTypeVerificationCode = attnCode ?? true
      attentionTypeFlightChanged = attnFlight ?? true
      attentionTypePaymentFailed = attnPayment ?? true
      attentionTypeDeliveryProblem = attnDelivery ?? true
      inboxTabDefault = inboxTab || 'all'
      notificationPermission = notifPerm ?? null
      if (trSettings) {
        translateEnabled = trSettings.enabled ?? true
        translateMode = trSettings.mode || 'prefer_offline'
        translateTargetLanguage = trSettings.targetLanguage || 'system'
        translateOnlineProvider = trSettings.onlineProvider || 'google'
        translateConsentGoogle = trSettings.consents?.google ?? false
        translateConsentDeepL = trSettings.consents?.deepl ?? false
        deeplKeySet = trSettings.deeplKeySet ?? false
        deeplKeyInput = ''
        deeplKeyCleared = false
      }
      originalNativeTitleBar = nativeTitleBar
    } catch (err) {
      console.error('Failed to load settings:', err)
    } finally {
      loading = false
    }
  }

  async function handleSave() {
    saving = true
    try {
      // Convert seconds to ms for storage
      const delayMs = markAsReadDelaySeconds < 0 ? -1 : Math.round(markAsReadDelaySeconds * 1000)

      // Save settings sequentially to avoid SQLite lock conflicts
      await SetReadReceiptResponsePolicy(readReceiptResponsePolicy)
      await SetMarkAsReadDelay(delayMs)
      await SetMessageListDensity(messageListDensity)
      await SetThemeMode(themeMode)
      await SetShowTitleBar(showTitleBar)
      await SetRunBackground(runBackground)
      await SetStartHidden(startHidden)
      await SetShowUnreadBadge(showUnreadBadge)
      await SetAutostart(autostart)
      if (language) {
        await SetLanguage(language)
      }
      await SetComposerMode(composerMode)
      await SetMailtoMode(mailtoMode)
      await SetComposerFormat(composerFormat)
      await SetUndoSendDelay(undoSendDelay)
      await SetSpellcheckEnabled(spellcheckEnabled)
      await SetSpellcheckLanguages(spellcheckLanguages)
      await SetNativeTitleBar(nativeTitleBar)
      await SetWindowControlsSide(windowControlsSide)
      await SetAlwaysLoadImages(alwaysLoadImages)
      await SetDarkMailContent(darkMailContent)
      await SetDarkComposerBody(darkComposerBody)
      await SetAccentBarUnread(accentBarUnread)
      await SetAccentUnreadStyle(accentUnreadStyle)
      await SetShowMessageListCircles(showMessageListCircles)
      await SetShowMessageListProfilePics(showMessageListProfilePics)
      await SetAlwaysShowMessageCheckbox(alwaysShowMessageCheckbox)
      await SetShowViewerCircles(showViewerCircles)
      await SetShowMessagePreviewPane(showMessagePreviewPane)
      await SetMessageOpenMode(messageOpenMode)
      setHideUnifiedSingleAccount(hideUnifiedSingleAccount)
      await SetCodeChipClickMode(codeChipClickMode)
      await SetOtpClipboardClearSeconds(otpClipboardClearSeconds)
      await SetAttentionEnabled(attentionEnabled)
      await SetAttentionTypeEnabled('verification_code', attentionTypeVerificationCode)
      await SetAttentionTypeEnabled('flight_changed', attentionTypeFlightChanged)
      await SetAttentionTypeEnabled('payment_failed', attentionTypePaymentFailed)
      await SetAttentionTypeEnabled('delivery_problem', attentionTypeDeliveryProblem)
      await SetInboxTabDefault(inboxTabDefault)
      await SetTranslationEnabled(translateEnabled)
      await SetTranslationMode(translateMode)
      await SetTranslationTargetLanguage(translateTargetLanguage)
      await SetTranslationOnlineProvider(translateOnlineProvider)
      await SetTranslationOnlineConsent('google', translateConsentGoogle)
      await SetTranslationOnlineConsent('deepl', translateConsentDeepL)
      // The DeepL key field is empty unless the user typed a new key:
      // only save when a key was typed, or when it was cleared.
      if (deeplKeyInput) {
        await SetDeepLAPIKey(deeplKeyInput)
      } else if (deeplKeyCleared) {
        await SetDeepLAPIKey('')
      }
      // Update the reactive stores so UI updates immediately
      updateDensityStore(messageListDensity as MessageListDensity)
      updateThemeStore(themeMode as ThemeMode)
      updateShowTitleBarStore(showTitleBar)
      updateRunBackgroundStore(runBackground)
      updateStartHiddenStore(startHidden)
      updateShowUnreadBadgeStore(showUnreadBadge)
      updateAutostartStore(autostart)
      if (language) {
        updateLanguageStore(language)
      }
      updateComposerModeStore(composerMode as ComposerMode)
      updateMailtoModeStore(mailtoMode as ComposerMode)
      updateComposerFormatStore(composerFormat as ComposerFormat)
      updateSpellcheckEnabledStore(spellcheckEnabled)
      updateSpellcheckLanguagesStore(spellcheckLanguages)
      syncSpellcheckLanguagesIfActive() // live-apply to an open composer; stays lazy otherwise
      updateNativeTitleBarStore(nativeTitleBar)
      updateWindowControlsSideStore(windowControlsSide as WindowControlsSide)
      updateAlwaysLoadImagesStore(alwaysLoadImages)
      updateDarkMailContentStore(darkMailContent)
      updateDarkComposerBodyStore(darkComposerBody)
      updateAccentBarUnreadStore(accentBarUnread)
      updateAccentUnreadStyleStore(accentUnreadStyle)
      updateShowMessageListCirclesStore(showMessageListCircles)
      updateShowMessageListProfilePicsStore(showMessageListProfilePics)
      updateAlwaysShowMessageCheckboxStore(alwaysShowMessageCheckbox)
      updateShowViewerCirclesStore(showViewerCircles)
      updateShowMessagePreviewPaneStore(showMessagePreviewPane)
      updateMessageOpenModeStore(messageOpenMode as MessageOpenMode)
      updateCodeChipClickModeStore(codeChipClickMode)
      updateOtpClipboardClearSecondsStore(otpClipboardClearSeconds)
      updateAttentionEnabledStore(attentionEnabled)
      updateAttentionTypeEnabledStore('verification_code', attentionTypeVerificationCode)
      updateAttentionTypeEnabledStore('flight_changed', attentionTypeFlightChanged)
      updateAttentionTypeEnabledStore('payment_failed', attentionTypePaymentFailed)
      updateAttentionTypeEnabledStore('delivery_problem', attentionTypeDeliveryProblem)
      addToast({
        type: 'success',
        message: $_('toast.settingsSaved'),
      })
      hasSaved = true
      originalThemeMode = themeMode
      // Show restart dialog if native title bar setting changed
      if (nativeTitleBar !== originalNativeTitleBar) {
        originalNativeTitleBar = nativeTitleBar
        showRestartDialog = true
        return
      }
      open = false
      onClose?.()
    } catch (err) {
      console.error('Failed to save settings:', err)
      addToast({
        type: 'error',
        message: $_('toast.failedToSaveSettings'),
      })
    } finally {
      saving = false
    }
  }

  function revertLivePreview() {
    if (!hasSaved && originalThemeMode && themeMode !== originalThemeMode) {
      applyThemeFromMode(originalThemeMode as ThemeMode)
    }
  }

  function handleCancel() {
    revertLivePreview()
    open = false
    onClose?.()
  }

  function handleOpenChange(isOpen: boolean) {
    open = isOpen
    if (!isOpen) {
      revertLivePreview()
      onClose?.()
    }
  }
</script>

<Dialog.Root bind:open onOpenChange={handleOpenChange}>
  <Dialog.Content class="max-w-3xl" preventCloseAutoFocus onInteractOutside={(e) => e.preventDefault()}>
    <Dialog.Header>
      <Dialog.Title>{$_('settings.title')}</Dialog.Title>
      <Dialog.Description>
        {$_('settings.description')}
      </Dialog.Description>
    </Dialog.Header>

    {#if loading}
      <div class="flex items-center justify-center py-8">
        <Icon icon="mdi:loading" class="w-6 h-6 animate-spin text-muted-foreground" />
      </div>
    {:else}
      <Tabs.Root bind:value={activeTab} class="w-full">
        <!-- Two rows of six: eleven icon+text tabs no longer fit one row
             without the labels running into each other. h-auto overrides
             the shared Tabs.List fixed h-10 so the second row gets its own
             height instead of overflowing onto the content below. -->
        <Tabs.List class="grid h-auto w-full grid-cols-6">
          <Tabs.Trigger value="general" class="flex items-center gap-2">
            <span class="inline-flex w-4 h-4 items-center justify-center shrink-0"><Icon icon="lucide:settings-2" width="16" height="16" /></span>
            {$_('settings.general')}
          </Tabs.Trigger>
          <Tabs.Trigger value="composer" class="flex items-center gap-2">
            <span class="inline-flex w-4 h-4 items-center justify-center shrink-0"><Icon icon="lucide:square-pen" width="46" height="46" /></span>
            {$_('settings.composer')}
          </Tabs.Trigger>
          <Tabs.Trigger value="images" class="flex items-center gap-2">
            <span class="inline-flex w-4 h-4 items-center justify-center shrink-0"><Icon icon="lucide:image" width="16" height="16" /></span>
            {$_('settings.images')}
          </Tabs.Trigger>
          <Tabs.Trigger value="messages" class="flex items-center gap-2">
            <span class="inline-flex w-4 h-4 items-center justify-center shrink-0"><Icon icon="lucide:message-square-text" width="16" height="16" /></span>
            {$_('settings.messages')}
          </Tabs.Trigger>
          <Tabs.Trigger value="attention" class="flex items-center gap-2">
            <span class="inline-flex w-4 h-4 items-center justify-center shrink-0"><Icon icon="lucide:bell-ring" width="16" height="16" /></span>
            {$_('settings.attention')}
          </Tabs.Trigger>
          <Tabs.Trigger value="categories" class="flex items-center gap-2">
            <span class="inline-flex w-4 h-4 items-center justify-center shrink-0"><Icon icon="lucide:layers" width="16" height="16" /></span>
            {$_('settings.categories')}
          </Tabs.Trigger>
          <Tabs.Trigger value="translation" class="flex items-center gap-2">
            <span class="inline-flex w-4 h-4 items-center justify-center shrink-0"><Icon icon="lucide:languages" width="16" height="16" /></span>
            {$_('settings.translation')}
          </Tabs.Trigger>
          <Tabs.Trigger value="subscriptions" class="flex items-center gap-2">
            <span class="inline-flex w-4 h-4 items-center justify-center shrink-0"><Icon icon="lucide:mail-minus" width="16" height="16" /></span>
            {$_('settings.subscriptions')}
          </Tabs.Trigger>
          <Tabs.Trigger value="accounts" class="flex items-center gap-2">
            <span class="inline-flex w-4 h-4 items-center justify-center shrink-0"><Icon icon="lucide:mails" width="16" height="16" /></span>
            {$_('settings.accounts')}
          </Tabs.Trigger>
          <Tabs.Trigger value="contacts" class="flex items-center gap-2">
            <span class="inline-flex w-4 h-4 items-center justify-center shrink-0"><Icon icon="lucide:contact" width="16" height="16" /></span>
            {$_('settings.contacts')}
          </Tabs.Trigger>
          <Tabs.Trigger value="extensions" class="flex items-center gap-2">
            <span class="inline-flex w-4 h-4 items-center justify-center shrink-0"><Icon icon="lucide:puzzle" width="16" height="16" /></span>
            {$_('settings.extensions')}
          </Tabs.Trigger>
          <Tabs.Trigger value="about" class="flex items-center gap-2">
            <span class="inline-flex w-4 h-4 items-center justify-center shrink-0"><Icon icon="lucide:info" width="16" height="16" /></span>
            {$_('settings.about')}
          </Tabs.Trigger>
        </Tabs.List>

        <div class="mt-4 h-[350px] overflow-y-auto pl-1 pr-3">
          <Tabs.Content value="general" class="mt-0">
            <GeneralTab
              bind:markAsReadDelaySeconds
              bind:messageListDensity
              bind:themeMode
              bind:nativeTitleBar
              bind:showTitleBar
              bind:windowControlsSide
              bind:runBackground
              bind:startHidden
              bind:showUnreadBadge
              bind:autostart
              bind:language
              onDelayChange={(v) => markAsReadDelaySeconds = v}
              onDensityChange={(v) => messageListDensity = v}
              onThemeChange={(v) => themeMode = v}
              onTitleBarChange={(ntb, stb) => { nativeTitleBar = ntb; showTitleBar = stb }}
              onWindowControlsSideChange={(v) => windowControlsSide = v}
              onRunBackgroundChange={(v) => { runBackground = v; if (!v) startHidden = false }}
              onStartHiddenChange={(v) => { startHidden = v; if (v) runBackground = true }}
              onShowUnreadBadgeChange={(v) => { showUnreadBadge = v }}
              onAutostartChange={(v) => autostart = v}
              onLanguageChange={(v) => language = v}
              bind:accentBarUnread
              bind:accentUnreadStyle
              bind:showMessageListCircles
              bind:showMessageListProfilePics
              bind:alwaysShowMessageCheckbox
              bind:showViewerCircles
              bind:showMessagePreviewPane
              bind:messageOpenMode
              bind:hideUnifiedSingleAccount
              bind:darkMailContent
              bind:darkComposerBody
              bind:notificationPermission
              onOpenNotificationSettings={async () => {
                try {
                  await OpenNotificationSettings()
                } catch (err) {
                  console.error('Failed to open notification settings:', err)
                }
              }}
            />
          </Tabs.Content>

          <Tabs.Content value="composer" class="mt-0">
            <ComposerTab
              bind:composerMode
              bind:mailtoMode
              bind:composerFormat
              bind:readReceiptResponsePolicy
              bind:undoSendDelay
              bind:spellcheckEnabled
              bind:spellcheckLanguages
              onComposerModeChange={(v) => { composerMode = v; if (v === 'detached') mailtoMode = 'detached' }}
              onMailtoModeChange={(v) => mailtoMode = v}
              onFormatChange={(v) => composerFormat = v}
              onPolicyChange={(v) => readReceiptResponsePolicy = v}
              onUndoSendDelayChange={(v) => undoSendDelay = v}
              onSpellcheckEnabledChange={(v) => spellcheckEnabled = v}
              onSpellcheckLanguagesChange={(v) => spellcheckLanguages = v}
            />
          </Tabs.Content>

          <Tabs.Content value="images" class="mt-0">
            <ImagesTab
              bind:alwaysLoadImages
              onAlwaysLoadImagesChange={(v) => alwaysLoadImages = v}
            />
          </Tabs.Content>

          <Tabs.Content value="messages" class="mt-0">
            <MessagesTab
              bind:codeChipClickMode
              bind:otpClipboardClearSeconds
              onCodeChipClickModeChange={(v) => codeChipClickMode = v}
              onOtpClipboardClearSecondsChange={(v) => otpClipboardClearSeconds = v}
            />
          </Tabs.Content>

          <Tabs.Content value="attention" class="mt-0">
            <AttentionTab
              bind:attentionEnabled
              bind:attentionTypeVerificationCode
              bind:attentionTypeFlightChanged
              bind:attentionTypePaymentFailed
              bind:attentionTypeDeliveryProblem
              onAttentionEnabledChange={(v) => attentionEnabled = v}
              onAttentionTypeChange={(type, v) => {
                if (type === 'verification_code') attentionTypeVerificationCode = v
                if (type === 'flight_changed') attentionTypeFlightChanged = v
                if (type === 'payment_failed') attentionTypePaymentFailed = v
                if (type === 'delivery_problem') attentionTypeDeliveryProblem = v
              }}
            />
          </Tabs.Content>

          <Tabs.Content value="categories" class="mt-0">
            <CategoriesTab
              bind:inboxTabDefault
              onInboxTabDefaultChange={(v) => inboxTabDefault = v}
            />
          </Tabs.Content>

          <Tabs.Content value="translation" class="mt-0">
            <TranslationTab
              bind:translateEnabled
              bind:translateMode
              bind:translateTargetLanguage
              bind:translateOnlineProvider
              bind:translateConsentGoogle
              bind:translateConsentDeepL
              bind:deeplKeyInput
              bind:deeplKeyCleared
              bind:deeplKeySet
              onTranslateEnabledChange={(v) => translateEnabled = v}
              onTranslateModeChange={(v) => translateMode = v}
              onTranslateTargetLanguageChange={(v) => translateTargetLanguage = v}
              onTranslateOnlineProviderChange={(v) => translateOnlineProvider = v}
              onTranslateConsentChange={(provider, v) => {
                if (provider === 'google') translateConsentGoogle = v
                if (provider === 'deepl') translateConsentDeepL = v
              }}
              onDeeplKeyInputChange={(v) => deeplKeyInput = v}
              onDeeplKeyClearedChange={(v) => deeplKeyCleared = v}
            />
          </Tabs.Content>

          <Tabs.Content value="subscriptions" class="mt-0">
            <SubscriptionsTab />
          </Tabs.Content>

          <Tabs.Content value="accounts" class="mt-0">
            <AccountsTab />
          </Tabs.Content>

          <Tabs.Content value="contacts" class="mt-0">
            <ContactsTab />
          </Tabs.Content>

          <Tabs.Content value="extensions" class="mt-0">
            <ExtensionsTab />
          </Tabs.Content>

          <Tabs.Content value="about" class="mt-0">
            <AboutTab />
          </Tabs.Content>
        </div>
      </Tabs.Root>

      <!-- Actions - show Save/Cancel on General and Composer tabs -->
      {#if activeTab === 'general' || activeTab === 'composer' || activeTab === 'images' || activeTab === 'messages' || activeTab === 'attention' || activeTab === 'categories' || activeTab === 'translation'}
        <div class="flex items-center justify-end gap-2 pt-4 border-t border-border">
          <Button variant="ghost" onclick={handleCancel} disabled={saving}>
            {$_('common.cancel')}
          </Button>
          <Button onclick={handleSave} disabled={saving}>
            {#if saving}
              <Icon icon="mdi:loading" class="w-4 h-4 mr-2 animate-spin" />
            {/if}
            {$_('common.save')}
          </Button>
        </div>
      {:else}
        <div class="flex items-center justify-end gap-2 pt-4 border-t border-border">
          <Button variant="ghost" onclick={handleCancel}>
            {$_('common.close')}
          </Button>
        </div>
      {/if}
    {/if}
  </Dialog.Content>
</Dialog.Root>

<ConfirmDialog
  bind:open={showRestartDialog}
  title={$_('settingsGeneral.restartRequired')}
  description={$_('settingsGeneral.restartRequiredDescription')}
  confirmLabel={$_('settingsGeneral.quitNow')}
  cancelLabel={$_('settingsGeneral.restartLater')}
  onConfirm={() => QuitApp()}
  onCancel={() => { showRestartDialog = false; open = false; onClose?.() }}
/>
