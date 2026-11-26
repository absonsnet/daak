<script lang="ts" module>
  // Shared helpers for linking a mail account as a contact source. Used by
  // both the Contacts sidebar ("Add account" row) and the Contacts settings
  // dialog ("Linked Accounts" section) so the two surfaces agree on which
  // accounts can be linked.
  //
  // Only Google/Microsoft OAuth mail accounts can be linked: the backend's
  // LinkAccountContactSource rejects non-OAuth accounts and providers other
  // than google/microsoft. Accounts already linked (a source carries their
  // accountId) are excluded, as are disabled accounts.

  // @ts-ignore - wailsjs bindings
  import type { account, v1 } from '$wailsjs/go/models'

  export function unlinkedMailAccounts(
    accounts: account.Account[],
    sources: v1.ContactSource[],
  ): account.Account[] {
    return accounts.filter(
      (a) =>
        a.authType === 'oauth2' &&
        a.enabled &&
        !sources.some((s) => s.accountId && s.accountId === a.id),
    )
  }
</script>

<script lang="ts">
  // LinkAccountSourceDialog — links a mail account as a contact source.
  //
  // Lists the user's unlinked Google/Microsoft OAuth mail accounts (from the
  // core mail-accounts store) and links the picked one via the existing
  // Contacts_LinkAccountSource bridge (contactSourcesStore.linkAccount,
  // default sync interval 60 — same as the post-account-add hook panel).
  // After linking, the new source appears in the sidebar (sources store
  // reloads inside linkAccount) and the browse list refreshes.
  //
  // This is the way back in for accounts whose post-add "Set up contacts"
  // step was dismissed: previously linking was only possible from that
  // one-shot hook panel.

  import { untrack } from 'svelte'
  import { _ } from 'svelte-i18n'
  import Icon from '@iconify/svelte'
  import * as Dialog from '$lib/components/ui/dialog'
  import { Button } from '$lib/components/ui/button'
  import { accountStore } from '$lib/stores/accounts.svelte'
  import { contactSourcesStore } from '$extensions/contacts/frontend/stores/contactSources.svelte'
  import { reloadContacts } from '$extensions/contacts/frontend/stores/contactsView.svelte'
  import { toasts } from '$lib/stores/toast'

  interface Props {
    open: boolean
    /** Pre-select this account when the dialog opens (settings rows pass
     *  the account their Link button belongs to). Falls back to the first
     *  unlinked account when the id isn't in the unlinked list. */
    preselectedAccountId?: string
    onLinked?: (accountId: string) => void
    onClose?: () => void
  }

  let { open = $bindable(false), preselectedAccountId = '', onLinked, onClose }: Props = $props()

  let selected = $state('')
  let linking = $state(false)

  const unlinked = $derived(
    unlinkedMailAccounts(
      accountStore.accounts.map((w) => w.account),
      contactSourcesStore.sources,
    ),
  )

  // Refresh sources + pick the initial selection each time the dialog
  // opens. Wrapped in untrack so the store reloads don't re-trigger the
  // effect and reset the selection mid-pick.
  $effect(() => {
    if (!open) return
    untrack(() => {
      void contactSourcesStore.load()
      if (accountStore.accounts.length === 0) {
        void accountStore.load()
      }
      const list = unlinkedMailAccounts(
        accountStore.accounts.map((w) => w.account),
        contactSourcesStore.sources,
      )
      selected =
        list.some((a) => a.id === preselectedAccountId)
          ? preselectedAccountId
          : (list[0]?.id ?? '')
      linking = false
    })
  })

  function close() {
    if (linking) return
    open = false
    onClose?.()
  }

  async function link() {
    const account = unlinked.find((a) => a.id === selected)
    if (!account || linking) return
    linking = true
    try {
      const name = account.name?.trim() || account.email
      await contactSourcesStore.linkAccount(account.id, name, 60)
      await reloadContacts()
      toasts.success($_('contacts.linkAccount.success', { values: { name } }))
      open = false
      onLinked?.(account.id)
    } catch (err) {
      console.error('Failed to link account as contact source:', err)
      toasts.error((err as Error)?.message ?? String(err))
    } finally {
      linking = false
    }
  }
</script>

<Dialog.Root bind:open onOpenChange={(v) => { if (!v) close() }}>
  <Dialog.Content class="max-w-md">
    <Dialog.Header>
      <Dialog.Title>{$_('contacts.linkAccount.title')}</Dialog.Title>
      <Dialog.Description>
        {$_('contacts.linkAccount.description')}
      </Dialog.Description>
    </Dialog.Header>

    <div class="mt-2 space-y-2 max-h-[40vh] overflow-y-auto pr-1">
      {#if unlinked.length === 0}
        <div class="flex flex-col items-center gap-2 py-6 text-center">
          <Icon icon="mdi:account-alert-outline" class="w-8 h-8 text-muted-foreground" />
          <p class="text-sm text-foreground">{$_('contacts.linkAccount.emptyHeading')}</p>
          <p class="text-xs text-muted-foreground">{$_('contacts.linkAccount.emptyHint')}</p>
        </div>
      {:else}
        {#each unlinked as account (account.id)}
          <label
            class="flex items-center gap-3 p-3 rounded-md border border-border cursor-pointer hover:bg-muted/40 transition-colors {selected === account.id ? 'bg-primary/5 border-primary/40' : ''}"
          >
            <input
              type="radio"
              name="link-account"
              value={account.id}
              bind:group={selected}
              class="accent-primary"
              disabled={linking}
            />
            <div class="flex-1 min-w-0">
              <div class="text-sm font-medium text-foreground truncate">{account.email}</div>
              {#if account.name && account.name !== account.email}
                <div class="text-xs text-muted-foreground truncate">{account.name}</div>
              {/if}
            </div>
          </label>
        {/each}
      {/if}
    </div>

    <div class="flex items-center justify-end gap-2 pt-4 border-t border-border mt-4">
      <Button variant="ghost" onclick={close} disabled={linking}>{$_('contacts.common.cancel')}</Button>
      <Button onclick={link} disabled={linking || !selected}>
        {#if linking}
          <Icon icon="mdi:loading" class="w-4 h-4 mr-1 animate-spin" />
        {/if}
        {$_('contacts.linkAccount.linkButton')}
      </Button>
    </div>
  </Dialog.Content>
</Dialog.Root>
