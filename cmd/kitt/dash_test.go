package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// calls lists openings as the kitt calls behind them, a "?" after one that asks for a prompt first.
func calls(list []opening) []string {
	var out []string
	for _, opening := range list {
		call := strings.Join(opening.args, " ")
		if opening.ask {
			call += " ?"
		}
		out = append(out, call)
	}
	return out
}

func same(t *testing.T, what string, got []string, chosen int, want []string, wantChosen int) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") || chosen != wantChosen {
		t.Errorf("%s: starts on %d, want %d\n got  %s\n want %s", what, chosen, wantChosen, strings.Join(got, " | "), strings.Join(want, " | "))
	}
}

func TestNewOpenings(t *testing.T) {
	list, chosen := newOpenings("tryout", "r")
	same(t, "a name", calls(list), chosen, []string{
		"new tryout --repo r --blank --focus",
		"new tryout --repo r --blank --up --focus",
		"new tryout --repo r --up ?",
	}, 1)

	list, chosen = newOpenings("412 web admin", "r")
	same(t, "an issue and its apps", calls(list), chosen, []string{
		"new 412 --repo r --apps web,admin --blank --focus",
		"new 412 --repo r --apps web,admin --blank --up --focus",
		"new 412 --repo r --apps web,admin --up",
	}, 2)
}

func TestLaneOpenings(t *testing.T) {
	cfg := RepoConfig{Apps: []App{
		{Name: "mobile", Kind: "expo"},
		{Name: "api", Kind: "backend"},
		{Name: "supabase", Kind: "backend", Shared: true},
	}}
	row := func(change func(*LaneView)) LaneView {
		view := LaneView{Lane: Lane{Repo: "r", Name: "lane", Managed: true}}
		change(&view)
		return view
	}

	// Nothing of the lane is open or running: the dialog starts on all of it.
	list, chosen := laneOpenings(row(func(v *LaneView) { v.Up = []string{"supabase"} }), cfg)
	same(t, "a lane that is closed", calls(list), chosen, []string{
		"focus r/lane --blank",
		"focus r/lane --blank --no-up",
		"focus r/lane",
		"focus r/lane ?",
	}, 0)

	// Open, an agent at work, servers of its own: go to it, or add to what is there.
	list, chosen = laneOpenings(row(func(v *LaneView) {
		v.Workspace, v.AgentPane, v.Up = "w1", "p1", []string{"mobile", "supabase", "api"}
		v.State = &LaneState{Issue: 412}
	}), cfg)
	same(t, "a lane at work", calls(list), chosen, []string{
		"focus r/lane --no-up",
		"focus r/lane --new-agent --no-up",
		"focus r/lane --restart",
		"focus r/lane --no-up ?",
	}, 0)
	if hint := list[2].hint; !strings.Contains(hint, "mobile, api") || strings.Contains(hint, "supabase") {
		t.Errorf("restart names %q: the lane's own servers, not the shared one", hint)
	}

	// Open but idle, with an issue: the agent can start on it by itself.
	list, chosen = laneOpenings(row(func(v *LaneView) { v.Workspace, v.State = "w1", &LaneState{Issue: 412} }), cfg)
	same(t, "an open issue lane with nothing running", calls(list), chosen, []string{
		"focus r/lane --no-up",
		"focus r/lane --blank",
		"focus r/lane --blank --no-up",
		"focus r/lane",
		"focus r/lane --agent",
	}, 0)
}

func TestOverlay(t *testing.T) {
	screen := strings.Repeat(red.Render("abcdefghijklmnopqrst")+"\n", 6) + "short"
	got := strings.Split(overlay(screen, "╭────╮\n│ hi │\n╰────╯", 20, 7), "\n")
	if len(got) != 7 {
		t.Fatalf("the screen has %d lines after the window, 7 before", len(got))
	}
	// Seven lines, a window of three: lines 2 to 4, columns 7 to 12.
	for i, line := range got {
		plain := ansi.Strip(line)
		switch {
		case i == 3 && plain != "abcdefg│ hi │nopqrst":
			t.Errorf("line %d = %q: the screen is not kept beside the window", i, plain)
		case (i < 2 || i == 5) && plain != "abcdefghijklmnopqrst":
			t.Errorf("line %d = %q: a line the window does not reach has changed", i, plain)
		}
	}
	// A screen shorter than the window, as before the lanes are read, still takes it.
	if out := overlay("", "│ hi │", 20, 0); !strings.Contains(out, "│ hi │") {
		t.Errorf("an empty screen lost the window: %q", out)
	}
}

