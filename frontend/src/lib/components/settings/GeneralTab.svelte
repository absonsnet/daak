<script lang="ts">
  import Icon from '@iconify/svelte'
  import * as Select from '$lib/components/ui/select'
  import { Label } from '$lib/components/ui/label'
  import { Input } from '$lib/components/ui/input'
  import { Button } from '$lib/components/ui/button'
  import Switch from '$lib/components/ui/switch/Switch.svelte'
  import { _, setLocale } from '$lib/i18n'
  import { supportedLocales } from '$lib/i18n'
  import { getIsDarkActive } from '$lib/stores/theme.svelte'

  interface Props {
    markAsReadDelaySeconds: number
    messageListDensity: string
    themeMode: string
    nativeTitleBar: boolean
    showTitleBar: boolean
    windowControlsSide: string
    runBackground: boolean
    startHidden: boolean
    showUnreadBadge: boolean
    autostart: boolean
    language: string
    onDelayChange: (value: number) => void
    onDensityChange: (value: string) => void
    onThemeChange: (value: string) => void
    onTitleBarChange: (nativeTitleBar: boolean, showTitleBar: boolean) => void
    onWindowControlsSideChange: (value: string) => void
    onRunBackgroundChange: (value: boolean) => void
    onStartHiddenChange: (value: boolean) => void
    onShowUnreadBadgeChange: (value: boolean) => void
    onAutostartChange: (value: boolean) => void
    onLanguageChange: (value: string) => void
    accentBarUnread: boolean
    accentUnreadStyle: string
    showMessageListCircles: boolean
    showMessageListProfilePics: boolean
    alwaysShowMessageCheckbox: boolean
    showViewerCircles: boolean
    darkMailContent: boolean
    darkComposerBody: boolean
    showMessagePreviewPane: boolean
    messageOpenMode: string
    hideUnifiedSingleAccount: boolean
    // macOS notification permission state (null until loaded). The row
    // renders only when the backend says the platform can open the
    // system notification settings page (macOS) — never a macOS-only
    // link on Windows/Linux.
    notificationPermission: { status: string; platform: string; canOpenSystemSettings: boolean } | null
    onOpenNotificationSettings: () => void
  }

  let {
    markAsReadDelaySeconds = $bindable(),
    messageListDensity = $bindable(),
    themeMode = $bindable(),
    nativeTitleBar = $bindable(),
    showTitleBar = $bindable(),
    windowControlsSide = $bindable(),
    runBackground = $bindable(),
    startHidden = $bindable(),
    showUnreadBadge = $bindable(),
    autostart = $bindable(),
    language = $bindable(),
    onDelayChange,
    onDensityChange,
    onThemeChange,
    onTitleBarChange,
    onWindowControlsSideChange,
    onRunBackgroundChange,
    onStartHiddenChange,
    onShowUnreadBadgeChange,
    onAutostartChange,
    onLanguageChange,
    accentBarUnread = $bindable(),
    accentUnreadStyle = $bindable(),
    showMessageListCircles = $bindable(),
    showMessageListProfilePics = $bindable(),
    alwaysShowMessageCheckbox = $bindable(),
    showViewerCircles = $bindable(),
    darkMailContent = $bindable(),
    darkComposerBody = $bindable(),
    showMessagePreviewPane = $bindable(),
    messageOpenMode = $bindable(),
    hideUnifiedSingleAccount = $bindable(),
    notificationPermission = $bindable(),
    onOpenNotificationSettings,
  }: Props = $props()

  // Message list density options
  const densityOptions = $derived([
    { value: 'micro', label: $_('settingsGeneral.densityMicro') },
    { value: 'compact', label: $_('settingsGeneral.densityCompact') },
    { value: 'standard', label: $_('settingsGeneral.densityStandard') },
    { value: 'large', label: $_('settingsGeneral.densityLarge') },
  ])

  // Title bar options
  const titleBarOptions = $derived([
    { value: 'daak', label: $_('settingsGeneral.titleBarDaak'), description: $_('settingsGeneral.titleBarDaakDesc') },
    { value: 'native', label: $_('settingsGeneral.titleBarNative'), description: $_('settingsGeneral.titleBarNativeDesc') },
    { value: 'disable', label: $_('settingsGeneral.titleBarDisable'), description: $_('settingsGeneral.titleBarDisableDesc') },
  ])

  const titleBarValue = $derived(
    nativeTitleBar ? 'native' : showTitleBar ? 'daak' : 'disable'
  )

  // Theme mode options
  const themeModeOptions = $derived([
    { value: 'system', label: $_('settingsGeneral.themeSystem') },
    { value: 'light', label: $_('settingsGeneral.themeLight') },
    { value: 'light-blue', label: $_('settingsGeneral.themeLightBlue') },
    { value: 'light-orange', label: $_('settingsGeneral.themeLightOrange') },
    { value: 'light-balanced', label: $_('settingsGeneral.themeLightBalanced') },
    { value: 'adwaita-light', label: 'Adwaita (Light)' },
    { value: 'breeze-light', label: 'Breeze (Light)' },
    { value: 'catppuccin-latte', label: 'Catppuccin Latte' },
    { value: 'github-light', label: 'GitHub (Light)' },
    { value: 'nord-light', label: 'Nord (Light)' },
    { value: 'pop-light', label: 'Pop! (Light)' },
    { value: 'vs-code-light', label: 'VS Code (Light)' },
    { value: 'yaru-light', label: 'Yaru (Light)' },
    { value: 'dark', label: $_('settingsGeneral.themeDark') },
    { value: 'dark-gray', label: $_('settingsGeneral.themeDarkGray') },
    { value: 'dark-balanced', label: $_('settingsGeneral.themeDarkBalanced') },
    { value: 'adwaita-dark', label: 'Adwaita (Dark)' },
    { value: 'breeze-dark', label: 'Breeze (Dark)' },
    { value: 'catppuccin-frappe', label: 'Catppuccin Frappé' },
    { value: 'catppuccin-macchiato', label: 'Catppuccin Macchiato' },
    { value: 'catppuccin-mocha', label: 'Catppuccin Mocha' },
    { value: 'dracula', label: 'Dracula' },
    { value: 'github-dark', label: 'GitHub (Dark)' },
    { value: 'github-soft-dark', label: 'GitHub (Soft Dark)' },
    { value: 'nord-dark', label: 'Nord (Dark)' },
    { value: 'pop-dark', label: 'Pop! (Dark)' },
    { value: 'tokyo-night', label: 'Tokyo Night' },
    { value: 'vs-code-dark', label: 'VS Code (Dark)' },
    { value: 'yaru-dark', label: 'Yaru (Dark)' },
  ])

  function getDensityLabel(value: string): string {
    return densityOptions.find(opt => opt.value === value)?.label || value
  }

  function getThemeModeLabel(value: string): string {
    return themeModeOptions.find(opt => opt.value === value)?.label || value
  }

  // Language picker
  function getLanguageLabel(code: string): string {
    return supportedLocales.find(l => l.code === code)?.name || code || 'English'
  }

  function handleDensityChange(value: string) {
    messageListDensity = value
    onDensityChange?.(value)
  }

  function handleThemeChange(value: string) {
    themeMode = value
    onThemeChange?.(value)
  }

  function handleTitleBarChange(value: string) {
    switch (value) {
      case 'daak':
        nativeTitleBar = false
        showTitleBar = true
        break
      case 'native':
        nativeTitleBar = true
        showTitleBar = false
        break
      case 'disable':
        nativeTitleBar = false
        showTitleBar = false
        break
    }
    onTitleBarChange?.(nativeTitleBar, showTitleBar)
  }

  function getTitleBarLabel(value: string): string {
    return titleBarOptions.find(opt => opt.value === value)?.label || value
  }

  // Window controls side options (only applies to the Daak title bar)
  const windowControlsSideOptions = $derived([
    { value: 'auto', label: $_('settingsGeneral.windowControlsAuto') },
    { value: 'left', label: $_('settingsGeneral.windowControlsLeft') },
    { value: 'right', label: $_('settingsGeneral.windowControlsRight') },
  ])

  function getWindowControlsSideLabel(value: string): string {
    return windowControlsSideOptions.find(opt => opt.value === value)?.label || value
  }

  function handleWindowControlsSideChange(value: string) {
    windowControlsSide = value
    onWindowControlsSideChange?.(value)
  }

  // Message open mode options (used when the preview pane is off)
  const messageOpenModeOptions = $derived([
    { value: 'tab', label: $_('settingsGeneral.messageOpenModeTab') },
    { value: 'dialog', label: $_('settingsGeneral.messageOpenModeDialog') },
    { value: 'popup', label: $_('settingsGeneral.messageOpenModePopup') },
  ])

  function getMessageOpenModeLabel(value: string): string {
    return messageOpenModeOptions.find(opt => opt.value === value)?.label || value
  }

  function handleMessageOpenModeChange(value: string) {
    messageOpenMode = value
  }

  function handleDelayInput(e: Event) {
    const target = e.target as HTMLInputElement
    const value = parseFloat(target.value)
    markAsReadDelaySeconds = value
    onDelayChange?.(value)
  }

  function handleRunBackgroundChange(value: boolean) {
    runBackground = value
    if (!value) {
      startHidden = false
    }
    onRunBackgroundChange?.(value)
  }

  function handleStartHiddenChange(value: boolean) {
    startHidden = value
    if (value && !runBackground) {
      runBackground = true
      onRunBackgroundChange?.(true)
    }
    onStartHiddenChange?.(value)
  }

  function handleShowUnreadBadgeChange(value: boolean) {
    showUnreadBadge = value
    onShowUnreadBadgeChange?.(value)
  }

  function handleAutostartChange(value: boolean) {
    autostart = value
    onAutostartChange?.(value)
  }

  function handleLanguageChange(value: string) {
    language = value
    // Apply immediately for live preview
    setLocale(value)
    onLanguageChange?.(value)
  }

  function handleOpenNotificationSettings() {
    onOpenNotificationSettings?.()
  }


