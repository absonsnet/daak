<script lang="ts">
  // ExhaustedWritesBanner — audit4 finding 21.
  //
  // Lists offline calendar writes that used up their retry budget, so
  // the user can retry them (next Drain) or discard them (the edit
  // never reaches the server). Hidden when there are no exhausted rows.
  import { _ } from 'svelte-i18n'
  import { exhaustedWrites } from '$extensions/calendar/frontend/stores/exhaustedWrites.svelte'

  function opLabel(op: string): string {
    if (op === 'create') return $_('calendar.write.exhaustedOp.create')
    if (op === 'update') return $_('calendar.write.exhaustedOp.update')
    if (op === 'delete') return $_('calendar.write.exhaustedOp.delete')
    return op
  }
</script>

{#if exhaustedWrites.writes.length > 0}
  <div
    class="shrink-0 border-b border-amber-300 bg-amber-50 px-4 py-2 text-amber-900 dark:border-amber-700 dark:bg-amber-950 dark:text-amber-100"
    role="alert"
  >
    <p class="text-sm font-medium">{$_('calendar.write.exhausted')}</p>
    <ul class="mt-1 space-y-1">
      {#each exhaustedWrites.writes as write (write.id)}
        <li class="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
          <span class="font-medium">{opLabel(write.op)}: {write.summary || write.eventId}</span>
          {#if write.lastError}
            <span class="truncate text-xs text-amber-700 dark:text-amber-300" title={write.lastError}>
              {write.lastError}
            </span>
          {/if}
          <span class="ml-auto flex gap-1">
            <button
              type="button"
              class="rounded border border-amber-400 px-2 py-0.5 text-xs font-medium hover:bg-amber-100 dark:border-amber-600 dark:hover:bg-amber-900"
              onclick={() => void exhaustedWrites.retry(write.id)}
            >
              {$_('calendar.write.retry')}
            </button>
            <button
              type="button"
              class="rounded border border-amber-400 px-2 py-0.5 text-xs font-medium hover:bg-amber-100 dark:border-amber-600 dark:hover:bg-amber-900"
              onclick={() => void exhaustedWrites.discard(write.id)}
            >
              {$_('calendar.write.discard')}
            </button>
          </span>
        </li>
      {/each}
    </ul>
  </div>
{/if}