// keys feeds the dashboard what a person types.
func keys(d dash, typed string, then ...tea.KeyType) dash {
	for _, r := range typed {
		model, _ := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		d = model.(dash)
	}
	for _, key := range then {
		model, _ := d.Update(tea.KeyMsg{Type: key})
		d = model.(dash)
	}
	return d
}

func TestNewLaneDialog(t *testing.T) {
	t.Setenv("KITT_HOME", t.TempDir())

	// A name: the dialog opens on a blank Claude with the dev servers.
	d := keys(dash{mode: "new"}, "tryout", tea.KeyEnter)
	if d.mode != "how" || d.pending != "tryout" || d.choice != 1 || len(d.openings) != 3 {
		t.Fatalf("after a name: mode %q, pending %q, choice %d of %d", d.mode, d.pending, d.choice, len(d.openings))
	}
	if view := d.dialog(80); !strings.Contains(view, "new lane tryout") || !strings.Contains(view, "a blank Claude and the dev servers") {
		t.Errorf("the dialog does not show the lane and its openings:\n%s", view)
	}
	// Up and down stay inside the openings, and a digit past them does nothing.
	if d = keys(d, "", tea.KeyUp, tea.KeyUp, tea.KeyUp); d.choice != 0 {
		t.Errorf("choice after three ups = %d, want 0", d.choice)
	}
	if d = keys(d, "7"); d.mode != "how" || d.choice != 0 {
		t.Errorf("after a digit with no opening: mode %q, choice %d", d.mode, d.choice)
	}
	// A name told to start by itself has no prompt yet: it is asked for, and up no longer changes the choice.
	if d = keys(d, "3"); d.mode != "prompt" || d.choice != 2 {
		t.Fatalf("after 3 on a name: mode %q, choice %d", d.mode, d.choice)
	}
	if d = keys(d, "go", tea.KeyUp); d.choice != 2 || d.input != "go" {
		t.Errorf("while typing the prompt: choice %d, input %q", d.choice, d.input)
	}

	// An issue: the dialog opens on the agent starting by itself, and esc leaves it.
	d = keys(dash{mode: "new"}, "412 web", tea.KeyEnter)
	if d.mode != "how" || d.choice != 2 {
		t.Fatalf("after an issue: mode %q, choice %d", d.mode, d.choice)
	}
	if d = keys(d, "", tea.KeyEsc); d.mode != "" {
		t.Errorf("mode after esc = %q", d.mode)
	}
}

func TestEnterOnALane(t *testing.T) {
	t.Setenv("KITT_HOME", t.TempDir())
	row := LaneView{Lane: Lane{Repo: "r", Name: "lane", Managed: true}, Workspace: "w1", AgentPane: "p1"}
	d := keys(dash{rows: []LaneView{row}}, "", tea.KeyEnter)
	if d.mode != "how" || d.title != "open " || d.choice != 0 {
		t.Fatalf("after enter: mode %q, title %q, choice %d", d.mode, d.title, d.choice)
	}
	view := d.dialog(90)
	for _, want := range []string{"open lane", "go to it", "a new Claude", "tell Claude something"} {
		if !strings.Contains(view, want) {
			t.Errorf("the dialog of an open lane with an agent lacks %q:\n%s", want, view)
		}
	}
	// Telling the agent something asks what.
	if d = keys(d, "", tea.KeyDown, tea.KeyDown, tea.KeyDown, tea.KeyEnter); d.mode != "prompt" {
		t.Errorf("after choosing to tell Claude something: mode %q", d.mode)
	}
}