</script>

<div class="space-y-6">
  <!-- Display Section -->
  <div class="space-y-4">
    <h3 class="text-sm font-medium flex items-center gap-2">
      <Icon icon="mdi:format-size" class="w-4 h-4" />
      {$_('settingsGeneral.display')}
    </h3>

    <div class="space-y-2">
      <Label>{$_('settingsGeneral.titleBar')}</Label>
      <Select.Root value={titleBarValue} onValueChange={handleTitleBarChange}>
        <Select.Trigger>
          <Select.Value>
            {getTitleBarLabel(titleBarValue)}
          </Select.Value>
        </Select.Trigger>
        <Select.Content>
          {#each titleBarOptions as opt (opt.value)}
            <Select.Item value={opt.value} label={opt.label} />
          {/each}
        </Select.Content>
      </Select.Root>
      <p class="text-xs text-muted-foreground">
        {$_('settingsGeneral.titleBarHelp')}
      </p>
    </div>

    {#if titleBarValue === 'daak'}
      <div class="space-y-2">
        <Label>{$_('settingsGeneral.windowControls')}</Label>
        <Select.Root value={windowControlsSide} onValueChange={handleWindowControlsSideChange}>
          <Select.Trigger>
            <Select.Value>
              {getWindowControlsSideLabel(windowControlsSide)}
            </Select.Value>
          </Select.Trigger>
          <Select.Content>
            {#each windowControlsSideOptions as opt (opt.value)}
              <Select.Item value={opt.value} label={opt.label} />
            {/each}
          </Select.Content>
        </Select.Root>
        <p class="text-xs text-muted-foreground">
          {$_('settingsGeneral.windowControlsHelp')}
        </p>
      </div>
    {/if}

    <div class="space-y-2">
      <Label>{$_('settingsGeneral.language')}</Label>
      <Select.Root value={language || 'en'} onValueChange={handleLanguageChange}>
        <Select.Trigger>
          <Select.Value>
            {getLanguageLabel(language || 'en')}
          </Select.Value>
        </Select.Trigger>
        <Select.Content>
          {#each supportedLocales as loc (loc.code)}
            <Select.Item value={loc.code} label={loc.name} />
          {/each}
        </Select.Content>
      </Select.Root>
    </div>

    <div class="space-y-2">
      <Label>{$_('settingsGeneral.theme')}</Label>
      <Select.Root value={themeMode} onValueChange={handleThemeChange}>
        <Select.Trigger>
          <Select.Value placeholder={$_('settingsGeneral.selectTheme')}>
            {getThemeModeLabel(themeMode)}
          </Select.Value>
        </Select.Trigger>
        <Select.Content>
          {#each themeModeOptions as opt (opt.value)}
            <Select.Item value={opt.value} label={opt.label} />
          {/each}
        </Select.Content>
      </Select.Root>
      <p class="text-xs text-muted-foreground">
        {$_('settingsGeneral.themeHelp')}
      </p>
    </div>

    <!-- Dark mail content — only relevant when a dark theme is active -->
    {#if getIsDarkActive()}
      <div class="space-y-2">
        <div class="flex items-center justify-between">
          <div>
            <Label for="dark-mail-content">{$_('settingsGeneral.darkMailContent')}</Label>
            <p class="text-xs text-muted-foreground">
              {$_('settingsGeneral.darkMailContentHelp')}
            </p>
          </div>
          <Switch
            id="dark-mail-content"
            bind:checked={darkMailContent}
          />
        </div>
      </div>

      <!-- Dark composer body — keep the composer message body white by default in dark mode -->
      <div class="space-y-2">
        <div class="flex items-center justify-between">
          <div>
            <Label for="dark-composer-body">{$_('settingsGeneral.darkComposerBody')}</Label>
            <p class="text-xs text-muted-foreground">
              {$_('settingsGeneral.darkComposerBodyHelp')}
            </p>
          </div>
          <Switch
            id="dark-composer-body"
            bind:checked={darkComposerBody}
          />
        </div>
      </div>
    {/if}

    <!-- Show message preview pane — when off, the list fills the window
         and messages open per the open-mode setting below -->
    <div class="space-y-2">
      <div class="flex items-center justify-between">
        <div>
          <Label for="show-message-preview-pane">{$_('settingsGeneral.showMessagePreviewPane')}</Label>
          <p class="text-xs text-muted-foreground">
            {$_('settingsGeneral.showMessagePreviewPaneHelp')}
          </p>
        </div>
        <Switch
          id="show-message-preview-pane"
          bind:checked={showMessagePreviewPane}
        />
      </div>
    </div>

    <!-- Hide the Unified sidebar section with a single account -->
    <div class="space-y-2">
      <div class="flex items-center justify-between">
        <div>
          <Label for="hide-unified-single-account">{$_('settingsGeneral.hideUnifiedSingleAccount')}</Label>
          <p class="text-xs text-muted-foreground">
            {$_('settingsGeneral.hideUnifiedSingleAccountHelp')}
          </p>
        </div>
        <Switch
          id="hide-unified-single-account"
          bind:checked={hideUnifiedSingleAccount}
        />
      </div>
    </div>

    <!-- Message open mode — how a message opens when the preview pane is off -->
    <div class="space-y-2">
      <Label>{$_('settingsGeneral.messageOpenMode')}</Label>      <Select.Root value={messageOpenMode} onValueChange={handleMessageOpenModeChange}>
        <Select.Trigger>
          <Select.Value>
            {getMessageOpenModeLabel(messageOpenMode)}
          </Select.Value>
        </Select.Trigger>
        <Select.Content>
          {#each messageOpenModeOptions as opt (opt.value)}
            <Select.Item value={opt.value} label={opt.label} />
          {/each}
        </Select.Content>
      </Select.Root>
      <p class="text-xs text-muted-foreground">
        {$_('settingsGeneral.messageOpenModeHelp')}
      </p>
    </div>

    <!-- Always show checkbox next to message — reserve the fixed checkbox
         column instead of the hover/swipe slide-reveal -->
    <div class="space-y-2">
      <div class="flex items-center justify-between">
        <div>
          <Label for="always-show-message-checkbox">{$_('settingsGeneral.alwaysShowCheckbox')}</Label>
          <p class="text-xs text-muted-foreground">
            {$_('settingsGeneral.alwaysShowCheckboxHelp')}
          </p>
        </div>
        <Switch
          id="always-show-message-checkbox"
          bind:checked={alwaysShowMessageCheckbox}
        />
      </div>
    </div>

    <!-- Show colored circles in message list -->
    <div class="space-y-2">
      <div class="flex items-center justify-between">
        <div>
          <Label for="show-message-list-circles">{$_('settingsGeneral.showMessageListCircles')}</Label>
          <p class="text-xs text-muted-foreground">
            {$_('settingsGeneral.showMessageListCirclesHelp')}
          </p>
        </div>
        <Switch
          id="show-message-list-circles"
          bind:checked={showMessageListCircles}
        />
      </div>
    </div>

    <!-- Show colored circles in conversation viewer -->
    <div class="space-y-2">
      <div class="flex items-center justify-between">
        <div>
          <Label for="show-viewer-circles">{$_('settingsGeneral.showViewerCircles')}</Label>
          <p class="text-xs text-muted-foreground">
            {$_('settingsGeneral.showViewerCirclesHelp')}
          </p>
        </div>
        <Switch
          id="show-viewer-circles"
          bind:checked={showViewerCircles}
        />
      </div>
    </div>

    <!-- Show contact profile pictures in message list -->
    <div class="space-y-2">
      <div class="flex items-center justify-between">
        <div>
          <Label for="show-message-list-profile-pics">{$_('settingsGeneral.showMessageListProfilePics')}</Label>
          <p class="text-xs text-muted-foreground">
            {$_('settingsGeneral.showMessageListProfilePicsHelp')}
          </p>
        </div>
        <Switch
          id="show-message-list-profile-pics"
          bind:checked={showMessageListProfilePics}
        />
      </div>
    </div>

    <!-- Accent bar for unread messages -->
    <div class="space-y-2">
      <div class="flex items-center justify-between">
        <div>
          <Label for="accent-bar-unread">{$_('settingsGeneral.accentBarUnread')}</Label>
          <p class="text-xs text-muted-foreground">
            {$_('settingsGeneral.accentBarUnreadHelp')}
          </p>
        </div>
        <Switch
          id="accent-bar-unread"
          bind:checked={accentBarUnread}
        />
      </div>
      {#if accentBarUnread}
        <Select.Root value={accentUnreadStyle} onValueChange={(v) => { if (v) accentUnreadStyle = v }}>
          <Select.Trigger>
            <Select.Value>
              {accentUnreadStyle === 'bar' ? $_('settingsGeneral.accentStyleBar') : accentUnreadStyle === 'glowdot' ? $_('settingsGeneral.accentStyleGlowDot') : $_('settingsGeneral.accentStyleDot')}
            </Select.Value>
          </Select.Trigger>
          <Select.Content>
            <Select.Item value="dot" label={$_('settingsGeneral.accentStyleDot')} />
            <Select.Item value="glowdot" label={$_('settingsGeneral.accentStyleGlowDot')} />
            <Select.Item value="bar" label={$_('settingsGeneral.accentStyleBar')} />
          </Select.Content>
        </Select.Root>
      {/if}
    </div>

    <div class="space-y-2">
      <Label>{$_('settingsGeneral.messageListDensity')}</Label>
      <Select.Root value={messageListDensity} onValueChange={handleDensityChange}>
        <Select.Trigger>
          <Select.Value placeholder={$_('settingsGeneral.selectDensity')}>
            {getDensityLabel(messageListDensity)}
          </Select.Value>
        </Select.Trigger>
        <Select.Content>
          {#each densityOptions as opt (opt.value)}
            <Select.Item value={opt.value} label={opt.label} />
          {/each}
        </Select.Content>
      </Select.Root>
      <p class="text-xs text-muted-foreground">
        {$_('settingsGeneral.messageListDensityHelp')}
      </p>
    </div>
  </div>

  <!-- Divider -->
  <div class="border-t border-border"></div>

  <!-- Mark as Read Section -->
  <div class="space-y-4">
    <h3 class="text-sm font-medium flex items-center gap-2">
      <Icon icon="mdi:email-open-outline" class="w-4 h-4" />
      {$_('settingsGeneral.markAsRead')}
    </h3>

    <div class="space-y-2">
      <Label>{$_('settingsGeneral.markAsReadAfter')}</Label>
      <div class="flex items-center gap-2">
        <Input
          type="number"
          value={markAsReadDelaySeconds}
          oninput={handleDelayInput}
          min={-1}
          max={5}
          step={0.1}
          class="w-24"
        />
        <span class="text-sm text-muted-foreground">{$_('common.seconds')}</span>
      </div>
      <p class="text-xs text-muted-foreground">
        {$_('settingsGeneral.markAsReadHelp')}
      </p>
    </div>
  </div>

  <!-- Divider -->
  <div class="border-t border-border"></div>

  <!-- Background Section -->
  <div class="space-y-4">
    <h3 class="text-sm font-medium flex items-center gap-2">
      <Icon icon="mdi:application-cog-outline" class="w-4 h-4" />
      {$_('settingsGeneral.background')}
    </h3>

    <div class="space-y-2">
      <div class="flex items-center justify-between">
        <div class="space-y-0.5">
          <Label for="run-background">{$_('settingsGeneral.runInBackground')}</Label>
          <p class="text-xs text-muted-foreground">
            {$_('settingsGeneral.runInBackgroundHelp')}
          </p>
        </div>
        <Switch
          id="run-background"
          bind:checked={runBackground}
          onCheckedChange={handleRunBackgroundChange}
        />
      </div>
    </div>

    <div class="space-y-2">
      <div class="flex items-center justify-between">
        <div class="space-y-0.5">
          <Label for="start-hidden" class={!runBackground ? 'text-muted-foreground' : ''}>{$_('settingsGeneral.startHidden')}</Label>
          <p class="text-xs text-muted-foreground">
            {$_('settingsGeneral.startHiddenHelp')}
          </p>
        </div>
        <Switch
          id="start-hidden"
          bind:checked={startHidden}
          onCheckedChange={handleStartHiddenChange}
          disabled={!runBackground}
        />
      </div>
    </div>

    <div class="space-y-2">
      <div class="flex items-center justify-between">
        <div class="space-y-0.5">
          <Label for="show-unread-badge">{$_('settingsGeneral.showUnreadBadge')}</Label>
          <p class="text-xs text-muted-foreground">
            {$_('settingsGeneral.showUnreadBadgeHelp')}
          </p>
        </div>
        <Switch
          id="show-unread-badge"
          bind:checked={showUnreadBadge}
          onCheckedChange={handleShowUnreadBadgeChange}
        />
      </div>
    </div>
  </div>

  <!-- Divider -->
  <div class="border-t border-border"></div>

  <!-- Notifications Section — macOS only. The OS permission state is
       queryable (and re-enableable via a System Settings deep link)
       only there; on other platforms the row stays hidden. -->
  {#if notificationPermission?.canOpenSystemSettings}
    <div class="space-y-4">
      <h3 class="text-sm font-medium flex items-center gap-2">
        <Icon icon="mdi:bell-outline" class="w-4 h-4" />
        {$_('settingsGeneral.notifications')}
      </h3>

      {#if notificationPermission.status === 'denied'}
        <div class="space-y-2 rounded-md border border-amber-500/50 bg-amber-500/10 p-3">
          <div class="flex items-center gap-2">
            <Icon icon="lucide:triangle-alert" class="w-4 h-4 text-amber-500" />
            <span class="text-sm font-medium">{$_('settingsGeneral.notificationPermissionBlocked')}</span>
          </div>
          <p class="text-xs text-muted-foreground">
            {$_('settingsGeneral.notificationPermissionBlockedHelp')}
          </p>
          <Button size="sm" onclick={handleOpenNotificationSettings}>
            {$_('settingsGeneral.openNotificationSettings')}
          </Button>
        </div>
      {:else if notificationPermission.status === 'authorized'}
        <div class="space-y-2">
          <div class="flex items-center gap-2">
            <Icon icon="lucide:check" class="w-4 h-4 text-emerald-500" />
            <span class="text-sm">{$_('settingsGeneral.notificationPermissionOn')}</span>
          </div>
          <p class="text-xs text-muted-foreground">
            {$_('settingsGeneral.notificationPermissionOnHelp')}
          </p>
        </div>
      {:else if notificationPermission.status === 'not-determined'}
        <div class="space-y-2">
          <span class="text-sm">{$_('settingsGeneral.notificationPermissionNotDetermined')}</span>
          <p class="text-xs text-muted-foreground">
            {$_('settingsGeneral.notificationPermissionNotDeterminedHelp')}
          </p>
        </div>
      {:else}
        <div class="space-y-2">
          <span class="text-sm">{$_('settingsGeneral.notificationPermissionUnknown')}</span>
          <p class="text-xs text-muted-foreground">
            {$_('settingsGeneral.notificationPermissionUnknownHelp')}
          </p>
        </div>
      {/if}
    </div>

    <!-- Divider -->
    <div class="border-t border-border"></div>
  {/if}

  <!-- Startup Section -->
  <div class="space-y-4">
    <h3 class="text-sm font-medium flex items-center gap-2">
      <Icon icon="mdi:power" class="w-4 h-4" />
      {$_('settingsGeneral.startup')}
    </h3>

    <div class="space-y-2">
      <div class="flex items-center justify-between">
        <div class="space-y-0.5">
          <Label for="autostart">{$_('settingsGeneral.autostartOnLogin')}</Label>
          <p class="text-xs text-muted-foreground">
            {$_('settingsGeneral.autostartHelp')}
          </p>
        </div>
        <Switch
          id="autostart"
          bind:checked={autostart}
          onCheckedChange={handleAutostartChange}
        />
      </div>
    </div>
  </div>

</div>

