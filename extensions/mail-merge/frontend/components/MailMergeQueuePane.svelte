<script lang="ts">
  // MailMergeQueuePane — the Mail Merge rail tab: the outbox queue /
  // job history (Phase 2).
  //
  // Mounted by App.svelte when the active rail tab is 'mail-merge'
  // (main window only — the detached composer has no rail). Talks to
  // the backend through the generated App bindings directly, like the
  // calendar components do; no ComposerApi is involved here.
  //
  // Shows the global queue controls (scheduled dispatch pause, the
  // per-hour budget, Send Unsent now — Thunderbird's File → Send
  // Unsent Messages) and, per run, its queued messages with status,
  // attempts and next-attempt time, plus pause/resume/cancel per run,
  // retry-failed and send-unsent per run. Refreshes on a 5s poll and
  // on the bridge's `mailmerge:queue-updated` events.

  import { onMount, onDestroy } from 'svelte'
  import Icon from '@iconify/svelte'
  import { EventsOn, EventsOff } from '$wailsjs/runtime/runtime'
  import { toasts } from '$lib/stores/toast'
  import { Button } from '$lib/components/ui/button'
  import { Input } from '$lib/components/ui/input'
  import { Label } from '$lib/components/ui/label'
  import Switch from '$lib/components/ui/switch/Switch.svelte'
  import {
    MailMerge_CancelQueuedItem,
    MailMerge_CancelQueuedRun,
    MailMerge_GetQueueConfig,
    MailMerge_GetQueueStats,
    MailMerge_ListQueue,
    MailMerge_ListRuns,
    MailMerge_PauseQueue,
    MailMerge_PauseQueuedRun,
    MailMerge_ResumeQueue,
    MailMerge_ResumeQueuedRun,
    MailMerge_RetryFailedRun,
    MailMerge_SaveQueueConfig,
    MailMerge_SendUnsent,
    MailMerge_SendUnsentRun,
  } from '$wailsjs/go/app/App.js'
  import {
    normalizeQueueItems,
    normalizeQueueStats,
    type MailMergeCancelQueuedRunResult,
    type MailMergeDispatchResult,
    type MailMergeQueueConfig,
    type MailMergeQueueStats,
    type MailMergeQueuedItem,
    type MailMergeRunSummary,
  } from '$extensions/mail-merge/frontend/lib/mailmerge'

  let items = $state<MailMergeQueuedItem[]>([])
  let stats = $state<MailMergeQueueStats | null>(null)
  let config = $state<MailMergeQueueConfig>({ perHour: 100, paused: false, countNormalSends: false })
  let runs = $state<MailMergeRunSummary[]>([])
  let loading = $state(true)
  let loadError = $state('')
  let actionBusy = $state('')
  let perHourDraft = $state(100)
  let countNormalSendsDraft = $state(false)
  let pollTimer: ReturnType<typeof setInterval> | null = null

  const queuedItems = $derived(items.filter((i) => i.status === 'queued' || i.status === 'sending'))
  const failedItems = $derived(items.filter((i) => i.status === 'failed'))

  function runLabel(runId: string): string {
    const run = runs.find((r) => r.id === runId)
    if (!run) return runId.slice(0, 8)
    const d = new Date(run.createdAt)
    const when = Number.isNaN(d.getTime()) ? '' : d.toLocaleString()
    return `${run.deliverMode === 'later' ? 'Send later' : run.deliverMode} · ${run.totalRows} rows · ${when}`
  }

  function itemStatusLabel(status: string): string {
    switch (status) {
      case 'queued': return 'Queued'
      case 'sending': return 'Sending…'
      case 'sent': return 'Sent'
      case 'failed': return 'Failed'
      case 'cancelled': return 'Cancelled'
      default: return status
    }
  }

  function formatTime(value: string): string {
    if (!value) return ''
    const d = new Date(value)
    return Number.isNaN(d.getTime()) ? value : d.toLocaleString()
  }

  async function refresh(): Promise<void> {
    try {
      const [queue, st, cfg, runList] = await Promise.all([
        MailMerge_ListQueue(),
        MailMerge_GetQueueStats(),
        MailMerge_GetQueueConfig(),
        MailMerge_ListRuns(),
      ])
      items = normalizeQueueItems((queue as MailMergeQueuedItem[]) || [])
      stats = normalizeQueueStats(st as MailMergeQueueStats)
      config = (cfg as MailMergeQueueConfig) || { perHour: 100, paused: false, countNormalSends: false }
      perHourDraft = config.perHour
      countNormalSendsDraft = config.countNormalSends ?? false
      runs = (runList as MailMergeRunSummary[]) || []
      loadError = ''
    } catch (err) {
      loadError = (err as Error)?.message ?? String(err)
    } finally {
      loading = false
    }
  }

  async function runAction(label: string, fn: () => Promise<unknown>): Promise<void> {
    actionBusy = label
    try {
      await fn()
      await refresh()
    } catch (err) {
      console.error(`mailMerge queue ${label} failed:`, err)
      toasts.error((err as Error)?.message ?? String(err))
    } finally {
      actionBusy = ''
    }
  }

  // Cancel a run's queued messages and say what actually happened:
  // in-flight messages are not cancelled — their send proceeds —
  // so a bare success would leave the user believing the run was
  // stopped. The backend errors when nothing was queued at all.
  async function handleCancelQueuedRun(runId: string): Promise<void> {
    actionBusy = `cancel-${runId}`
    try {
      const res = (await MailMerge_CancelQueuedRun(runId)) as MailMergeCancelQueuedRunResult
      const cancelled = res.cancelled ?? 0
      const inFlight = res.inFlight ?? 0
      const cancelledText = `Cancelled ${cancelled} queued message${cancelled === 1 ? '' : 's'}.`
      if (inFlight > 0) {
        toasts.success(
          `${cancelledText} ${inFlight} ${inFlight === 1 ? 'was' : 'were'} already sending and will still go out.`,
        )
      } else {
        toasts.success(cancelledText)
      }
      await refresh()
    } catch (err) {
      console.error(`mailMerge queue cancel-${runId} failed:`, err)
      toasts.error((err as Error)?.message ?? String(err))
    } finally {
      actionBusy = ''
    }
  }

  async function handleSendUnsent(): Promise<void> {
    actionBusy = 'send-unsent'
    try {
      const res = (await MailMerge_SendUnsent()) as MailMergeDispatchResult
      toasts.success(`Send unsent: ${res.sent} sent, ${res.failed} failed, ${res.remaining} still queued.`)
      await refresh()
    } catch (err) {
      console.error('mailMerge SendUnsent failed:', err)
      toasts.error((err as Error)?.message ?? String(err))
    } finally {
      actionBusy = ''
    }
  }

  async function handleSavePerHour(): Promise<void> {
    if (perHourDraft < 1) {
      toasts.error('The per-hour limit must be at least 1.')
      return
    }
    await runAction('save-config', () => MailMerge_SaveQueueConfig({ perHour: perHourDraft, paused: config.paused, countNormalSends: countNormalSendsDraft }))
  }

  function itemsForRun(runId: string): MailMergeQueuedItem[] {
    return items.filter((i) => i.runId === runId)
  }

  onMount(() => {
    void refresh()
    pollTimer = setInterval(() => { void refresh() }, 5000)
    EventsOn('mailmerge:queue-updated', () => { void refresh() })
  })

  onDestroy(() => {
    if (pollTimer !== null) clearInterval(pollTimer)
    EventsOff('mailmerge:queue-updated')
  })
