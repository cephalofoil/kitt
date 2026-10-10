package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The backlog: a repo's open issues, to read one and to start a lane on it.

// Issue is an open issue, as far as reading it in the dashboard needs.
type Issue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	URL       string    `json:"url"`
	Author    string    `json:"author"`
	Milestone string    `json:"milestone,omitempty"`
	Labels    []string  `json:"labels,omitempty"`
	Assignees []string  `json:"assignees,omitempty"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
	Comments  int       `json:"comments"`
}

type issueCache struct {
	at     time.Time
	issues []Issue
}

var (
	issueMu    sync.Mutex
	issueByDir = map[string]issueCache{}
)

// issuesOf reads a repo's open issues, newest first, and keeps them for a while.
func issuesOf(repoPath string, maxAge time.Duration) ([]Issue, error) {
	issueMu.Lock()
	defer issueMu.Unlock()

	if cached, ok := issueByDir[repoPath]; ok && time.Since(cached.at) < maxAge {
		return cached.issues, nil
	}
	out, err := runTimeout(repoPath, 40*time.Second, "gh", "issue", "list", "--state", "open", "--limit", "200",
		"--json", "number,title,body,url,author,labels,assignees,milestone,createdAt,updatedAt,comments")
	if err != nil {
		return nil, fail("the issues could not be read: %s", firstLine(out+" "+err.Error()))
	}
	var list []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		URL    string `json:"url"`
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Assignees []struct {
			Login string `json:"login"`
		} `json:"assignees"`
		Milestone *struct {
			Title string `json:"title"`
		} `json:"milestone"`
		Created  time.Time         `json:"createdAt"`
		Updated  time.Time         `json:"updatedAt"`
		Comments []json.RawMessage `json:"comments"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fail("the issues could not be read: %v", err)
	}
	issues := make([]Issue, 0, len(list))
	for _, it := range list {
		issue := Issue{Number: it.Number, Title: it.Title, Body: it.Body, URL: it.URL, Author: it.Author.Login,
			Created: it.Created, Updated: it.Updated, Comments: len(it.Comments)}
		if it.Milestone != nil {
			issue.Milestone = it.Milestone.Title
		}
		for _, label := range it.Labels {
			issue.Labels = append(issue.Labels, label.Name)
		}
		for _, person := range it.Assignees {
			issue.Assignees = append(issue.Assignees, person.Login)
		}
		issues = append(issues, issue)
	}
	issueByDir[repoPath] = issueCache{at: time.Now(), issues: issues}
	return issues, nil
}

// matches says whether an issue has every word of a filter in its number,
// title, labels, author or milestone.
func (i Issue) matches(filter string) bool {
	text := strings.ToLower(fmt.Sprintf("#%d %s %s %s %s", i.Number, i.Title, strings.Join(i.Labels, " "), i.Author, i.Milestone))
	for _, word := range strings.Fields(strings.ToLower(filter)) {
		if !strings.Contains(text, word) {
			return false
		}
	}
	return true
}

func cmdBacklog(args []string) error {
	_, opts := flags(args, "json")
	repo, err := findRepo(opts["repo"])
	if err != nil {
		return err
	}
	issues, err := issuesOf(repo.Path, 0)
	if err != nil {
		return err
	}
	lanes := map[int]string{}
	for _, lane := range allLanes() {
		if lane.Repo == repo.Name && lane.State != nil && lane.State.Issue > 0 {
			lanes[lane.State.Issue] = lane.Name
		}
	}
	if opts["json"] != "" {
		out, _ := json.MarshalIndent(issues, "", "  ")
		fmt.Println(string(out))
		return nil
	}
	for _, issue := range issues {
		line := fmt.Sprintf("#%-5d %s %-4s %s", issue.Number, cut(issue.Title, 60), ago(issue.Updated), strings.Join(issue.Labels, ","))
		if name := lanes[issue.Number]; name != "" {
			line += "  lane " + name
		}
		fmt.Println(strings.TrimRight(line, " "))
	}
	return nil
}

// --- In the dashboard ----------------------------------------------------------

// backlog is the dashboard's second screen: the open issues of one repo, the
// one under the cursor to read beside them.
type backlog struct {
	open   bool
	repo   string
	issues []Issue
	loaded bool
	failed string
	cursor int
	// scroll is how far down the issue under the cursor is read.
	scroll int
	filter string
	typing bool
}

