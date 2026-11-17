package main

import (
	"os/exec"
	"slices"
	"strings"
	"sync"
)

// The open dialog asks which agent a lane opens with. The openings are made
// for the lane's own kind (laneOpenings, newOpenings); forKind turns them into
// ones for the kind chosen with tab.

var installed = sync.OnceValue(func() []string {
	var kinds []string
	for _, kind := range agentKinds {
		if _, err := exec.LookPath(kind); err == nil {
			kinds = append(kinds, kind)
		}
	}
	return kinds
})

// startsAgent reports whether an opening starts an agent or speaks to one.
func startsAgent(o opening) bool {
	if o.ask || (len(o.args) > 0 && o.args[0] == "new") {
		return true
	}
	return slices.ContainsFunc(o.args, func(a string) bool { return a == "--blank" || a == "--agent" || a == "--new-agent" })
}

// forKind words the openings for an agent of kind, and has those that start
// one pass --with where kind is not the one the lane would start anyway.
func forKind(list []opening, kind, usual string) []opening {
	out := make([]opening, len(list))
	name := agentLabel(kind)
	for i, o := range list {
		if startsAgent(o) {
			o.label = strings.ReplaceAll(o.label, "Claude", name)
			o.hint = strings.ReplaceAll(o.hint, "Claude", name)
			if kind != usual && kind != "" {
				o.args = append(append([]string{}, o.args...), "--with", kind)
			}
		}
		out[i] = o
	}
	return out
}

// how opens the dialog on these openings, with the lane's usual agent kind.
func (d *dash) how(list []opening, chosen int, usual string) {
	if usual == "" {
		usual = kindClaude
	}
	d.made, d.usual, d.kind = list, usual, usual
	d.openings, d.choice = forKind(list, usual, usual), chosen
}

// nextKind switches the dialog to the next installed agent kind.
func (d *dash) nextKind() {
	kinds := installed()
	if len(kinds) < 2 {
		return
	}
	at := slices.Index(kinds, d.kind)
	d.kind = kinds[(at+1)%len(kinds)]
	d.openings = forKind(d.made, d.kind, d.usual)
}

// kindLine is the dialog's line naming the agent kinds, the chosen one marked.
func (d dash) kindLine() string {
	kinds := installed()
	if len(kinds) < 2 {
		return ""
	}
	var names []string
	for _, kind := range kinds {
		if kind == d.kind {
			names = append(names, bold.Render(agentLabel(kind)))
		} else {
			names = append(names, dim.Render(agentLabel(kind)))
		}
	}
	return dim.Render("agent  ") + strings.Join(names, dim.Render(" · ")) + dim.Render("   tab switches")
}

// laneUsualKind is the kind a lane's agent starts as when none is chosen.
func laneUsualKind(row LaneView, cfg RepoConfig) string {
	return laneAgentKind(row.Lane, cfg, "")
}

// repoUsualKind is a repo's [agent] kind, for a lane yet to be made.
func repoUsualKind(repo string) string {
	ref, err := findRepo(repo)
	if err != nil {
		return kindClaude
	}
	return loadRepoConfig(ref.Path, "").Agent.Kind
}
