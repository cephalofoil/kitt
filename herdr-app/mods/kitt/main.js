// kitt: the lanes of a repo (one git worktree per ticket, with its ports, dev
// servers and agent), as the kitt TUI dashboard shows them, inside herdr-app.
// A lane is a space here: kitt makes it (`kitt --herdr=app ...`), this mod finds
// it again by folder or name and reads the agent state live from Herdr.
//
// One file, three roles, told apart by props():
//   - none / layout block: the dashboard (the owner of every kitt call it starts),
//   - { dialog: "open" | "new" | "backlog" | "remove" | "phone", owner, ... }: a dialog.
// A dialog never runs a long kitt call itself (closing it would cancel the call):
// it hands the call to its owner through shared("kitt.requests").

import { View, div, svg } from "gpui-kit";
import { v_flex, h_flex, InputState, Input, Scrollbar } from "gpui-base";
import {
  exec, run, theme, spaces, agents, notify, openOverlay, closeOverlay, props, shared, setShared,
  nextCommand, checking, home, layout, settings,
} from "herdr";

function findNode(node, id) {
  if (node?.id === id) return node;
  for (const c of node?.children ?? []) { const f = findNode(c, id); if (f) return f; }
  return null;
}

const ICON = (name) => `mods/kitt/${name}.svg`;

// The kitt wordmark of the TUI dashboard (cmd/kitt/logo.go, logoWord): "ki" in the
// text colour, "tt" in the dashboard's blue (ANSI 12). The TUI draws two pixel rows
// per line in half blocks; here each pixel is a square of its own.
const LOGO = [
  "ww.......ww....bb.....bb..",
  "ww.......ww....bb.....bb..",
  "ww...........bbbbbb.bbbbbb",
  "ww..www..ww..bbbbbb.bbbbbb",
  "ww.www...ww....bb.....bb..",
  "wwwww....ww....bb.....bb..",
  "ww.www...ww....bbbb...bbbb",
  "ww..www.wwww....bbb....bbb",
];

function wordmark(t, px) {
  const color = { w: t.foreground, b: t.ansi12 ?? t.accent };
  return v_flex().flex_none().children(LOGO.map((row) => h_flex().children([...row].map((c) =>
    div().w(px).h(px).flex_none().when(c !== ".", (el) => el.bg(color[c]))))));
}
const RANK = { blocked: 4, done: 3, working: 2, idle: 1 };
const POLL = 20000; // kitt ls: every 20 s, and after every action
const PR_POLL = 90000; // open PRs without a lane (gh): every 90 s
const LONG = 600000; // new, pr, focus, up: installs and servers can take minutes

// Paths compare as Windows does: either slash, any case, no trailing slash.
const norm = (p) => (p ?? "").replace(/\\/g, "/").replace(/\/+$/, "").toLowerCase();
const keyOf = (l) => `${l.Repo}/${l.Name}`;
const issueOf = (l) => Number(/^(\d+)-/.exec(l?.Name ?? "")?.[1] ?? 0);
const lastLine = (s) => (s ?? "").replace(/\r/g, "").trim().split("\n").filter(Boolean).pop() ?? "";
const clean = (s) => lastLine(s).replace(/^kitt:\s*/, "");