</script>

<div class="flex-1 min-w-0 flex flex-col overflow-hidden bg-background">
  <div class="flex-1 min-h-0 overflow-y-auto p-4 space-y-4">
    <header class="space-y-1">
      <h2 class="text-lg font-semibold">Mail Merge — Outbox</h2>
      <p class="text-sm text-muted-foreground">
        Messages queued by “Send later”, sent automatically up to the hourly limit below — or now, with Send Unsent.
        Each message was rendered when queued, so it is sent exactly as queued.
      </p>
    </header>

    {#if loading}
      <p class="text-sm text-muted-foreground flex items-center gap-2">
        <Icon icon="mdi:loading" class="w-4 h-4 animate-spin" /> Loading queue…
      </p>
    {:else if loadError}
      <p class="text-sm text-destructive">{loadError}</p>
    {:else}
      <!-- Global controls -->
      <section class="space-y-3 rounded-md border border-border p-3">
        <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
          <span><span class="font-medium">{stats?.queued ?? 0}</span> queued</span>
          {#if (stats?.failed ?? 0) > 0}
            <span class="text-destructive">Failed: {stats?.failed}</span>
          {/if}
          <!-- Sent and cancelled rows are deleted from the queue on
               success/cancel — the run list below is the record. -->
          <span class="text-xs text-muted-foreground">
            Scheduled dispatch: {config.paused ? 'paused' : `${config.perHour} per hour`}
          </span>
        </div>
        <div class="flex flex-wrap items-end gap-3">
          <div class="space-y-1">
            <label for="mm-queue-perhour" class="text-sm text-muted-foreground">Send at most (per hour)</label>
            <Input id="mm-queue-perhour" type="number" min="1" bind:value={perHourDraft} class="w-24" />
          </div>
          <Button size="sm" variant="outline" disabled={actionBusy !== ''} onclick={() => void handleSavePerHour()}>
            {actionBusy === 'save-config' ? 'Saving…' : 'Save limit'}
          </Button>
          {#if config.paused}
            <Button size="sm" variant="outline" disabled={actionBusy !== ''} onclick={() => void runAction('resume-queue', () => MailMerge_ResumeQueue())}>
              {actionBusy === 'resume-queue' ? 'Resuming…' : 'Resume scheduled dispatch'}
            </Button>
          {:else}
            <Button size="sm" variant="outline" disabled={actionBusy !== ''} onclick={() => void runAction('pause-queue', () => MailMerge_PauseQueue())}>
              {actionBusy === 'pause-queue' ? 'Pausing…' : 'Pause scheduled dispatch'}
            </Button>
          {/if}
          <Button size="sm" disabled={actionBusy !== '' || queuedItems.length === 0} onclick={() => void handleSendUnsent()}>
            {actionBusy === 'send-unsent' ? 'Sending…' : 'Send unsent now'}
          </Button>
        </div>
        <div class="flex flex-wrap items-center gap-3 pt-1">
          <Switch id="mm-queue-count-normal" bind:checked={countNormalSendsDraft} />
          <Label for="mm-queue-count-normal" class="text-sm">
            Count ordinary emails I send toward this hourly limit too
          </Label>
        </div>
        <p class="text-xs text-muted-foreground">
          Pausing stops the automatic hourly dispatch only — Send Unsent still sends on demand. The per-hour limit is
          shared across all runs: once it is reached, the next batch goes out when the oldest send in the window is over
          an hour old.
        </p>
        <p class="text-xs text-muted-foreground">
          {#if countNormalSendsDraft}
            With the toggle on, ordinary emails sent from the composer (through the Outbox) count toward the same hourly
            limit as Mail Merge mail, so a busy hour of ordinary sending can hold back the next merge batch. Reactions
            never count. Save the limit to apply the change.
          {:else}
            With the toggle off, the limit counts Mail Merge mail only — ordinary emails sent from the composer do not
            count toward it, and Mail Merge's own direct sends do count (they are Mail Merge mail). Reactions never
            count. Save the limit to apply the change.
          {/if}
        </p>
      </section>

      <!-- Per-run queue -->
      {#if !stats || stats.runs.length === 0}
        <p class="text-sm text-muted-foreground">The outbox is empty — no messages have been queued yet. Use “Send later” in the Mail Merge dialog to queue a run.</p>
      {:else}
        {#each stats.runs as runStat (runStat.runId)}
          <section class="space-y-2 rounded-md border border-border p-3">
            <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
              <span class="font-medium">{runLabel(runStat.runId)}</span>
              {#if runStat.paused}<span class="text-amber-700 dark:text-amber-400">Paused</span>{/if}
              <span>{runStat.queued} queued</span>
              <span class="text-green-700 dark:text-green-400">{runStat.sent} sent</span>
              {#if runStat.failed > 0}<span class="text-destructive">{runStat.failed} failed</span>{/if}
              {#if runStat.cancelled > 0}<span class="text-muted-foreground">{runStat.cancelled} cancelled</span>{/if}
            </div>
            <div class="flex flex-wrap gap-2">
              <Button
                size="sm"
                variant="outline"
                disabled={actionBusy !== '' || runStat.queued === 0}
                onclick={() => void runAction(`send-${runStat.runId}`, async () => {
                  const res = (await MailMerge_SendUnsentRun(runStat.runId)) as MailMergeDispatchResult
                  toasts.success(`Run send unsent: ${res.sent} sent, ${res.failed} failed, ${res.remaining} still queued.`)
                })}
              >
                {actionBusy === `send-${runStat.runId}` ? 'Sending…' : 'Send unsent (this run)'}
              </Button>
              {#if runStat.paused}
                <Button size="sm" variant="outline" disabled={actionBusy !== ''} onclick={() => void runAction(`resume-${runStat.runId}`, () => MailMerge_ResumeQueuedRun(runStat.runId))}>
                  Resume
                </Button>
              {:else}
                <Button size="sm" variant="outline" disabled={actionBusy !== ''} onclick={() => void runAction(`pause-${runStat.runId}`, () => MailMerge_PauseQueuedRun(runStat.runId))}>
                  Pause
                </Button>
              {/if}
              {#if runStat.failed > 0}
                <Button size="sm" variant="outline" disabled={actionBusy !== ''} onclick={() => void runAction(`retry-${runStat.runId}`, () => MailMerge_RetryFailedRun(runStat.runId))}>
                  Retry {runStat.failed} failed
                </Button>
              {/if}
              {#if runStat.queued > 0}
                <Button size="sm" variant="outline" disabled={actionBusy !== ''} onclick={() => void handleCancelQueuedRun(runStat.runId)}>
                  Cancel queued
                </Button>
              {/if}
            </div>
            <div class="max-h-64 overflow-y-auto rounded-md border border-border">
              <table class="w-full text-sm">
                <thead class="sticky top-0 bg-muted">
                  <tr class="text-left">
                    <th class="px-2 py-1 font-medium">#</th>
                    <th class="px-2 py-1 font-medium">Recipient</th>
                    <th class="px-2 py-1 font-medium">Subject</th>
                    <th class="px-2 py-1 font-medium">Status</th>
                    <th class="px-2 py-1 font-medium">Next attempt</th>
                    <th class="px-2 py-1 font-medium">Detail</th>
                    <th class="px-2 py-1 font-medium"></th>
                  </tr>
                </thead>
                <tbody>
                  {#each itemsForRun(runStat.runId) as item (item.id)}
                    <tr class="border-t border-border">
                      <td class="px-2 py-1 tabular-nums text-muted-foreground">{item.rowIndex + 1}</td>
                      <td class="px-2 py-1 break-all">{item.recipient || '—'}</td>
                      <td class="px-2 py-1 break-all">{item.subject || '—'}</td>
                      <td class="px-2 py-1">{itemStatusLabel(item.status)}{item.attempts > 0 ? ` (${item.attempts}/${item.maxAttempts})` : ''}</td>
                      <td class="px-2 py-1 text-muted-foreground">{item.status === 'queued' ? formatTime(item.nextAttemptAt) : item.status === 'sent' && item.sentAt ? formatTime(item.sentAt) : '—'}</td>
                      <td class="px-2 py-1 text-muted-foreground break-words">{item.lastError || ''}</td>
                      <td class="px-2 py-1 text-right">
                        {#if item.status === 'queued'}
                          <Button size="sm" variant="ghost" disabled={actionBusy !== ''} onclick={() => void runAction(`cancel-item-${item.id}`, () => MailMerge_CancelQueuedItem(item.id))}>
                            Cancel
                          </Button>
                        {/if}
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          </section>
        {/each}
      {/if}

      {#if failedItems.length > 0}
        <p class="text-xs text-muted-foreground">
          Failed messages stopped after {failedItems[0]?.maxAttempts ?? 3} attempts. Use “Retry failed” on the run to queue them again.
        </p>
      {/if}
    {/if}
  </div>
</div>
