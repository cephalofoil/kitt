import { type Plugin, type PluginModule, tool } from '@opencode-ai/plugin'

import { type Answer, createSteps, runIn, TOOL } from './steps'

// The server half of pr-watch: the tools the model calls to act on the branch,
// and the rule that holds a merge until the person says yes. What is drawn
// above the prompt, and the keys, are the TUI half (tui.tsx).

// A command that merges a pull request: the CLI's own, or the API's merge endpoint.
const MERGE = ['gh pr merge*', 'gh api *pulls/*/merge*']

const said = (out: Answer): string => ('deny' in out ? out.deny : out.result)

const server: Plugin = async ({ directory }) => {
  const steps = createSteps(runIn(() => directory))

  return {
    // Merging is held until the person says yes, whoever asked for it and however
    // green the PR is: opencode's own permission prompt does the asking.
    config: async config => {
      const before = config.permission?.bash
      const rules = typeof before === 'string' ? { '*': before } : { '*': 'allow' as const, ...before }

      config.permission = { ...config.permission, bash: { ...rules, ...Object.fromEntries(MERGE.map(one => [one, 'ask' as const])) } }
    },

    tool: {
      [TOOL.commit]: tool({
        description:
          'Commit uncommitted changes of the current branch with the message you give. Stages everything, or only `paths` when given. ' +
          'Refuses on the default branch. Never pushes. Use it when the person asks to commit.',
        args: {
          subject: tool.schema.string().describe('The commit subject, in the form this repo uses'),
          body: tool.schema.string().optional().describe('Optional commit body'),
          paths: tool.schema.array(tool.schema.string()).optional().describe('Only these paths; leave out to commit everything'),
        },
        execute: async args => said(await steps.commit(args)),
      }),
      [TOOL.push]: tool({
        description:
          'Push the current branch to origin, setting its upstream on the first push. A refused push answers with the commits the ' +
          'remote holds; force_with_lease then overwrites exactly those, and only those. Refuses on the default branch.',
        args: {
          force_with_lease: tool.schema
            .boolean()
            .optional()
            .describe("Only after a refused push whose listed commits are this branch's own from before a rebase"),
        },
        execute: async args => said(await steps.push(args)),
      }),
      [TOOL.rebase]: tool({
        description:
          'Rebase the current branch onto the latest default branch, then push that branch to its own remote branch (first push sets the ' +
          'upstream, a later one uses --force-with-lease). Nothing is pushed to the default branch. A rebase that conflicts is ' +
          'undone and answered with the conflicting files. Needs a clean work tree. Refuses on the default branch.',
        args: {},
        execute: async () => said(await steps.rebase()),
      }),
    },
  }
}

const plugin: PluginModule = { id: 'kitt.pr-watch', server }

export default plugin