// The installed kitt may predate `--herdr=app`: then it is asked without it
// (its herdr is the TUI's) for a while, and asked with it again later.
let legacyUntil = 0;
async function kitt(args, { cwd, timeout = 60000 } = {}) {
  const opts = { cwd: cwd || home(), timeout };
  if (Date.now() >= legacyUntil) {
    const r = await exec("kitt", ["--herdr=app", ...args], opts);
    if (!(r.code !== 0 && /unknown command "--herdr/.test(`${r.stderr}${r.stdout}`))) return r;
    legacyUntil = Date.now() + 5 * 60000;
  }
  const r = await exec("kitt", args, opts);
  r.legacy = true;
  return r;
}

// The space a lane is: the one kitt names (Space), else the one in its folder, else one named like it.
function spaceOf(lane, list) {
  if (lane.Space) {
    const s = list.find((x) => x.id === lane.Space);
    if (s) return s;
  }
  const at = norm(lane.Path);
  return list.find((s) => norm(s.folder) === at) ?? list.find((s) => s.named && s.name === lane.Name) ?? null;
}

const statusWord = { blocked: "needs you", done: "finished", working: "working", idle: "idle" };
// A lane's own dev servers: shared ones (one database for all lanes) are up for every lane alike.
const own = (l) => (Array.isArray(l.Servers) ? l.Servers.filter((s) => !s.shared) : []);
const up = (l) => own(l).filter((s) => s.up);

// What the TUI's dialog offers for a lane that exists, by what it has already.
function laneOpenings(lane, space, hasAgent) {
  const target = keyOf(lane);
  const focus = (...more) => ["focus", target, ...more];
  const running = up(lane).map((s) => s.app);
  const list = [];
  let chosen = 0;
  if (space) list.push({ label: "go to it", hint: "its space as it is: nothing is started", select: space.id });
  if (!hasAgent && running.length === 0) {
    if (!space) chosen = list.length;
    list.push({ label: "a blank Claude and the dev servers", hint: "an agent to talk to, its apps started on its ports, its app in the emulator", args: focus("--blank") });
  }
  if (hasAgent) list.push({ label: "a new Claude", hint: "one more agent, in a tab of its own: the one at work stays", args: focus("--new-agent", "--no-up") });
  else list.push({ label: "a blank Claude", hint: "an agent to talk to, no dev servers started", args: focus("--blank", "--no-up") });
  if (running.length) list.push({ label: "restart the dev servers", hint: `stopped and started again: ${running.join(", ")}`, args: focus("--restart") });
  else list.push({ label: "the dev servers", hint: "its apps started on its ports, its app in the emulator", args: focus() });
  if (hasAgent) list.push({ label: "tell Claude something", hint: "you type it next: it goes to the agent at work", args: focus("--no-up"), ask: true });
  else if (issueOf(lane)) list.push({ label: "Claude on the issue, through to a pull request", hint: "it reads the issue, builds it, proves it in the app and opens the PR; the dev servers start", args: focus("--agent") });
  else list.push({ label: "Claude with a prompt and the dev servers", hint: "you type what Claude is to do next, then it starts by itself", args: focus(), ask: true });
  return { list, chosen };
}

// How a lane that is yet to be made opens: "412 web admin" is an issue and its apps.
function newOpenings(input, repo) {
  const words = input.trim().split(/\s+/).filter(Boolean);
  const issue = /^\d+$/.test(words[0] ?? "");
  const base = issue && words.length > 1
    ? ["new", words[0], "--repo", repo, "--apps", words.slice(1).join(",")]
    : ["new", input.trim(), "--repo", repo];
  const list = [
    { label: "a blank Claude", hint: "the lane and an agent to talk to, no dev servers started", args: [...base, "--blank", "--focus"] },
    { label: "a blank Claude and the dev servers", hint: "the same, with the lane's apps started on its ports", args: [...base, "--blank", "--up", "--focus"] },
  ];
  if (issue) return { list: [...list, { label: "Claude on the issue, through to a pull request", hint: "it reads the issue, builds it, proves it in the app and opens the PR; the dev servers start", args: [...base, "--up"] }], chosen: 2 };
  return { list: [...list, { label: "Claude with a prompt and the dev servers", hint: "you type what Claude is to do next, then it starts by itself", args: [...base, "--up"], ask: true }], chosen: 1 };
}

// Hands a kitt call to the dashboard copy that opened the dialog.
function request(owner, req) {
  const q = Array.isArray(shared("kitt.requests")) ? shared("kitt.requests") : [];
  setShared("kitt.requests", [...q, { ...req, owner, id: `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`, at: Date.now() }]);
}

export default class Kitt extends View {
  init(_props, cx) {
    this.handle = cx.focus_handle();
    this.p = props() ?? {};
    if (this.p.dialog) return this.initDialog(cx);

    this.uid = `kitt-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
    this.lanes = null; // null: not read yet
    this.repos = [];
    this.prs = {}; // repo name -> open PRs without a lane
    this.sel = null; // a lane's key or "pr:<repo>#<n>"
    this.busy = []; // labels of kitt calls running
    this.note = null; // { text, error, at }
    this.width = 280;
    this.showAll = false;
    this.loading = false;
    this.error = null;
    try { this.repo = localStorage.getItem("repo") ?? ""; } catch { this.repo = ""; }
    try { this.showAll = localStorage.getItem("all") === "1"; } catch {}
    if (checking()) { this.lanes = []; return; }

    this.refresh(cx, true);
    cx.timer.every(POLL, (cx) => this.refresh(cx));
    cx.timer.every(PR_POLL, (cx) => this.refreshPRs(cx));
    cx.timer.every(300, (cx) => this.pump(cx));
    cx.spawn(async (cx) => {
      for (;;) {
        const c = await nextCommand();
        if (c === "focus") this.handle.focus();
        else if (c === "refresh") this.refresh(cx, true);
        else if (c === "new") this.newLane();
        else if (c === "backlog") this.backlog();
        cx.notify();
      }
    });
  }

  // ---- data ----------------------------------------------------------------

  refresh(cx, withRepos = false) {
    if (this.loading) { this.again = true; return; }
    this.loading = true;
    cx.notify();
    cx.spawn(async (cx) => {
      try {
        if (withRepos || !this.repos.length) await this.readRepos();
        const r = await kitt(["ls", "--all", "--json"], { timeout: 60000 });
        if (r.code !== 0) throw new Error(clean(r.stderr) || clean(r.stdout) || `kitt ls exited ${r.code}`);
        const list = JSON.parse(r.stdout.trim() || "[]");
        this.lanes = Array.isArray(list) ? list : [];
        this.error = null;
        if (withRepos || !this.prsAt) this.refreshPRs(cx);
      } catch (e) {
        this.error = String(e?.message ?? e);
        if (this.lanes === null) this.lanes = [];
      }
      this.loading = false;
      cx.notify();
      if (this.again) { this.again = false; this.refresh(cx); }
    });
  }

  async readRepos() {
    try {
      const r = await kitt(["repo", "list", "--json"], { timeout: 20000 });
      if (r.code === 0 && r.stdout.trim().startsWith("[")) {
        this.repos = JSON.parse(r.stdout).map((x) => ({ name: x.name ?? x.Name, path: x.path ?? x.Path })).filter((x) => x.name);
        return;
      }
      // An older kitt: its text list names each repo on a line of its own, details indented below.
      const t = await kitt(["repo", "list"], { timeout: 20000 });
      this.repos = (t.stdout ?? "").replace(/\r/g, "").split("\n")
        .map((line) => /^(\S+)\s+(\S.*)$/.exec(line))
        .filter(Boolean)
        .map((m) => ({ name: m[1], path: m[2].trim() }));
    } catch { /* the lanes still name their repos */ }
  }

  // Open pull requests a person opened that no lane has yet (gh, like the TUI).
  refreshPRs(cx) {
    if (this.prsLoading) return;
    this.prsLoading = true;
    this.prsAt = Date.now();
    cx.spawn(async (cx) => {
      const prs = {};
      for (const repo of this.knownRepos()) {
        if (!repo.path) continue;
        try {
          const r = await exec("gh", ["pr", "list", "--state", "open", "--limit", "50", "--json", "number,title,isDraft,headRefName,author,statusCheckRollup"], { cwd: repo.path, timeout: 45000 });
          if (r.code !== 0) continue;
          prs[repo.name] = JSON.parse(r.stdout || "[]").filter((p) => !p.author?.is_bot && !/\[bot\]$|^app\//.test(p.author?.login ?? "")).map((p) => ({
            number: p.number, title: p.title, draft: p.isDraft, branch: p.headRefName,
            checks: rollup(p.statusCheckRollup),
          }));
        } catch { /* gh missing or offline: no section */ }
      }
      this.prs = prs;
      this.prsLoading = false;
      cx.notify();
    });
  }

  knownRepos() {
    const out = [...this.repos];
    for (const l of this.lanes ?? []) {
      if (!out.some((r) => r.name === l.Repo)) out.push({ name: l.Repo, path: null });
      const r = out.find((x) => x.name === l.Repo);
      // No repo list: the lane in the main checkout tells where the repo is.
      if (r && !r.path && l.Managed && l.Slot === 0) r.path = l.Path;
    }
    return out;
  }

  repoPath(name) {
    return this.knownRepos().find((r) => r.name === name)?.path ?? home();
  }

  // ---- calls ---------------------------------------------------------------

  // Requests the dialogs left for this copy.
  pump(cx) {
    const q = shared("kitt.requests");
    if (!Array.isArray(q) || !q.length) return;
    const now = Date.now();
    const mine = q.filter((r) => r.owner === this.uid);
    const keep = q.filter((r) => r.owner !== this.uid && now - r.at < 120000);
    if (!mine.length && keep.length === q.length) return;
    setShared("kitt.requests", keep);
    for (const r of mine) this.call(cx, r);
  }

  // Runs one kitt call in the background: a spinner while it runs, its last line after.
  call(cx, req) {
    if (req.select) { run("space.select", { id: req.select }); this.toTerminals(); return; }
    const label = req.label ?? req.args.join(" ");
    this.busy = [...this.busy, label];
    cx.notify();
    cx.spawn(async (cx) => {
      let r;
      try { r = await kitt(req.args, { cwd: req.cwd, timeout: req.timeout ?? LONG }); }
      catch (e) { r = { code: -1, stdout: "", stderr: String(e?.message ?? e) }; }
      const at = this.busy.indexOf(label);
      if (at >= 0) this.busy = [...this.busy.slice(0, at), ...this.busy.slice(at + 1)];
      const said = r.code === 0 ? lastLine(r.stdout) || lastLine(r.stderr) : clean(r.stderr) || clean(r.stdout) || `exit ${r.code}`;
      if (r.code !== 0 && req.kind === "rm" && /--force/.test(`${r.stderr}${r.stdout}`)) {
        this.dialog("remove", { lane: req.lane, force: true, reason: said.split(" (pass --force")[0] }, 420, 210);
      } else if (r.code === 0 && req.kind === "phone") {
        const lines = (r.stdout ?? "").replace(/\r/g, "").trim().split("\n");
        this.dialog("phone", { lane: req.lane, link: lines.pop() ?? "", notes: lines.map((l) => /^\s*note\s*(.*)$/.exec(l)?.[1]).filter(Boolean) }, 460, 260);
      } else if (r.code !== 0) {
        notify({ title: `kitt ${req.args[0]} failed`, body: said, level: "error" });
      } else if (req.args[0] === "focus" || (req.args[0] === "new" && req.args.includes("--focus"))) {
        // Gone into a lane: its space is shown, so are its terminals.
        this.toTerminals();
      }
      this.note = { text: r.code === 0 ? said || `${label}: done` : said, error: r.code !== 0, at: Date.now() };
      if (r.legacy && !this.warnedLegacy) {
        this.warnedLegacy = true;
        notify({ title: "kitt without --herdr", body: "The installed kitt does not know --herdr=app yet: it worked with the herdr TUI. Rebuild kitt.", level: "warning" });
      }
      cx.notify();
      this.refresh(cx);
    });
  }

  quick(cx, lane, args, label, extra = {}) {
    this.call(cx, { args, label, cwd: this.repoPath(lane?.Repo), lane, ...extra });
  }

  dialog(kind, extra, width, height) {
    openOverlay({ mod: "kitt", kind: "dialog", width, height, props: { dialog: kind, owner: this.uid, ...extra } });
  }

  // ---- actions (keys and buttons) -------------------------------------------

  item() {
    return this.items().find((i) => i.key === this.sel) ?? null;
  }

  openLane(lane) {
    if (!lane.Managed) return this.say(`${lane.Name} is not a lane yet: a adopts it`);
    const space = spaceOf(lane, spaces());
    const hasAgent = Boolean(space && agents().some((a) => a.space === space.id)) || Boolean(lane.Agent);
    const { list, chosen } = laneOpenings(lane, space, hasAgent);
    this.dialog("open", { title: "open", name: lane.Name, lane: slim(lane), options: list, chosen, cwd: this.repoPath(lane.Repo) }, 480, 140 + list.length * 46);
  }

  newLane(prefill = "") {
    const repo = this.currentRepo();
    if (!repo) return this.say("no repo registered: kitt repo add inside one");
    this.dialog("new", { repo: repo.name, cwd: repo.path ?? home(), value: prefill }, 480, 236);
  }

  backlog() {
    const repo = this.currentRepo();
    if (!repo) return this.say("no repo registered: kitt repo add inside one");
    const sp = spaces();
    const lanes = (this.lanes ?? []).filter((l) => l.Repo === repo.name && l.Managed && issueOf(l))
      .map((l) => ({ issue: issueOf(l), key: keyOf(l), name: l.Name, space: spaceOf(l, sp)?.id ?? null }));
    this.dialog("backlog", { repo: repo.name, cwd: repo.path ?? home(), lanes }, 620, 0.75);
  }

  currentRepo() {
    const repos = this.knownRepos();
    const lane = this.item()?.lane;
    return repos.find((r) => r.name === (this.repo || lane?.Repo)) ?? repos[0] ?? null;
  }

  // A lane's check runs in a tab of its space, so its output stays to be read.
  check(lane) {
    const argv = ["kitt", "--herdr=app", "check", keyOf(lane)];
    const space = spaceOf(lane, spaces());
    if (space) run("tab.new", { space: space.id, name: "check", argv, folder: lane.Path, focus: true });
    else run("space.new", { name: lane.Name, folder: lane.Path, tab: "check", argv, focus: true });
  }

  act(cx, k) {
    const it = this.item();
    if (k === "r") return this.refresh(cx, true);
    if (k === "n") return this.newLane();
    if (k === "b") return this.backlog();
    if (k === "t") { this.showAll = !this.showAll; try { localStorage.setItem("all", this.showAll ? "1" : "0"); } catch {} return cx.notify(); }
    if (!it) return;
    if (it.pr) {
      const { repo, pr } = it;
      if (k === "enter") return this.call(cx, { args: ["pr", String(pr.number), "--repo", repo], label: `PR #${pr.number} → lane`, cwd: this.repoPath(repo) });
      if (k === "o") { cx.spawn(async () => { await exec("gh", ["pr", "view", String(pr.number), "--web"], { cwd: this.repoPath(repo) }); }); return this.say(`opened PR #${pr.number}`); }
      return;
    }
    const lane = it.lane;
    const target = keyOf(lane);
    const managedOnly = () => (lane.Managed ? false : (this.say(`${lane.Name} is not a lane yet: a adopts it`), true));
    switch (k) {
      case "enter": return this.openLane(lane);
      case "g": if (managedOnly()) return; return this.quick(cx, lane, ["agent", target], `agent for ${lane.Name}`);
      case "e": return this.quick(cx, lane, ["emu", target], `emulator → ${lane.Name}`);
      case "o": return this.quick(cx, lane, ["open", target], `browser → ${lane.Name}`, { timeout: 60000 });
      case "u": return this.quick(cx, lane, ["up", target], `starting servers of ${lane.Name}`);
      case "d": return this.quick(cx, lane, ["down", target], `stopping servers of ${lane.Name}`, { timeout: 120000 });
      case "i": if (managedOnly()) return; return this.quick(cx, lane, ["setup", target], `installing ${lane.Name}`);
      case "h": if (managedOnly()) return; return this.quick(cx, lane, ["phone", target, "--link"], `phone → ${lane.Name}`, { kind: "phone" });
      case "c": return this.check(lane);
      case "p":
        if (!lane.Proof) return this.say(`${lane.Name} has no proof yet`);
        return this.quick(cx, lane, ["proof", "open", target], "opening proof", { timeout: 60000 });
      case "a":
        if (lane.Managed) return this.say(`${lane.Name} is a lane already`);
        return this.quick(cx, lane, ["adopt", lane.Path], `adopting ${lane.Name}`);
      case "x":
        if (this.isMain(lane)) return this.say("the main checkout is not removed");
        if (!lane.Managed) return this.say(`${lane.Name} is not a lane`);
        return this.dialog("remove", { lane: slim(lane), cwd: this.repoPath(lane.Repo), dirty: lane.Dirty }, 420, 200);
    }
  }

  isMain(lane) {
    const r = this.knownRepos().find((x) => x.name === lane.Repo);
    return (r?.path && norm(r.path) === norm(lane.Path)) || (lane.Slot === 0 && lane.Managed);
  }

  say(text, error = false) {
    this.note = { text, error, at: Date.now() };
  }

  // Lanes and PRs in the order they are drawn: what ↑↓ walks through.
  items() {
    const lanes = (this.lanes ?? []).filter((l) => (l.Managed || this.showAll) && (!this.repo || l.Repo === this.repo));
    const has = new Set(lanes.map((l) => `${l.Repo}\u0000${l.Branch}`));
    const prs = Object.entries(this.prs)
      .filter(([repo]) => !this.repo || repo === this.repo)
      .flatMap(([repo, list]) => list.filter((p) => !(this.lanes ?? []).some((l) => l.Repo === repo && l.Branch === p.branch) && !has.has(`${repo}\u0000${p.branch}`)).map((pr) => ({ key: `pr:${repo}#${pr.number}`, repo, pr })));
    return [...lanes.map((lane) => ({ key: keyOf(lane), lane })), ...prs];
  }

  key(e, cx) {
    if (e.modifiers?.control || e.modifiers?.alt || e.modifiers?.platform) return;
    const list = this.items();
    const at = list.findIndex((i) => i.key === this.sel);
    const k = e.key;
    if (k === "down" || k === "j" || k === "up" || k === "k") {
      if (!list.length) return;
      const step = k === "down" || k === "j" ? 1 : -1;
      this.sel = list[Math.max(0, Math.min(list.length - 1, at < 0 ? 0 : at + step))].key;
    } else if (["enter", "g", "e", "o", "u", "d", "i", "h", "c", "p", "a", "x", "n", "b", "r", "t"].includes(k)) {
      this.act(cx, k);
    } else return;
    cx.stop_propagation();
    cx.notify();
  }

  // ---- the dashboard: the kitt TUI's, as a view of its own ---------------------

  // Shown when the layout's "view" stack shows this block (or always, outside the kitt layout).
  shown() {
    const view = findNode(layout(), "view");
    return !view || view.active === "kitt-dash";
  }

  // Back to the terminals: what going into a lane, or picking another space, wants.
  toTerminals() {
    const view = findNode(layout(), "view");
    if (view && view.active === "kitt-dash") run("layout.select", { stack: "view", child: "main" });
  }

  render(cx) {
    const t = theme();
    if (this.p.dialog) return this.renderDialog(t, cx);

    // Coming into view takes the keyboard; another space picked in the sidebar leaves for it.
    const shown = this.shown();
    const space = spaces().find((s) => s.active)?.id ?? null;
    // (Neither may happen during render: a task does them right after.)
    // The block is laid out only after the switch, so focus is tried until it holds.
    if (shown && !this.wasShown && !checking()) {
      for (const ms of [0, 60, 180, 400]) {
        cx.timer.after(ms, () => {
          if (this.shown() && !this.handle.is_focused()) this.handle.focus();
        });
      }
    }
    if (shown && this.wasShown && this.lastSpace !== undefined && space !== this.lastSpace && !this.busy.length) cx.spawn(async () => this.toTerminals());
    this.wasShown = shown;
    this.lastSpace = space;

    const mono = settings().font_family ?? "JetBrains Mono";
    const W = this.width;
    const sp = spaces();
    const ag = agents();
    const all = this.lanes ?? [];
    const lanes = all.filter((l) => l.Managed && (!this.repo || l.Repo === this.repo));
    const repos = this.knownRepos();
    const items = this.items();
    if (this.sel && !items.some((i) => i.key === this.sel)) this.sel = null;
    if (!this.sel && items.length) this.sel = items[0].key;

    const live = new Map(all.map((l) => {
      const s = spaceOf(l, sp);
      const mine = s ? ag.filter((a) => a.space === s.id) : [];
      const top = mine.reduce((m, a) => ((RANK[a.status] ?? 0) > (RANK[m] ?? 0) ? a.status : m), null);
      return [keyOf(l), { space: s, status: top ?? (l.Agent || null) }];
    }));
    const waiting = lanes.filter((l) => ["blocked", "done"].includes(live.get(keyOf(l))?.status)).length;
    const emu = lanes.find((l) => l.InEmulator)?.Name;
    const prItems = items.filter((i) => i.pr);

    // The header: the wordmark, and beside it what matters at a glance, one fact a line.
    const facts = v_flex().gap(2).text_size(13)
      .child(h_flex().gap(0)
        .child(div().font_semibold().text_color(t.foreground).child(this.lanes === null ? "reading lanes…" : `${lanes.length} ${lanes.length === 1 ? "lane" : "lanes"}`))
        .when(prItems.length > 0, (el) => el.child(div().text_color(t.dim).child(` · ${prItems.length} open PRs without one`))))
      .child(div().text_color(waiting ? t.blocked : t.dim).child(waiting ? `${waiting} waiting for you` : "nobody is waiting for you"))
      .child(h_flex().child(div().text_color(t.dim).child(emu ? "emulator → " : "emulator free")).when(Boolean(emu), (el) => el.child(div().text_color(t.ansi6).child(emu))));

    const tool = (id, icon, tip, onClick, spin = false) => div().id(id).p(6).rounded(6).text_color(t.dim).cursor_pointer()
      .hover((s) => s.bg(t.hover).text_color(t.foreground)).tooltip(tip)
      .on_click((_e, cx) => { cx.stop_propagation(); onClick(cx); cx.notify(); })
      .child(spin ? svg(ICON("spinner")).size(15).spin(900) : svg(ICON(icon)).size(15));

    const header = h_flex().flex_none().px(24).pt(20).pb(14).gap(22).items_end()
      .child(wordmark(t, W < 700 ? 5 : 7))
      .child(div().flex_1().min_w(0).child(facts))
      .child(h_flex().gap(2).items_center()
        .when(repos.length > 1, (el) => el.children([{ name: "", label: "all" }, ...repos.map((r) => ({ name: r.name, label: r.name }))].map((r) => div().id(`repo-${r.label}`)
          .px(8).h(24).flex().items_center().rounded(6).text_size(12).cursor_pointer()
          .bg(this.repo === r.name ? t.selected : t.background).text_color(this.repo === r.name ? t.foreground : t.dim)
          .hover((s) => s.bg(t.hover))
          .on_click((_e, cx) => { this.repo = r.name; try { localStorage.setItem("repo", r.name); } catch {} cx.notify(); })
          .child(r.label))))
        .child(tool("k-new", "plus", "New lane (n)", () => this.newLane()))
        .child(tool("k-backlog", "list", "Backlog (b)", () => this.backlog()))
        .child(tool("k-refresh", "refresh", "Refresh (r)", (cx) => this.refresh(cx, true), this.loading)));

    // Columns as the TUI draws them; the title takes what is left and goes first when narrow.
    const C = { ptr: 20, dot: 16, name: W < 760 ? 170 : 230, apps: 130, agent: 86, git: 74, pr: 104, proof: 104, marks: 36 };
    const showApps = W >= 880, showTitle = W >= 640;
    const cell = (w, el) => div().w(w).flex_none().min_w(0).truncate().child(el);

    const rows = [];
    let repo = null;
    const showRepo = new Set(items.filter((i) => i.lane).map((i) => i.lane.Repo)).size > 1 || items.some((i) => i.lane);
    for (const it of items.filter((i) => i.lane)) {
      if (showRepo && it.lane.Repo !== repo) {
        repo = it.lane.Repo;
        rows.push(div().px(4).pt(rows.length ? 14 : 2).pb(4).text_color(t.dim).child(repo));
      }
      rows.push(this.laneRow(t, it.lane, live.get(it.key), C, cell, showApps, showTitle));
    }
    const hidden = all.filter((l) => !l.Managed && (!this.repo || l.Repo === this.repo)).length;
    if (hidden && !this.showAll) {
      rows.push(div().id("k-hidden").px(4).pt(12).text_color(t.dim).cursor_pointer().hover((s) => s.text_color(t.foreground))
        .on_click((_e, cx) => this.act(cx, "t"))
        .child(`+ ${hidden} other worktrees · t shows them`));
    }
    if (prItems.length) {
      rows.push(div().px(4).pt(16).pb(4).text_color(t.dim).child("open pull requests · enter checks one out as a lane"));
      for (const it of prItems) rows.push(this.prRow(t, it, C, cell));
    }
    if (this.lanes !== null && !items.length && !this.error) {
      rows.push(div().px(4).pt(6).text_color(t.dim).child("No lanes yet. n makes one from an issue number or a name."));
    }

    // The line above the keys: what runs, else what came of the last call.
    const note = this.note && Date.now() - this.note.at < 30000 ? this.note : null;
    const status = h_flex().flex_none().h(22).px(24).gap(8).items_center().text_size(12.5)
      .when(this.busy.length > 0, (el) => el.child(svg(ICON("spinner")).size(12).flex_none().text_color(t.working).spin(900))
        .child(div().flex_1().min_w(0).truncate().text_color(t.working).child(`… ${this.busy.at(-1)}${this.busy.length > 1 ? ` (+${this.busy.length - 1})` : ""}`)))
      .when(!this.busy.length && Boolean(this.error), (el) => el.child(div().flex_1().min_w(0).truncate().text_color(t.blocked).child(this.error)))
      .when(!this.busy.length && !this.error && Boolean(note), (el) => el.child(div().flex_1().min_w(0).truncate().text_color(note.error ? t.blocked : t.muted).child(note.text)));

    // The keys, as the TUI's legend: each one also a button for the selected row.
    const KEYS = [["enter", "open lane"], ["e", "emulator"], ["h", "phone"], ["o", "browser"], ["g", "agent"], ["i", "install"], ["u", "up"], ["d", "down"], ["c", "check"],
      ["p", "proof"], ["n", "new"], ["b", "backlog"], ["a", "adopt"], ["x", "remove"], ["t", "all worktrees"], ["r", "refresh"]];
    const legend = v_flex().flex_none().px(24).pt(6).pb(14).gap(4).text_size(12.5)
      .child(h_flex().flex_wrap().gap_y(2).children(KEYS.map(([k, label], i) => h_flex().id(`key-${k}`).px(3).rounded(4).cursor_pointer()
        .hover((s) => s.bg(t.hover))
        .on_click((_e, cx) => { this.handle.focus(); this.act(cx, k); cx.notify(); })
        .when(i > 0, (el) => el.child(div().pr(3).text_color(t.dim).child("·")))
        .child(div().text_color(t.accent).child(k)).child(div().pl(5).text_color(t.dim).child(label)))))
      .child(h_flex().gap(0).text_color(t.dim)
        .child(div().text_color(t.ansi2).child("▲")).child(div().child(" dev servers up · "))
        .child(div().text_color(t.ansi6).child("▣")).child(div().child(" in the emulator · ")).child(div().child("◆ has a space in Herdr")));

    return v_flex()
      .id("kitt-root")
      .track_focus(this.handle)
      .on_key_down((e, cx) => this.key(e, cx))
      .on_size_change((size, cx) => { if (Math.abs(size.width - this.width) > 1) { this.width = size.width; cx.notify(); } })
      .size_full()
      .min_h(0)
      .bg(t.background)
      .text_color(t.foreground)
      .font_family(mono)
      .text_size(13)
      .child(header)
      .child(div().relative().flex_1().min_h(0)
        .child(v_flex().id("kitt-list").size_full().px(20).pb(8).overflow_y_scroll().children(rows))
        .child(Scrollbar.vertical("kitt-list").absolute().inset_0()))
      .child(status)
      .child(legend);
  }

  agentCell(t, status) {
    const word = { blocked: "needs you", done: "finished", working: "working", idle: "idle" }[status] ?? "–";
    const color = { blocked: t.blocked, done: t.ansi2, working: t.working }[status] ?? t.dim;
    return div().text_color(color).child(word);
  }

  dot(t, status) {
    if (status === "working") return svg(ICON("spinner")).size(11).text_color(t.working).spin(900);
    const glyph = { blocked: "●", done: "●", idle: "○" }[status] ?? "·";
    const color = { blocked: t.blocked, done: t.ansi2 }[status] ?? t.dim;
    return div().text_color(color).child(glyph);
  }

  laneRow(t, lane, live, C, cell, showApps, showTitle) {
    const key = keyOf(lane);
    const on = this.sel === key;
    const select = (e, cx) => { this.sel = key; this.handle.focus(); if (e.click_count >= 2) this.openLane(lane); cx.notify(); };
    const row = h_flex().id(`lane-${key}`).flex_none().h(26).px(4).items_center().rounded(5).cursor_pointer()
      .when(on, (el) => el.bg(t.selected)).when(!on, (el) => el.hover((s) => s.bg(t.hover)))
      .on_click(select)
      .child(div().w(C.ptr).flex_none().text_color(t.accent).child(on ? "▸" : ""));
    if (!lane.Managed) {
      return row.child(div().flex_1().min_w(0).truncate().text_color(t.dim).child(`· ${lane.Name}  ${lane.Branch}  not a lane`));
    }
    const status = live?.status ?? null;
    const title = lane.Issue && lane.Title ? `#${lane.Issue} ${lane.Title}` : lane.Title || lane.Branch;
    const git = [lane.Dirty > 0 && `±${lane.Dirty}`, lane.Behind > 0 && !lane.IsMain && `↓${lane.Behind}`].filter(Boolean).join(" ");
    const pr = lane.PR;
    const prText = !pr ? "" : pr.State === "MERGED" ? `#${pr.Number} merged` : pr.State === "CLOSED" ? `#${pr.Number} closed`
      : `#${pr.Number} ${{ pass: "✓", fail: "✗", running: "●" }[pr.Checks] ?? ""}`.trim();
    const prColor = !pr ? t.dim : pr.State === "MERGED" ? t.ansi5 : pr.State === "CLOSED" ? t.dim : pr.Checks === "fail" ? t.blocked : pr.Checks === "running" ? t.working : t.ansi2;
    const proof = lane.Install === "installing" ? { text: "installing…", color: t.working }
      : lane.Install === "missing" ? { text: "no install·i", color: t.blocked }
      : (() => { const p = proofText(lane); return p ? { text: p.text, color: p.color(t) } : { text: "", color: t.dim }; })();
    const servers = up(lane), total = own(lane).length;
    return row
      .child(h_flex().w(C.dot).flex_none().child(this.dot(t, status)))
      .child(cell(C.name, div().when(on, (el) => el.font_bold()).child(lane.Name)))
      .when(showTitle, (el) => el.child(div().flex_1().min_w(0).pr(10).truncate().text_color(t.dim).child(title)))
      .when(!showTitle, (el) => el.child(div().flex_1()))
      .when(showApps, (el) => el.child(cell(C.apps, div().text_color(t.ansi6).child((lane.Touched ?? []).join("·")))))
      .child(cell(C.agent, this.agentCell(t, status)))
      .child(cell(C.git, div().text_color(lane.Dirty > 0 ? t.ansi6 : t.working).child(git)))
      .child(cell(C.pr, div().text_color(prColor).child(prText)))
      .child(cell(C.proof, div().text_color(proof.color).child(proof.text)))
      .child(h_flex().w(C.marks).flex_none().gap(4)
        .child(div().text_color(servers.length && servers.length < total ? t.working : t.ansi2).child(servers.length ? "▲" : " "))
        .child(div().text_color(t.ansi6).child(lane.InEmulator ? "▣" : " "))
        .child(div().text_size(10).text_color(t.dim).child(live?.space ? "◆" : " ")));
  }

  prRow(t, it, C, cell) {
    const { pr } = it;
    const on = this.sel === it.key;
    const color = pr.checks === "fail" ? t.blocked : pr.checks === "running" ? t.working : pr.checks === "pass" ? t.ansi2 : t.dim;
    const mark = { pass: "✓", fail: "✗", running: "●" }[pr.checks] ?? "";
    return h_flex().id(`pr-${it.key}`).flex_none().h(26).px(4).items_center().rounded(5).cursor_pointer()
      .when(on, (el) => el.bg(t.selected)).when(!on, (el) => el.hover((s) => s.bg(t.hover)))
      .on_click((e, cx) => { this.sel = it.key; this.handle.focus(); if (e.click_count >= 2) this.act(cx, "enter"); cx.notify(); })
      .child(div().w(C.ptr).flex_none().text_color(t.accent).child(on ? "▸" : ""))
      .child(div().w(C.dot).flex_none().text_color(t.dim).child("◦"))
      .child(div().w(60).flex_none().when(on, (el) => el.font_bold()).child(`#${pr.number}`))
      .child(div().flex_1().min_w(0).pr(10).truncate().child(pr.title))
      .child(cell(260, div().text_color(t.dim).child(pr.branch ?? "")))
      .child(cell(C.pr, div().text_color(color).child(`#${pr.number} ${mark}`.trim())))
      .child(cell(50, div().text_color(t.dim).child(pr.draft ? "draft" : "")));
  }

  // ---- dialogs -----------------------------------------------------------------

  initDialog(cx) {
    const p = this.p;
    this.stage = p.dialog === "new" ? "input" : p.dialog === "backlog" ? "list" : "choose";
    this.options = p.options ?? [];
    this.choice = p.chosen ?? 0;
    this.text = p.value ?? "";
    this.input = InputState.new({ value: this.text, placeholder: p.dialog === "backlog" ? "filter: words, a label or a number" : "issue number (plus apps: 412 web) or a name" });
    this.input.on("change", (_e, cx) => { this.text = this.input.value(); this.at = 0; cx.notify(); });
    this.input.on("submit", (_e, cx) => { this.submit(cx); cx.notify(); });
    this.at = 0;
    if (p.dialog === "new" || p.dialog === "backlog") { this.input.focus(); cx.timer.after(150, () => this.input.focus()); }
    else this.handle.focus();
    if (p.dialog === "backlog" && !checking()) {
      this.issues = null;
      cx.spawn(async (cx) => {
        try {
          const r = await kitt(["backlog", "--repo", p.repo, "--json"], { cwd: p.cwd, timeout: 60000 });
          if (r.code !== 0) throw new Error(clean(r.stderr) || clean(r.stdout));
          this.issues = JSON.parse(r.stdout || "[]");
        } catch (e) { this.issues = []; this.failed = String(e?.message ?? e); }
        cx.notify();
        this.input.focus();
      });
    } else this.issues = [];
  }

  close() { closeOverlay(this.p.overlay); }

  send(req) {
    request(this.p.owner, { cwd: this.p.cwd, ...req });
    this.close();
  }

  // Enter in a dialog: the stage decides what it means.
  submit(cx) {
    const p = this.p;
    if (p.dialog === "backlog" && this.stage === "list") return this.pickIssue(this.shownIssues()[this.at]);
    if (this.stage === "input") {
      const input = this.text.trim();
      if (!input) return;
      const { list, chosen } = newOpenings(input, p.repo);
      this.name = input;
      this.options = list;
      this.choice = chosen;
      this.stage = "choose";
      this.handle.focus();
      return;
    }
    if (this.stage === "choose") {
      const o = this.options[this.choice];
      if (!o) return;
      if (o.select) { run("space.select", { id: o.select }); return this.close(); }
      if (o.ask) {
        this.stage = "prompt";
        this.input.set_value("");
        this.text = "";
        this.input.focus();
        return;
      }
      return this.send({ args: o.args, label: `${this.titleWord()} ${this.laneName()}` });
    }
    if (this.stage === "prompt") {
      const o = this.options[this.choice];
      const text = this.text.trim();
      if (!o || !text) return;
      return this.send({ args: [...o.args, "--prompt", text], label: `${this.titleWord()} ${this.laneName()}` });
    }
  }

  titleWord() { return this.p.dialog === "open" ? "opening" : "new lane"; }
  laneName() { return this.p.name ?? this.name ?? ""; }

  pickIssue(issue) {
    if (!issue) return;
    const lane = (this.p.lanes ?? []).find((l) => l.issue === issue.number);
    if (lane) {
      if (lane.space) run("space.select", { id: lane.space });
      else request(this.p.owner, { args: ["focus", lane.key, "--no-up"], label: `opening ${lane.name}`, cwd: this.p.cwd });
      return this.close();
    }
    const base = ["new", String(issue.number), "--repo", this.p.repo];
    this.name = `#${issue.number} ${issue.title}`;
    this.options = [
      { label: "a blank Claude", hint: "the lane and an agent to talk to, no dev servers started", args: [...base, "--blank", "--focus"] },
      { label: "Claude on the issue, through to a pull request", hint: "it reads the issue, builds it, proves it in the app and opens the PR; the dev servers start", args: [...base, "--up"] },
    ];
    this.choice = 1;
    this.stage = "choose";
    this.handle.focus();
  }

  shownIssues() {
    const words = this.text.toLowerCase().split(/\s+/).filter(Boolean);
    return (this.issues ?? []).filter((i) => {
      const hay = `#${i.number} ${i.title} ${(i.labels ?? []).join(" ")} ${i.author ?? ""} ${i.milestone ?? ""}`.toLowerCase();
      return words.every((w) => hay.includes(w));
    });
  }

  dialogKey(e, cx) {
    const p = this.p;
    const k = e.key;
    if (k === "escape") { this.close(); cx.stop_propagation(); return; }
    if (p.dialog === "remove") {
      if (k === "enter" || (!p.force && k === "y") || (p.force && e.key_char === "D")) this.remove();
      else return;
    } else if (p.dialog === "phone") {
      this.close();
    } else if (this.stage === "choose") {
      const n = this.options.length;
      if (k === "down" || k === "j") this.choice = Math.min(n - 1, this.choice + 1);
      else if (k === "up" || k === "k") this.choice = Math.max(0, this.choice - 1);
      else if (k === "enter") this.submit(cx);
      else if (/^[1-9]$/.test(k) && Number(k) <= n) { this.choice = Number(k) - 1; this.submit(cx); }
      else return;
    } else if (p.dialog === "backlog" && this.stage === "list") {
      const n = this.shownIssues().length;
      if (k === "down" && n) this.at = Math.min(n - 1, this.at + 1);
      else if (k === "up" && n) this.at = Math.max(0, this.at - 1);
      else if (k === "enter") this.submit(cx);
      else return;
    } else return;
    cx.stop_propagation();
    cx.notify();
  }

  remove() {
    const p = this.p;
    const args = ["rm", keyOf(p.lane), ...(p.force ? ["--force"] : [])];
    this.send({ args, label: `${p.force ? "deleting" : "removing"} ${p.lane.Name}`, kind: "rm", lane: p.lane, timeout: 300000 });
  }

  renderDialog(t) {
    const p = this.p;
    const button = (id, text, primary, danger, onClick) => h_flex().id(id).h(30).px(13).items_center().rounded(8).border(1)
      .border_color(primary ? (danger ? t.blocked : t.solid) : t.input).bg(primary ? (danger ? t.blocked : t.solid) : t.raised)
      .text_color(primary ? t.solid_foreground : t.foreground).text_size(12.5).font_medium().cursor_pointer()
      .hover((s) => s.opacity(0.88)).on_click((_e, cx) => { onClick(cx); cx.notify(); }).child(text);
    const field = () => Input.new(this.input).h(34).px(10).rounded(8).border(1).border_color(t.accent).bg(t.background).text_size(13).text_color(t.foreground);
    const shell = (title, sub) => v_flex().id("kitt-dialog").track_focus(this.handle).on_key_down((e, cx) => this.dialogKey(e, cx))
      .size_full().min_h(0).p(18).gap(10).bg(t.popover).text_size(13).text_color(t.foreground)
      .child(h_flex().items_center().gap(10)
        .child(wordmark(t, 2))
        .child(div().flex_1().min_w(0).truncate().text_size(14).font_semibold().child(title))
        .child(div().flex_none().text_size(11).text_color(t.dim).child("Esc")))
      .when(Boolean(sub), (el) => el.child(div().text_size(12).text_color(t.muted).child(sub)));

    if (p.dialog === "remove") {
      const lane = p.lane;
      return shell(p.force ? `delete ${lane.Name}?` : `remove ${lane.Name}?`, null)
        .child(div().flex_1().text_color(p.force ? t.blocked : t.muted).child(p.force
          ? `${p.reason}. Deleting removes the worktree and everything unsaved in it.`
          : `Removes the worktree and closes its space. The branch stays.${lane.Dirty ? ` kitt refuses while it holds ${lane.Dirty} uncommitted changes.` : ""}`))
        .child(h_flex().justify_end().gap(8)
          .child(button("rm-no", "Keep it", false, false, () => this.close()))
          .child(button("rm-yes", p.force ? "Delete anyway (D)" : "Remove (y)", true, true, () => this.remove())));
    }

    if (p.dialog === "phone") {
      const address = (() => { const at = p.link.indexOf("url="); try { return at >= 0 ? decodeURIComponent(p.link.slice(at + 4)) : p.link; } catch { return p.link; } })();
      return shell(`phone → ${p.lane?.Name ?? ""}`, "In the dev client on the phone, type (kitt phone prints a code to scan):")
        .child(div().p(10).rounded(8).bg(t.background).border(1).border_color(t.border).text_size(13).font_semibold().child(address))
        .children((p.notes ?? []).map((n, i) => div().id(`note-${i}`).text_size(12).text_color(t.working).child(`! ${n}`)))
        .child(div().flex_1())
        .child(div().text_size(11).text_color(t.dim).child("the phone must be on this machine's network · d on the lane stops it with its servers"));
    }

    if (this.stage === "input") {
      return shell(`new lane in ${p.repo}`, "An issue number (plus apps, like \"412 web\") or a name for a session without a ticket.")
        .child(field())
        .child(div().flex_1())
        .child(h_flex().justify_end().gap(8)
          .child(button("new-cancel", "Cancel", false, false, () => this.close()))
          .child(button("new-go", "Next", true, false, (cx) => this.submit(cx))));
    }

    if (this.stage === "prompt") {
      return shell(`${p.dialog === "open" ? "open" : "new lane"} ${this.laneName()}`, "What Claude is to do:")
        .child(field())
        .child(div().flex_1())
        .child(h_flex().justify_end().gap(8)
          .child(button("pr-cancel", "Cancel", false, false, () => this.close()))
          .child(button("pr-go", "Start", true, false, (cx) => this.submit(cx))));
    }

    if (p.dialog === "backlog" && this.stage === "list") {
      const shown = this.shownIssues();
      const lanes = new Map((p.lanes ?? []).map((l) => [l.issue, l]));
      const sel = shown[this.at];
      return shell(`backlog of ${p.repo}`, this.issues === null ? "reading issues…" : this.failed ? null : `${shown.length} of ${this.issues.length} open issues · ↵ makes a lane, or goes into the one it has`)
        .when(Boolean(this.failed), (el) => el.child(div().text_color(t.blocked).child(this.failed)))
        .child(field())
        .child(div().relative().flex_1().min_h(0)
          .child(v_flex().id("bl-list").size_full().overflow_y_scroll().children(shown.map((i, n) => {
            const on = n === this.at;
            const lane = lanes.get(i.number);
            return v_flex().id(`bl-${i.number}`).flex_none().px(9).py(5).rounded(7).cursor_pointer()
              .when(on, (el) => el.bg(t.selected)).when(!on, (el) => el.hover((s) => s.bg(t.hover)))
              .on_click((e, cx) => { this.at = n; if (e.click_count >= 2) this.pickIssue(i); cx.notify(); })
              .child(h_flex().gap(8).items_center()
                .child(div().flex_none().text_size(12).text_color(t.accent).child(`#${i.number}`))
                .child(div().flex_1().min_w(0).truncate().child(i.title))
                .when(Boolean(lane), (el) => el.child(div().flex_none().text_size(11).text_color(t.ansi2).child(`lane ${lane.name}`))))
              .when((i.labels ?? []).length > 0, (el) => el.child(div().pl(1).text_size(11).text_color(t.dim).truncate().child(i.labels.join(" · "))));
          })))
          .child(Scrollbar.vertical("bl-list").absolute().inset_0()))
        .when(Boolean(sel?.body), (el) => el.child(div().id("bl-body").flex_none().max_h(110).overflow_y_scroll().p(8).rounded(7).bg(t.background)
          .text_size(11.5).text_color(t.muted).child(sel.body.slice(0, 1200))))
        .child(div().text_size(11).text_color(t.dim).child("↑↓ choose · ↵ lane · esc close"));
    }

    // choose: how the lane opens.
    const title = p.dialog === "open" ? `open ${this.laneName()}` : `new lane ${this.laneName()}`;
    return shell(title, null)
      .child(v_flex().flex_1().min_h(0).gap(2).children(this.options.map((o, i) => {
        const on = i === this.choice;
        return v_flex().id(`opt-${i}`).flex_none().px(10).py(5).rounded(8).cursor_pointer()
          .when(on, (el) => el.bg(t.selected)).when(!on, (el) => el.hover((s) => s.bg(t.hover)))
          .on_click((_e, cx) => { this.choice = i; this.submit(cx); cx.notify(); })
          .child(h_flex().gap(8).child(div().flex_none().text_color(t.dim).child(String(i + 1))).child(div().flex_1().min_w(0).truncate().when(on, (el) => el.font_semibold()).child(o.label)))
          .child(div().pl(16).text_size(11.5).text_color(t.dim).child(o.hint));
      })))
      .child(div().text_size(11).text_color(t.dim).child(`↑↓ or 1-${this.options.length} · ↵ takes it · esc leaves it`));
  }
}

// What a dialog needs of a lane: props are stored as given, keep them small.
function slim(l) {
  return { Repo: l.Repo, Name: l.Name, Path: l.Path, Branch: l.Branch, Dirty: l.Dirty, Space: l.Space ?? "" };
}

function rollup(list) {
  if (!Array.isArray(list) || !list.length) return "";
  const bad = ["FAILURE", "ERROR", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED"];
  if (list.some((c) => bad.includes(c.conclusion) || bad.includes(c.state))) return "fail";
  if (list.some((c) => (c.status && c.status !== "COMPLETED") || c.state === "PENDING")) return "running";
  return "pass";
}

// The last proof, else the last checks: what the TUI's proof column says.
function proofText(l) {
  const p = l.Proof;
  if (p?.status === "pass") return { text: "proof ✓", short: "✓", color: (t) => t.ansi2 };
  if (p?.status === "fail") return { text: "proof ✗", short: "✗", color: (t) => t.blocked };
  if (p?.status === "running") return { text: "proving…", short: "…", color: (t) => t.working };
  const c = l.Checks;
  if (c) {
    const failed = Array.isArray(c.failed) && c.failed.length > 0;
    return failed ? { text: "checks ✗", short: "✗", color: (t) => t.blocked } : { text: "checks ✓", short: "✓", color: (t) => t.dim };
  }
  return null;
}
