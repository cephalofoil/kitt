export type PrWatchCheckState = 'pass' | 'fail' | 'running' | 'queued' | 'skipped'

export type PrWatchCheck = {
  name: string
  state: PrWatchCheckState
  seconds: number | null
  runId: string | null
  jobId: string | null
}

export type PrWatchLog = { jobId: string; step: string; lines: string[] }

export type PrWatchIssue = { number: number; title: string; done: number; total: number }

export type PrWatchSnapshot = {
  number: number
  title: string
  state: 'OPEN' | 'MERGED' | 'CLOSED'
  isDraft: boolean
  headSha: string
  branch: string
  issue: PrWatchIssue | null
  checks: PrWatchCheck[]
  logs: PrWatchLog[]
}

export type PrWatchGit = {
  branch: string
  head: string
  modified: number
  untracked: number
  /** Commits the branch lacks from the remote's default branch, and has on top of it. */
  behind: number
  ahead: number
  /** Commits not on the branch's upstream yet; null when it has none. */
  unpushed: number | null
  /** Files a rebase would likely conflict in; null when it could not be tried. */
  conflicts: string[] | null
  /** The remote's default branch, as `origin/main`; empty when none was found. */
  base: string
  baseSha: string
}

declare module 'claude-code' {
  interface PluginState {
    'pr-watch': {
      snapshot: PrWatchSnapshot | null
      git: PrWatchGit | null
      hiddenPr: string | null
      hiddenRebase: string | null
      /** The head a plain push was refused for: the remote branch holds other commits. */
      rejected: string | null
      isLoaded: boolean
    }
  }
}