type issuesMsg struct {
	repo   string
	issues []Issue
	failed string
}

func loadIssues(repo RepoRef, maxAge time.Duration) tea.Cmd {
	return func() tea.Msg {
		issues, err := issuesOf(repo.Path, maxAge)
		if err != nil {
			return issuesMsg{repo: repo.Name, failed: err.Error()}
		}
		return issuesMsg{repo: repo.Name, issues: issues}
	}
}

// repoAtCursor is the repo of the row under the cursor, else the first one registered.
func (d dash) repoAtCursor() string {
	rows := d.visible()
	if d.cursor < len(rows) {
		return rows[d.cursor].Repo
	}
	if at := d.cursor - len(rows); at >= 0 && at < len(d.prs) {
		return d.prs[at].Repo
	}
	if repos := loadGlobal().Repos; len(repos) > 0 {
		return repos[0].Name
	}
	return ""
}

// openBacklog shows a repo's issues in place of the lanes.
func (d dash) openBacklog(name string) (tea.Model, tea.Cmd) {
	repo, err := findRepo(name)
	if err != nil {
		d.note, d.noteAt = err.Error(), time.Now()
		return d, nil
	}
	d.backlog = backlog{open: true, repo: repo.Name}
	return d, loadIssues(repo, time.Minute)
}

func (d dash) shownIssues() []Issue {
	if d.backlog.filter == "" {
		return d.backlog.issues
	}
	var shown []Issue
	for _, issue := range d.backlog.issues {
		if issue.matches(d.backlog.filter) {
			shown = append(shown, issue)
		}
	}
	return shown
}

// laneOfIssue is the lane a repo's issue is worked on in, if it has one.
func (d dash) laneOfIssue(repo string, number int) *LaneView {
	for i, row := range d.rows {
		if row.Managed && row.Repo == repo && row.State != nil && row.State.Issue == number {
			return &d.rows[i]
		}
	}
	return nil
}

// backlogKey is a key pressed in the backlog.
func (d dash) backlogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	b := &d.backlog
	shown := d.shownIssues()
	move := func(step int) {
		b.cursor, b.scroll = min(max(0, len(shown)-1), max(0, b.cursor+step)), 0
	}
	read := func(step int) {
		_, detailW, _, detailH, _ := d.backlogLayout(len(shown))
		longest := 0
		if b.cursor < len(shown) {
			longest = len(issueLines(shown[b.cursor], detailW)) - detailH
		}
		b.scroll = min(max(0, longest), max(0, b.scroll+step*max(1, detailH-2)))
	}

	// A filter being typed takes every letter, also the ones that are keys.
	if b.typing {
		switch msg.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			b.typing, b.filter, b.cursor, b.scroll = false, "", 0, 0
		case tea.KeyEnter:
			b.typing = false
		case tea.KeyUp:
			move(-1)
		case tea.KeyDown:
			move(1)
		case tea.KeyBackspace:
			if r := []rune(b.filter); len(r) > 0 {
				b.filter, b.cursor, b.scroll = string(r[:len(r)-1]), 0, 0
			}
		case tea.KeyRunes, tea.KeySpace:
			b.filter, b.cursor, b.scroll = b.filter+msg.String(), 0, 0
		}
		return d, nil
	}

	switch msg.String() {
	case "ctrl+c":
		return d, tea.Quit
	case "esc":
		// The filter goes first, then the backlog.
		if b.filter != "" {
			b.filter, b.cursor, b.scroll = "", 0, 0
			return d, nil
		}
		b.open = false
	case "b", "q":
		b.open = false
	case "up", "k":
		move(-1)
	case "down", "j":
		move(1)
	case "pgdown", " ", "ctrl+d":
		read(1)
	case "pgup", "ctrl+u":
		read(-1)
	case "/":
		b.typing = true
	case "r":
		if repo, err := findRepo(b.repo); err == nil {
			b.loaded = false
			return d, loadIssues(repo, 0)
		}
	case "tab":
		// The next repo's issues.
		repos := loadGlobal().Repos
		for i, repo := range repos {
			if repo.Name == b.repo && len(repos) > 1 {
				return d.openBacklog(repos[(i+1)%len(repos)].Name)
			}
		}
	case "o":
		if b.cursor < len(shown) {
			repo, err := findRepo(b.repo)
			if err != nil {
				return d, nil
			}
			number := fmt.Sprint(shown[b.cursor].Number)
			d.busy = "browser → issue #" + number
			return d, func() tea.Msg {
				if _, err := run(repo.Path, "gh", "issue", "view", number, "--web"); err != nil {
					return doneMsg(err.Error())
				}
				return doneMsg("opened issue #" + number)
			}
		}
	case "enter":
		if b.cursor >= len(shown) {
			return d, nil
		}
		issue := shown[b.cursor]
		// An issue that has a lane is gone into; one without gets its lane made.
		if row := d.laneOfIssue(b.repo, issue.Number); row != nil {
			d.mode, d.pending, d.title = "how", row.Name, "open "
			d.openings, d.choice = laneOpenings(*row, loadRepoConfig(row.Main, row.Path))
			return d, nil
		}
		d.mode, d.title = "how", "new lane "
		d.pending = strings.TrimSpace(cut(fmt.Sprintf("#%d %s", issue.Number, issue.Title), 56))
		d.openings, d.choice = newOpenings(fmt.Sprint(issue.Number), b.repo)
	}
	return d, nil
}

