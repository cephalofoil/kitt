// Package prwatch carries the opencode plugin of pr-watch into the kitt binary:
// kitt writes it where opencode loads plugins from (kitt opencode), so it needs
// no package registry.
package prwatch

import "embed"

// Files are the plugin's sources, at their paths in this folder.
//
//go:embed server.ts steps.ts tui.tsx shared/parse.ts shared/types.d.ts
var Files embed.FS
