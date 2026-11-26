<script lang="ts">
  import { onMount } from 'svelte'
  import { _ } from 'svelte-i18n'
  import Icon from '@iconify/svelte'
  import SourceSidebar from '$lib/components/kit/SourceSidebar.svelte'
  import SourceItem from '$lib/components/kit/SourceItem.svelte'
  import SidebarFooter from '$lib/components/kit/SidebarFooter.svelte'
  import SidebarSyncStatus from '$lib/components/kit/SidebarSyncStatus.svelte'
  import { accountStore } from '$lib/stores/accounts.svelte'
  import { contactSourcesStore } from '$extensions/contacts/frontend/stores/contactSources.svelte'
  import { contactsView, selectSource } from '$extensions/contacts/frontend/stores/contactsView.svelte'
  import LinkAccountSourceDialog from './LinkAccountSourceDialog.svelte'
  // @ts-ignore - wailsjs bindings
  import type { v1 } from '$wailsjs/go/models'

  interface Props {
    onSelect: () => void
    onOpenSettings?: () => void
  }

  const { onSelect, onOpenSettings }: Props = $props()

  onMount(() => {
    contactSourcesStore.load()
    // The Accounts section labels sources by mail-account email, so the
    // mail-accounts cache must be populated. Mail's sidebar loads it on
    // mount too; only fetch here when it hasn't been loaded yet.
    if (accountStore.accounts.length === 0) {
      void accountStore.load()
    }
  })

  // Link-account dialog state. Opened from the "Add account" row below
  // the sections.
  let showLinkAccount = $state(false)

  // Source IDs:
  //   ''                  → all (merged search across every source)
  //   'local'             → all local contacts (manual + collected)
  //   'local:manual'      → user-added local contacts
  //   'local:collected'   → auto-collected from sent mail
  // Plus the user's configured contact sources, each with a UUID id.
  type SidebarItem = {
    id: string
    label: string
    icon: string
  }

  // A source is an account source when it is a Google/Microsoft source
  // linked to a mail account (accountId set at link time). Standalone
  // Google/Microsoft sources (contacts-only OAuth, no linked account)
  // stay with CardDAV under "Sources" — they are not a mail account.
  function isAccountSource(s: v1.ContactSource): boolean {
    return (s.type === 'google' || s.type === 'microsoft') && !!s.accountId
  }

  // Account sources are labelled by the linked mail account's email
  // address, falling back to the source name when the account is gone
  // (e.g. removed after linking).
  function accountLabel(s: v1.ContactSource): string {
    const email = accountStore.accounts.find((w) => w.account.id === s.accountId)?.account.email
    return email || s.name
  }

  function providerIcon(type: string): string {
    switch (type) {
      case 'google':
        return 'mdi:google'
      case 'microsoft':
        return 'mdi:microsoft'
      case 'carddav':
        return 'mdi:server'
    }
    return 'mdi:account-multiple-outline'
  }

  // Reactive — re-runs when locale changes because $_ is referenced inside.
  const sections = $derived.by(() => {
    const builtins: SidebarItem[] = [
      { id: '', label: $_('contacts.sidebar.all'), icon: 'mdi:account-multiple' },
      { id: 'local', label: $_('contacts.sidebar.localAll'), icon: 'mdi:folder-account-outline' },
      { id: 'local:manual', label: $_('contacts.sidebar.localManual'), icon: 'mdi:account-plus-outline' },
      { id: 'local:collected', label: $_('contacts.sidebar.localCollected'), icon: 'mdi:email-outline' },
    ]
    const accountItems: SidebarItem[] = contactSourcesStore.sources
      .filter(isAccountSource)
      .map(s => ({
        id: s.id,
        label: accountLabel(s),
        icon: providerIcon(s.type),
      }))
    const sourceItems: SidebarItem[] = contactSourcesStore.sources
      .filter(s => !isAccountSource(s))
      .map(s => ({
        id: s.id,
        label: s.name,
        icon: providerIcon(s.type),
      }))
    return [
      { items: builtins },
      { id: 'accounts', heading: $_('contacts.sidebar.accountsHeading'), items: accountItems },
      { id: 'sources', heading: $_('contacts.sidebar.sourcesHeading'), items: sourceItems },
    ]
  })

  function pick(id: string) {
    selectSource(id)
    onSelect()
  }
</script>

<SourceSidebar
  title={$_('contacts.sidebar.title')}
  {sections}
  selectedId={contactsView.selectedSourceId}
  onSelect={pick}
>
  {#snippet item(it: SidebarItem, { active })}
    <SourceItem icon={it.icon} label={it.label} {active} onclick={() => pick(it.id)} />
  {/snippet}

  {#snippet sectionEmpty(section: { id?: string; heading?: string; items: SidebarItem[] })}
    <p class="mx-4 my-1 text-xs text-muted-foreground">
      {section.id === 'accounts' ? $_('contacts.sidebar.noAccounts') : $_('contacts.sidebar.noSources')}
    </p>
  {/snippet}

  {#snippet afterSections()}
    <SourceItem
      icon="mdi:account-plus-outline"
      label={$_('contacts.sidebar.addAccount')}
      active={false}
      onclick={() => { showLinkAccount = true }}
    />
  {/snippet}

  {#snippet footerContent()}
    <SidebarFooter>
      {#snippet leading()}
        <SidebarSyncStatus
          syncing={contactSourcesStore.syncing}
          idleLabel={$_('contacts.sidebar.idle')}
          syncingLabel={$_('contacts.sidebar.syncing')}
          onSync={() => contactSourcesStore.syncAll()}
        />
      {/snippet}
      {#snippet trailing()}
        <button
          class="p-1 rounded hover:bg-muted/40"
          title={$_('contacts.sidebar.settings')}
          onclick={() => onOpenSettings?.()}
          type="button"
          aria-label={$_('contacts.sidebar.settings')}
        >
          <Icon icon="mdi:cog-outline" class="w-4 h-4" />
        </button>
      {/snippet}
    </SidebarFooter>
  {/snippet}
</SourceSidebar>

<LinkAccountSourceDialog bind:open={showLinkAccount} />