// backlogLayout divides the pane between the issues and the one being read:
// beside each other where the pane is wide, else the list above.
func (d dash) backlogLayout(count int) (listW, detailW, listH, detailH int, beside bool) {
	width, height := d.width, d.height
	if width <= 0 {
		width = 100
	}
	if height <= 0 {
		height = 30
	}
	// The header takes three lines, the status and the keys four.
	room := max(6, height-7)
	if width >= 110 {
		listW = min(84, width/2)
		return listW, width - listW - 5, room, room, true
	}
	listH = min(max(1, count), max(4, room/3))
	return width - 2, width - 4, listH, max(3, room-listH-1), false
}

var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

// issueLines is an issue to read, wrapped to a width: what it is, who and
// when, then its text with headings and code set apart.
func issueLines(issue Issue, width int) []string {
	width = max(20, width)
	wrap := func(text string, style lipgloss.Style) []string {
		var lines []string
		for _, line := range strings.Split(ansi.Wrap(text, width, ""), "\n") {
			lines = append(lines, style.Render(line))
		}
		return lines
	}
	plain := lipgloss.NewStyle()

	lines := wrap(fmt.Sprintf("#%d %s", issue.Number, issue.Title), bold)
	facts := []string{"by " + issue.Author, "opened " + ago(issue.Created) + " ago", "updated " + ago(issue.Updated) + " ago"}
	if issue.Comments == 1 {
		facts = append(facts, "1 comment")
	} else if issue.Comments > 1 {
		facts = append(facts, fmt.Sprintf("%d comments", issue.Comments))
	}
	if issue.Milestone != "" {
		facts = append(facts, "milestone "+issue.Milestone)
	}
	if len(issue.Assignees) > 0 {
		facts = append(facts, "assigned to "+strings.Join(issue.Assignees, ", "))
	}
	lines = append(lines, wrap(strings.Join(facts, " · "), dim)...)
	if len(issue.Labels) > 0 {
		lines = append(lines, wrap(strings.Join(issue.Labels, " · "), cyan)...)
	}
	lines = append(lines, "")

	body := strings.ReplaceAll(strings.ReplaceAll(issue.Body, "\r", ""), "\t", "  ")
	body = strings.TrimSpace(htmlComment.ReplaceAllString(body, ""))
	if body == "" {
		return append(lines, dim.Render("no description"))
	}
	code, blank := false, false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```"):
			code = !code
		case code:
			lines = append(lines, wrap("  "+line, dim)...)
		case trimmed == "":
			// One empty line between paragraphs is enough.
			if !blank {
				lines = append(lines, "")
			}
		case strings.HasPrefix(trimmed, "#"):
			lines = append(lines, wrap(strings.TrimSpace(strings.TrimLeft(trimmed, "#")), accent)...)
		default:
			lines = append(lines, wrap(strings.ReplaceAll(line, "**", ""), plain)...)
		}
		blank = trimmed == ""
	}
	return lines
}

func (d dash) backlogView(width int) string {
	b := d.backlog
	shown := d.shownIssues()
	listW, detailW, listH, detailH, beside := d.backlogLayout(len(shown))

	var out strings.Builder
	head := accent.Render("backlog") + dim.Render(" · ") + bold.Render(b.repo)
	switch {
	case !b.loaded:
		head += dim.Render(" · reading the issues…")
	case b.filter != "":
		head += dim.Render(fmt.Sprintf(" · %d of %d open issues", len(shown), len(b.issues)))
	default:
		head += dim.Render(fmt.Sprintf(" · %d open issues", len(b.issues)))
	}
	out.WriteString("\n  " + head + "\n\n")

	// The issues, as many as fit, the cursor kept in sight.
	var list []string
	first := min(max(0, len(shown)-listH), max(0, b.cursor-listH/2))
	const ageW, laneW = 4, 6
	labelW := min(14, max(0, listW-50))
	titleW := max(10, listW-(2+7+ageW+laneW+labelW+3))
	for i := first; i < len(shown) && i < first+listH; i++ {
		issue := shown[i]
		pointer, number := "  ", cut(fmt.Sprintf("#%d", issue.Number), 7)
		title := cut(issue.Title, titleW)
		if i == b.cursor {
			pointer, number, title = accent.Render("▸ "), bold.Render(number), bold.Render(title)
		}
		lane := cut("", laneW)
		if d.laneOfIssue(b.repo, issue.Number) != nil {
			lane = green.Render(cut("◆ lane", laneW))
		}
		line := pointer + number + title + " "
		if labelW > 0 {
			line += cyan.Render(cut(strings.Join(issue.Labels, "·"), labelW)) + " "
		}
		list = append(list, line+dim.Render(cut(ago(issue.Updated), ageW))+" "+lane)
	}
	switch {
	case b.failed != "":
		list = []string{"  " + red.Render(cut(b.failed, max(10, listW-2)))}
	case b.loaded && len(b.issues) == 0:
		list = []string{"  " + dim.Render("no open issues")}
	case b.loaded && len(shown) == 0:
		list = []string{"  " + dim.Render("no issue matches")}
	}

	var detail []string
	if b.cursor < len(shown) {
		all := issueLines(shown[b.cursor], detailW)
		from := min(b.scroll, max(0, len(all)-detailH))
		detail = all[from:min(len(all), from+detailH)]
		// The last line says there is more below.
		if from+detailH < len(all) {
			detail[len(detail)-1] = dim.Render(fmt.Sprintf("… %d more lines · space reads on", len(all)-from-detailH+1))
		}
	}

	if beside {
		for i := 0; i < max(len(list), len(detail)); i++ {
			left, right := "", ""
			if i < len(list) {
				left = list[i]
			}
			if i < len(detail) {
				right = detail[i]
			}
			out.WriteString(left + strings.Repeat(" ", max(0, listW-lipgloss.Width(left))) + dim.Render(" │ ") + right + "\n")
		}
	} else {
		out.WriteString(strings.Join(list, "\n") + "\n  " + dim.Render(strings.Repeat("─", max(0, width-4))) + "\n")
		for _, line := range detail {
			out.WriteString("  " + line + "\n")
		}
	}

	out.WriteString("\n")
	switch {
	case d.mode == "prompt":
		out.WriteString("  " + accent.Render(d.title+d.pending) + dim.Render(" · what Claude is to do: ") + d.input + "▏\n")
	case b.typing:
		out.WriteString("  " + accent.Render("filter") + dim.Render(" · words of the title, a label, a number: ") + b.filter + "▏\n")
	case d.busy != "":
		out.WriteString("  " + yellow.Render("… "+d.busy) + "\n")
	case b.filter != "":
		out.WriteString("  " + dim.Render("filter: ") + b.filter + dim.Render(" · esc clears it") + "\n")
	case d.note != "" && time.Since(d.noteAt) < 20*time.Second:
		out.WriteString("  " + cut(d.note, width-4) + "\n")
	default:
		out.WriteString("\n")
	}

	keys := [][2]string{{"enter", "start a lane"}, {"space", "read on"}, {"/", "filter"}, {"o", "browser"}, {"r", "reload"}, {"tab", "next repo"}, {"esc", "back"}}
	var legend []string
	for _, key := range keys {
		legend = append(legend, accent.Render(key[0])+" "+dim.Render(key[1]))
	}
	out.WriteString("\n  " + ansi.Truncate(strings.Join(legend, dim.Render(" · ")), max(10, width-4), "…") + "\n")
	return out.String()
}
