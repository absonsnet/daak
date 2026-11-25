<script lang="ts">
  // MailMergeProgress — live per-row run log + run history.
  //
  // Given a runId it polls MailMerge_GetRun and subscribes to the
  // bridge's `mailmerge:run-updated` events (emitted with the run id
  // after every row lands — see extensions/mail-merge/backend/bridge.go
  // emitRunUpdated), refreshing on either signal while the run is
  // active. Cancel stops after the current message. Held rows (cap)
  // offer Send next N / Send all remaining / Cancel remaining; a
  // failed run offers Resume (MailMerge_ResumeRun re-runs failed rows
  // only — or the whole stored request when no failed rows were
  // recorded).
  //
  // With no runId it shows the run history (MailMerge_ListRuns); picking
  // a run loads its detail. A whole-run failure (e.g. SMTP connect
  // fails before any row) marks the run `failed` with the error on
  // the run and every unprocessed row recorded failed — Resume
  // re-runs them. If recording those failed rows itself fails, the
  // run is still `failed` (with the recording failure in its error
  // text) and Resume re-runs the whole stored request — so the
  // Resume button is gated on the failed status, not on the failed
  // row count.

  import { onMount, onDestroy } from 'svelte'
  import Icon from '@iconify/svelte'
  import { EventsOn, EventsOff } from '$wailsjs/runtime/runtime'
  import { toasts } from '$lib/stores/toast'
  import { Button } from '$lib/components/ui/button'
  import type { ComposerApi } from '$lib/composerApi'
  import { normalizeRun } from '$extensions/mail-merge/frontend/lib/mailmerge'
  import type { MailMergeRun, MailMergeRunSummary } from '$extensions/mail-merge/frontend/lib/mailmerge'

  interface Props {
    api: ComposerApi
    /** Run to follow live. Omit for the history view. */
    runId?: string | null
    /** Cap limit of the run, for the "Send next N" label. */
    capLimit?: number
    onRunStarted?: (runId: string) => void
  }

  let { api, runId = null, capLimit = 100, onRunStarted }: Props = $props()

  let run = $state<MailMergeRun | null>(null)
  let loading = $state(false)
  let loadError = $state('')
  let history = $state<MailMergeRunSummary[]>([])
  let historyLoading = $state(false)
  let selectedHistoryId = $state<string | null>(null)
  let actionBusy = $state('')
  let pollTimer: ReturnType<typeof setInterval> | null = null

  const isRunning = $derived(run?.status === 'running')
  const heldCount = $derived(run?.heldCount ?? 0)
  const failedCount = $derived(run?.failedCount ?? 0)
  const doneRows = $derived((run?.sentCount ?? 0) + (run?.failedCount ?? 0) + (run?.skippedCount ?? 0) + (run?.heldCount ?? 0))

  function statusLabel(status: string): string {
    switch (status) {
      case 'running': return 'Running'
      case 'done': return 'Done'
      case 'cancelled': return 'Cancelled'
      case 'failed': return 'Failed'
      case 'held': return 'Held — over cap'
      case 'queued': return 'Queued — awaiting dispatch'
      default: return status
    }
  }

  function rowStatusLabel(status: string): string {
    switch (status) {
      case 'sent': return 'Sent'
      case 'draft-saved': return 'Draft saved'
      case 'failed': return 'Failed'
      case 'skipped': return 'Skipped'
      case 'held': return 'Held — over cap'
      case 'queued': return 'Queued'
      default: return status
    }
  }

  function deliverModeLabel(mode: string): string {
    switch (mode) {
      case 'send': return 'Send now'
      case 'later': return 'Send later (queued)'
      default: return 'Save as drafts'
    }
  }

  function formatTime(value: string): string {
    if (!value) return ''
    const d = new Date(value)
    return Number.isNaN(d.getTime()) ? value : d.toLocaleString()
  }

  async function refreshRun(id: string): Promise<void> {
    try {
      run = normalizeRun((await api.mailMergeGetRun(id)) as MailMergeRun)
      loadError = ''
    } catch (err) {
      loadError = (err as Error)?.message ?? String(err)
    }
  }

  async function refreshHistory(): Promise<void> {
    historyLoading = true
    try {
      history = ((await api.mailMergeListRuns()) as MailMergeRunSummary[]) || []
    } catch (err) {
      console.error('mailMergeListRuns failed:', err)
    } finally {
      historyLoading = false
    }
  }

  function startPolling(): void {
    stopPolling()
    pollTimer = setInterval(() => {
      if (runId) void refreshRun(runId)
    }, 2000)
  }

  function stopPolling(): void {
    if (pollTimer !== null) {
      clearInterval(pollTimer)
      pollTimer = null
    }
  }

  async function runAction(label: string, fn: () => Promise<unknown>): Promise<void> {
    actionBusy = label
    try {
      await fn()
      if (runId) await refreshRun(runId)
      await refreshHistory()
    } catch (err) {
      console.error(`mailMerge ${label} failed:`, err)
      toasts.error((err as Error)?.message ?? String(err))
    } finally {
      actionBusy = ''
    }
  }

  async function handleResume(): Promise<void> {
    if (!run) return
    actionBusy = 'resume'
    try {
      const newId = await api.mailMergeResumeRun(run.id)
      toasts.success('Resume started — re-running the failed rows.')
      onRunStarted?.(newId)
    } catch (err) {
      console.error('mailMergeResumeRun failed:', err)
      toasts.error((err as Error)?.message ?? String(err))
    } finally {
      actionBusy = ''
    }
  }

  function selectHistory(id: string): void {
    selectedHistoryId = id
    void refreshRun(id)
  }

  $effect(() => {
    if (runId) {
      loading = true
      void refreshRun(runId).finally(() => { loading = false })
      startPolling()
    } else {
      run = null
      void refreshHistory()
    }
    return () => stopPolling()
  })

  // While a run is active, refresh on every bridge progress event for
  // this run (EventsOn payload is the run id string).
  $effect(() => {
    if (!runId) return
    const handler = (id: string) => {
      if (id === runId) void refreshRun(runId)
    }
    EventsOn('mailmerge:run-updated', handler)
    return () => { EventsOff('mailmerge:run-updated') }
  })

  // Stop polling once the run reaches a terminal state (done/cancelled/
  // held still needs no polling — cap actions refresh explicitly).
  // Refresh the history then too, so the just-finished run shows up
  // without the user having to press Refresh.
  $effect(() => {
    if (run && run.status !== 'running') {
      stopPolling()
      void refreshHistory()
    }
  })

  onMount(() => {
    void refreshHistory()
  })

  onDestroy(() => stopPolling())
