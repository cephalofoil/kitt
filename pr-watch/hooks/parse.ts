import type { PrWatchCheck, PrWatchCheckState, PrWatchSnapshot } from '../types'

type RollupItem = {
  __typename?: string
  name?: string
  context?: string
  status?: string
  state?: string
  conclusion?: string
  startedAt?: string
  completedAt?: string
  detailsUrl?: string
}

export type Overall = 'failed' | 'running' | 'green' | 'merged' | 'closed'

const FAILED = new Set(['FAILURE', 'TIMED_OUT', 'CANCELLED', 'ACTION_REQUIRED', 'STARTUP_FAILURE', 'ERROR'])
const SKIPPED = new Set(['SKIPPED', 'NEUTRAL'])

const stateOf = (item: RollupItem): PrWatchCheckState => {
  if (item.__typename === 'StatusContext') {
    if (item.state === 'SUCCESS') return 'pass'
    if (item.state === 'PENDING' || item.state === 'EXPECTED') return 'running'

    return 'fail'
  }
  if (item.status !== 'COMPLETED') {
    return item.status === 'IN_PROGRESS' ? 'running' : 'queued'
  }
  if (FAILED.has(item.conclusion ?? '')) return 'fail'
  if (SKIPPED.has(item.conclusion ?? '')) return 'skipped'

  return 'pass'
}

const time = (text: string | undefined): number | null => {
  const ms = text === undefined ? NaN : Date.parse(text)

  return Number.isNaN(ms) || ms <= 0 ? null : ms
}

export const parseChecks = (rollup: readonly RollupItem[], now: number): PrWatchCheck[] => {
  const latest = new Map<string, { check: PrWatchCheck; startedAt: number }>()

  for (const item of rollup) {
    const name = item.name ?? item.context ?? 'check'
    const state = stateOf(item)
    const startedAt = time(item.startedAt)
    const completedAt = time(item.completedAt)
    const end = state === 'running' ? now : completedAt
    const ids = /\/actions\/runs\/(\d+)\/job\/(\d+)/.exec(item.detailsUrl ?? '')
    const check: PrWatchCheck = {
      name,
      state,
      seconds:
        startedAt === null || end === null || state === 'skipped' || state === 'queued'
          ? null
          : Math.max(0, Math.round((end - startedAt) / 1000)),
      runId: ids?.[1] ?? null,
      jobId: ids?.[2] ?? null,
    }
    const seen = latest.get(name)

    if (seen === undefined || (startedAt ?? 0) >= seen.startedAt) {
      latest.set(name, { check, startedAt: startedAt ?? 0 })
    }
  }

  return [...latest.values()].map(one => one.check)
}

const ANSI = /\u001b\[[0-9;]*[A-Za-z]/g
const STAMP = /^﻿?\d{4}-\d{2}-\d{2}T[\d:.]+Z\s?/

/** `gh run view --log-failed` prints `job<TAB>step<TAB>timestamp text` per line. */
export const parseLog = (text: string): { step: string; lines: string[] } => {
  let step = ''
  const lines: string[] = []

  for (const raw of text.split(/\r?\n/)) {
    const parts = raw.split('\t')
    const body = (parts.length >= 3 ? parts.slice(2).join('\t') : raw)
      .replace(ANSI, '')
      .replace(STAMP, '')
      .trimEnd()

    if (body.trim() === '' || /^##\[(group|endgroup|command|debug)\]/.test(body)) continue
    if (parts.length >= 3) step = parts[1] ?? step
    lines.push(body.replace(/^##\[error\]/, '').trim())
  }

  return { step, lines: lines.slice(-40) }
}

/** The lines worth a row in the pane: the ones that name the failure, else the tail. */
export const headline = (lines: readonly string[], most: number): string[] => {
  const telling = lines.filter(line =>
    /\b(error|failed|failure|FAIL|AssertionError|Exception|not ok)\b/.test(line) &&
    !/Process completed with exit code/.test(line),
  )

  return (telling.length > 0 ? telling : lines).slice(-most)
}

export const countBoxes = (body: string): { done: number; total: number } => {
  const done = (body.match(/^\s*[-*] \[[xX]\]/gm) ?? []).length
  const open = (body.match(/^\s*[-*] \[ \]/gm) ?? []).length

  return { done, total: done + open }
}

export const duration = (seconds: number | null): string => {
  if (seconds === null) return ''
  if (seconds < 60) return `${seconds}s`

  return `${Math.floor(seconds / 60)}m ${String(seconds % 60).padStart(2, '0')}s`
}

export const overall = (snapshot: PrWatchSnapshot): Overall => {
  if (snapshot.state === 'MERGED') return 'merged'
  if (snapshot.state === 'CLOSED') return 'closed'
  if (snapshot.checks.some(check => check.state === 'fail')) return 'failed'
  if (snapshot.checks.some(check => check.state === 'running' || check.state === 'queued')) return 'running'

  return 'green'
}

export const issueNumber = (
  closing: readonly { number?: number }[] | undefined,
  title: string,
  body: string,
): number | null => {
  const first = closing?.[0]?.number

  if (typeof first === 'number') return first

  const found =
    /\(#(\d+)\)\s*$/.exec(title) ?? /\b(?:clos\w*|fix\w*|resolv\w*)\s+#(\d+)/i.exec(body)

  return found === null ? null : Number(found[1])
}
