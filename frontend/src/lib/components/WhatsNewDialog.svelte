<script lang="ts">
  // Per-version release announcement. Shown once after the user upgrades
  // to a new Daak version. Only the explicit OK click records
  // acknowledgement (via onAcknowledge → SetLastSeenVersion in App.svelte).
  // Closing via ESC or outside-click leaves the version unrecorded, so
  // the dialog fires again on next launch.
  //
  // Per-release update workflow: bump Version in app/state.go, then edit
  // the body markup below to be the new release announcement.
  import { Dialog as DialogPrimitive } from 'bits-ui'
  import { cn } from '$lib/utils'
  import { Button } from '$lib/components/ui/button'
  // @ts-ignore - wailsjs path
  import { OpenURL } from '../../../wailsjs/go/app/App.js'
  import { _ } from '$lib/i18n'
  import { APP_NAME } from '$lib/config/brand'
  import { dialogGuardOpen, dialogGuardClose } from '$lib/stores/dialogGuard'

  interface Props {
    open: boolean
    onAcknowledge: () => void
  }

  let { open = $bindable(false), onAcknowledge }: Props = $props()

  // Hold the dialog guard while open so global shortcuts (Ctrl+, etc.) don't
  // fire behind this modal. Effect cleanup releases it on close and on
  // unmount-while-open (same pattern as ConfirmDialog).
  $effect(() => {
    if (!open) return
    dialogGuardOpen()
    return () => dialogGuardClose()
  })

  const CHANGELOG_URL = 'https://github.com/absonsnet/daak/blob/main/CHANGELOG.md'

  // Open external links via the backend OpenURL: on Linux it tries the OpenURI
  // portal first, so links work inside the Flatpak sandbox (where xdg-open —
  // and thus Wails' BrowserOpenURL — can't reach the host browser).
  function openExternal(url: string) {
    OpenURL(url).catch((err: unknown) => console.error('Failed to open URL:', err))
  }
</script>

<DialogPrimitive.Root bind:open>
  <DialogPrimitive.Portal>
    <DialogPrimitive.Overlay
      class="fixed inset-0 z-50 bg-black/80 data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0"
    />
    <DialogPrimitive.Content
      preventScroll={false}
      class={cn(
        'fixed left-[50%] top-[50%] z-50 grid w-full max-w-lg translate-x-[-50%] translate-y-[-50%] gap-6 border bg-background p-8 shadow-lg duration-200 data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95 data-[state=closed]:slide-out-to-left-1/2 data-[state=closed]:slide-out-to-top-[48%] data-[state=open]:slide-in-from-left-1/2 data-[state=open]:slide-in-from-top-[48%] sm:rounded-lg'
      )}
    >
      <div class="flex flex-col space-y-1.5 text-center sm:text-left">
        <h2 class="text-lg font-semibold leading-none tracking-tight">
          What's new?
        </h2>
      </div>

      <div class="space-y-4 max-h-[60vh] overflow-y-auto text-sm">
        <p>🚀 Welcome to {APP_NAME} v0.7.38!</p>

        <p>v0.7.38 fixes the remaining nineteen issues found in the whole-app audit:</p>

        <ul class="list-disc pl-6 space-y-1">
          <li>🔁 Failed body downloads now retry on Force Resync, and Unsubscribe is fixed: no more injected headers, and one-click Unsubscribe only reports success when the request truly arrives</li>
          <li>📬 Mail Merge is safer: failed rows no longer block later rows to the same person, cancelling never deletes a send already in flight, failed runs are recorded as failed with Resume working, and run-log failures are no longer hidden</li>
          <li>⏪ The Undo toast now reliably appears, and closing the window can no longer strand Daak invisible with no tray icon</li>
          <li>🔍 Message-ID lookups now match regardless of brackets, attachments save byte-identical, server-search attachments list properly, new-mail counts are real arrivals, large messages are flagged as truncated, contacts are checked across all of the sending account's address books, and sign-in refreshes no longer clash</li>
        </ul>

        <p>🏷 See the full change log here:</p>

        <p>
          <button
            type="button"
            class="text-primary hover:underline break-all focus:outline-none focus-visible:outline-none focus:ring-0"
            onclick={() => openExternal(CHANGELOG_URL)}
          >
            https://github.com/absonsnet/daak/blob/main/CHANGELOG.md
          </button>
        </p>
      </div>

      <div class="flex flex-col-reverse sm:flex-row sm:justify-end sm:space-x-2">
        <Button onclick={onAcknowledge}>{$_('common.ok')}</Button>
      </div>
    </DialogPrimitive.Content>
  </DialogPrimitive.Portal>
</DialogPrimitive.Root>