</script>

<div class="space-y-3">
  {#if runId}
    {#if loading && !run}
      <p class="text-sm text-muted-foreground flex items-center gap-2">
        <Icon icon="mdi:loading" class="w-4 h-4 animate-spin" /> Loading run…
      </p>
    {:else if loadError}
      <p class="text-sm text-destructive">{loadError}</p>
    {:else if run}
      <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
        <span class="font-medium">{statusLabel(run.status)}</span>
        <span>{doneRows} / {run.totalRows} rows</span>
        <span class="text-green-700 dark:text-green-400">Sent/saved: {run.sentCount}</span>
        {#if run.failedCount > 0}
          <span class="text-destructive">Failed: {run.failedCount}</span>
        {/if}
        {#if run.skippedCount > 0}
          <span class="text-muted-foreground">Skipped: {run.skippedCount}</span>
        {/if}
        {#if run.heldCount > 0}
          <span class="text-amber-700 dark:text-amber-400">Held: {run.heldCount}</span>
        {/if}
        {#if run.queuedCount > 0}
          <span class="text-amber-700 dark:text-amber-400">Queued: {run.queuedCount}</span>
        {/if}
        <span class="text-xs text-muted-foreground">{formatTime(run.createdAt)}</span>
      </div>

      {#if run.error}
        <p class="text-sm text-destructive">{run.error}</p>
      {/if}

      {#if isRunning}
        <div class="h-2 rounded bg-muted overflow-hidden" role="progressbar" aria-valuenow={doneRows} aria-valuemax={run.totalRows}>
          <div
            class="h-full bg-primary transition-all"
            style="width: {run.totalRows > 0 ? Math.round((doneRows / run.totalRows) * 100) : 0}%"
          ></div>
        </div>
      {/if}

      <div class="flex flex-wrap gap-2">
        {#if isRunning}
          <Button
            variant="outline"
            size="sm"
            disabled={actionBusy !== ''}
            onclick={() => void runAction('cancel', () => api.mailMergeCancelRun(run!.id))}
          >
            {actionBusy === 'cancel' ? 'Cancelling…' : 'Cancel run'}
          </Button>
        {/if}
        {#if heldCount > 0 && !isRunning}
          <Button
            size="sm"
            disabled={actionBusy !== ''}
            onclick={() => void runAction('next-batch', () => api.mailMergeSendNextBatch(run!.id))}
          >
            {actionBusy === 'next-batch' ? 'Sending…' : `Send next ${Math.min(capLimit, heldCount)}`}
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={actionBusy !== ''}
            onclick={() => void runAction('send-all', () => api.mailMergeSendAllRemaining(run!.id))}
          >
            {actionBusy === 'send-all' ? 'Sending…' : 'Send all remaining'}
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={actionBusy !== ''}
            onclick={() => void runAction('cancel-remaining', () => api.mailMergeCancelRemaining(run!.id))}
          >
            {actionBusy === 'cancel-remaining' ? 'Cancelling…' : 'Cancel remaining'}
          </Button>
        {/if}
        {#if run.status === 'failed' && !isRunning}
          <Button
            variant="outline"
            size="sm"
            disabled={actionBusy !== ''}
            onclick={() => void handleResume()}
          >
            {actionBusy === 'resume' ? 'Resuming…' : failedCount > 0 ? `Resume (${failedCount} failed)` : 'Resume'}
          </Button>
        {/if}
      </div>

      {#if run.rows.length === 0}
        <p class="text-sm text-muted-foreground">
          {#if run.status === 'cancelled'}
            No rows were processed — the run stopped before any message was produced (e.g. the mail server could not be reached). Check the account's SMTP settings and try again.
          {:else if run.status === 'failed'}
            No rows were processed — the run failed before any message was produced{run.error ? `: ${run.error}` : '.'} Check the account's SMTP settings and try again.
          {:else}
            No rows processed yet.
          {/if}
        </p>
      {:else}
        <div class="max-h-64 overflow-y-auto rounded-md border border-border">
          <table class="w-full text-sm">
            <thead class="sticky top-0 bg-muted">
              <tr class="text-left">
                <th class="px-2 py-1 font-medium">#</th>
                <th class="px-2 py-1 font-medium">Recipient</th>
                <th class="px-2 py-1 font-medium">Result</th>
                <th class="px-2 py-1 font-medium">Detail</th>
              </tr>
            </thead>
            <tbody>
              {#each run.rows as row (row.index)}
                <tr class="border-t border-border">
                  <td class="px-2 py-1 tabular-nums text-muted-foreground">{row.index + 1}</td>
                  <td class="px-2 py-1 break-all">{row.email || '—'}</td>
                  <td class="px-2 py-1">{rowStatusLabel(row.status)}</td>
                  <td class="px-2 py-1 text-muted-foreground break-words">{row.error || ''}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    {/if}
  {/if}

  <!-- Run history -->
  <div class="space-y-2 pt-1">
    <div class="flex items-center justify-between">
      <h4 class="text-sm font-medium">Run history</h4>
      <Button variant="ghost" size="sm" disabled={historyLoading} onclick={() => void refreshHistory()}>
        Refresh
      </Button>
    </div>
    {#if historyLoading && history.length === 0}
      <p class="text-sm text-muted-foreground">Loading…</p>
    {:else if history.length === 0}
      <p class="text-sm text-muted-foreground">No mail merge runs yet.</p>
    {:else}
      <div class="max-h-48 overflow-y-auto rounded-md border border-border divide-y divide-border">
        {#each history as item (item.id)}
          <button
            type="button"
            class="w-full text-left px-3 py-2 text-sm hover:bg-muted flex flex-wrap items-center gap-x-3 gap-y-0.5 {selectedHistoryId === item.id || runId === item.id ? 'bg-muted' : ''}"
            onclick={() => selectHistory(item.id)}
          >
            <span class="font-medium">{statusLabel(item.status)}</span>
            <span>{deliverModeLabel(item.deliverMode)}</span>
            <span>{item.sentCount}/{item.totalRows} sent/saved</span>
            {#if item.queuedCount > 0}<span class="text-amber-700 dark:text-amber-400">{item.queuedCount} queued</span>{/if}
            {#if item.failedCount > 0}<span class="text-destructive">{item.failedCount} failed</span>{/if}
            {#if item.heldCount > 0}<span class="text-amber-700 dark:text-amber-400">{item.heldCount} held</span>{/if}
            {#if item.skippedCount > 0}<span class="text-muted-foreground">{item.skippedCount} skipped</span>{/if}
            <span class="ml-auto text-xs text-muted-foreground">{formatTime(item.createdAt)}</span>
          </button>
        {/each}
      </div>
    {/if}
  </div>
</div>
