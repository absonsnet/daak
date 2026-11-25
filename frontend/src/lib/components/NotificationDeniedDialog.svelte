<script lang="ts">
  // One-time notice shown when the OS notification permission is denied
  // (re-audit finding R28). Without it, a denied macOS permission fails
  // silently forever: new-mail notifications are dropped and the user
  // has no way to know why. Shown exactly once per install — the
  // frontend marks the notice shown when this dialog opens
  // (fourth-audit finding 54), never on the check, so a notice that
  // never reaches the screen is not consumed. "Open System Settings"
  // deep-links to the Notifications pane; "Not now" just dismisses it
  // (the Settings row keeps showing the blocked state).
  import * as AlertDialog from '$lib/components/ui/alert-dialog'
  import { Button } from '$lib/components/ui/button'
  import Icon from '@iconify/svelte'
  import { _ } from '$lib/i18n'
  import { dialogGuardOpen, dialogGuardClose } from '$lib/stores/dialogGuard'

  interface Props {
    open: boolean
    onOpenSettings: () => void
    onDismiss: () => void
  }
  // Hold the dialog guard while open so global shortcuts (Ctrl+, etc.) don't
  // fire behind this modal. Effect cleanup releases it on close and on
  // unmount-while-open (same pattern as ConfirmDialog).
  $effect(() => {
    if (!open) return
    dialogGuardOpen()
    return () => dialogGuardClose()
  })

  let { open = $bindable(false), onOpenSettings, onDismiss }: Props = $props()

  function handleOpenSettings() {
    onOpenSettings()
  }

  function handleDismiss() {
    onDismiss()
  }
</script>

<AlertDialog.Root bind:open>
  <AlertDialog.Content>
    <AlertDialog.Header>
      <AlertDialog.Title class="flex items-center gap-2">
        <Icon icon="lucide:bell-off" class="w-5 h-5 text-amber-500" />
        {$_('notificationDenied.title')}
      </AlertDialog.Title>
      <AlertDialog.Description>
        <p class="mb-3">{$_('notificationDenied.intro')}</p>
        <p>{$_('notificationDenied.howTo')}</p>
      </AlertDialog.Description>
    </AlertDialog.Header>
    <AlertDialog.Footer>
      <Button variant="ghost" onclick={handleDismiss}>
        {$_('notificationDenied.notNow')}
      </Button>
      <Button onclick={handleOpenSettings}>
        {$_('notificationDenied.openSettings')}
      </Button>
    </AlertDialog.Footer>
  </AlertDialog.Content>
</AlertDialog.Root>
