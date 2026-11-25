// Exhausted offline writes store (audit4 finding 21).
//
// The offline queue gives up on a row after 3 failed tries. Until now
// that edit silently never reached the server. The backend publishes
// `calendar:write-exhausted` when a row's budget runs out; this store
// loads the exhausted rows so the calendar pane can show them with
// retry/discard actions.

// @ts-ignore - wailsjs bindings
import {
  Calendar_ListExhaustedWrites,
  Calendar_RetryExhaustedWrite,
  Calendar_DiscardExhaustedWrite,
} from '$wailsjs/go/app/App.js'
// @ts-ignore - wailsjs bindings
import { EventsOn } from '$wailsjs/runtime/runtime.js'
// @ts-ignore - wailsjs bindings
import type { backend } from '$wailsjs/go/models'

type ExhaustedWrite = backend.ExhaustedWrite

let writes = $state<ExhaustedWrite[]>([])
let loading = $state(false)
let lastError = $state<string | null>(null)
let subscribed = false

function ensureSubscriptions() {
  if (subscribed) return
  subscribed = true
  // A row becoming exhausted, or a sync completing (Drain runs after
  // sync), can change the list — reload either way.
  EventsOn('calendar:write-exhausted', () => {
    void load()
  })
  EventsOn('calendar:sync-complete', () => {
    void load()
  })
}

async function load() {
  ensureSubscriptions()
  loading = true
  lastError = null
  try {
    writes = (await Calendar_ListExhaustedWrites()) || []
  } catch (err) {
    lastError = (err as Error)?.message ?? String(err)
    console.error('Failed to load exhausted calendar writes:', err)
  } finally {
    loading = false
  }
}

async function retry(id: string) {
  await Calendar_RetryExhaustedWrite(id)
  await load()
}

async function discard(id: string) {
  await Calendar_DiscardExhaustedWrite(id)
  await load()
}

export const exhaustedWrites = {
  get writes() {
    return writes
  },
  get loading() {
    return loading
  },
  get lastError() {
    return lastError
  },
  load,
  retry,
  discard,
}
