package main

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	qrcode "github.com/skip2/go-qrcode"
)

// --- kitt phone --------------------------------------------------------------

// A phone on the Wi-Fi is not the emulator: nothing reverses ports into it, so
// the `localhost` the lane's Metro hands its app leads nowhere. The phone gets
// a Metro of its own, next to the lane's, whose app is told this machine's
// address instead. The lane's Metro and the emulator stay as they are.

// phoneView is what a phone needs to load a lane: the address of its Metro,
// the link that opens the dev client on it, and what may keep it from working.
type phoneView struct {
	URL   string
	Link  string
	Notes []string
}

// lanHost is this machine's address on the network it reaches the world
// through. Dialling UDP sends nothing; it only makes the system pick the route.
func lanHost() string {
	conn, err := net.Dial("udp", "192.0.2.1:9")
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP.IsLoopback() {
		return ""
	}
	return addr.IP.String()
}

// phonePort is the port of a lane's phone Metro: the first after its own Metro
// that no app of the repo has in this lane. A slot leaves ten ports to an app.
func phonePort(cfg RepoConfig, app App, lane Lane) int {
	taken := map[int]bool{}
	for _, other := range cfg.Apps {
		taken[other.port(lane.Slot)] = true
	}
	own := app.port(lane.Slot)
	for port := own + 1; port < own+portStride; port++ {
		if !taken[port] {
			return port
		}
	}
	return 0
}

var localURL = regexp.MustCompile(`://(localhost|127\.0\.0\.1)(:|/|$)`)

// phoneEnv is an app's environment as a phone needs it: every address on this
// machine named by the machine's address on the network. It also returns the
// ports those addresses lead to, by the variable that names them.
func phoneEnv(env map[string]string, host string) (map[string]string, map[string]int) {
	out, ports := map[string]string{}, map[string]int{}
	for key, value := range env {
		out[key] = localURL.ReplaceAllString(value, "://"+host+"$2")
		if out[key] == value {
			continue
		}
		if parsed, err := url.Parse(out[key]); err == nil {
			if port, err := strconv.Atoi(parsed.Port()); err == nil {
				ports[key] = port
			}
		}
	}
	return out, ports
}

// answers says whether something on this machine listens on a port for the
// network, not only for itself. A firewall in between is not seen from here.
func answers(host string, port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 400*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// phone starts a lane's phone Metro unless it runs already, and returns how a
// phone reaches it.
func phone(lane Lane, host string, printOnly bool) (phoneView, error) {
	if !lane.Managed {
		return phoneView{}, fail("%s is not a lane yet: kitt adopt %s", lane.Name, lane.Name)
	}
	cfg := loadRepoConfig(lane.Main, lane.Path)
	expo := cfg.expo()
	if expo == nil || expo.Dev == "" {
		return phoneView{}, fail("%s has no phone app with a `dev` command", lane.Repo)
	}
	if host == "" {
		if host = lanHost(); host == "" {
			return phoneView{}, fail("this machine has no address on a network: join the phone's Wi-Fi, or name the address with --host")
		}
	}
	port := phonePort(cfg, *expo, lane)
	if port == 0 {
		return phoneView{}, fail("no free port next to %s's %d", expo.Name, expo.port(lane.Slot))
	}

	// The same app on the phone's port: {port} in its dev line and its env follow.
	app := *expo
	app.Port += port - expo.port(lane.Slot)
	dir := appDir(app, lane)
	env, backends := phoneEnv(appEnv(cfg, app, lane, dir), host)
	// What Metro tells the dev client about itself, where the machine has several addresses.
	env["REACT_NATIVE_PACKAGER_HOSTNAME"] = host
	command := cfg.expand(app.Dev, app, lane)

	view := phoneView{URL: fmt.Sprintf("http://%s:%d", host, port)}
	view.Link = view.URL
	if app.Scheme != "" {
		view.Link = app.Scheme + "://expo-development-client/?url=" + url.QueryEscape(view.URL)
	}
	for _, key := range sortedKeys(env) {
		backend, ok := backends[key]
		if !ok || answers(host, backend) {
			continue
		}
		name := "it"
		for _, other := range cfg.Apps {
			if other.port(lane.Slot) == backend {
				name = "kitt up " + lane.Name + " " + other.Name
			}
		}
		if listening(backend) {
			view.Notes = append(view.Notes, fmt.Sprintf("%s: port %d answers this machine only, not %s", key, backend, host))
		} else {
			view.Notes = append(view.Notes, fmt.Sprintf("%s: nothing answers on %d (start %s)", key, backend, name))
		}
	}

	if listening(port) {
		// A Metro started for another network still hands its app the old address.
		if lane.State != nil && lane.State.Phone != "" && lane.State.Phone != host {
			return view, fail("the phone Metro of %s runs for %s, this machine is %s now: kitt down %s, then again", lane.Name, lane.State.Phone, host, lane.Name)
		}
		return view, nil
	}
	if printOnly || !hasHerdr() {
		view.Notes = append(view.Notes, fmt.Sprintf("start it: cd %s && %s%s", dir, envPrefix(env), command))
		return view, nil
	}
	if !ensureSetup(lane, cfg, *expo) {
		return view, fail("%s is not installed in %s: kitt setup %s", expo.Name, lane.Name, lane.Name)
	}
	workspace, _ := herdrOpen(lane, false)
	if workspace == "" {
		return view, fail("could not open %s in herdr", lane.Name)
	}
	pane, err := devPane(workspace, dir, env)
	if err != nil {
		return view, err
	}
	_ = herdr(nil, "pane", "rename", pane, fmt.Sprintf("%s phone :%d", app.Name, port))
	if err := herdr(nil, "pane", "run", pane, command); err != nil {
		return view, err
	}
	_ = updateState(func(s *State) {
		if entry := s.Lanes[lane.key()]; entry != nil {
			entry.Phone = host
		}
	})
	for waited := 0; waited < 90 && !listening(port); waited += 2 {
		time.Sleep(2 * time.Second)
	}
	if !listening(port) {
		view.Notes = append(view.Notes, fmt.Sprintf("Metro does not answer on %d yet: see the dev tab of %s", port, lane.Name))
	}
	return view, nil
}

// qr draws a text as a QR code, two rows of it to a line. The colours are set,
// not the terminal's: a camera wants dark on light.
func qr(text string) string {
	code, err := qrcode.New(text, qrcode.Low)
	if err != nil {
		return ""
	}
	code.DisableBorder = true
	bits := code.Bitmap()
	const quiet = 2
	size := len(bits) + 2*quiet
	dark := func(x, y int) bool {
		x, y = x-quiet, y-quiet
		return x >= 0 && y >= 0 && y < len(bits) && x < len(bits) && bits[y][x]
	}
	paper := lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("15"))
	// The blocks carry the code by themselves, where the output takes no colours.
	blocks := map[[2]bool]string{{false, false}: " ", {true, false}: "▀", {false, true}: "▄", {true, true}: "█"}

	var lines []string
	for y := 0; y < size; y += 2 {
		var line strings.Builder
		for x := 0; x < size; x++ {
			line.WriteString(blocks[[2]bool{dark(x, y), dark(x, y+1)}])
		}
		lines = append(lines, paper.Render(line.String()))
	}
	return strings.Join(lines, "\n")
}

func cmdPhone(args []string) error {
	rest, opts := flags(args, "print", "link")
	lane, err := findLane(strings.Join(rest, ""))
	if err != nil {
		return err
	}
	view, err := phone(lane, opts["host"], opts["print"] != "")
	if err != nil {
		return err
	}
	if opts["link"] == "" {
		fmt.Printf("phone → %s  %s\n\n%s\n\n", lane.Name, view.URL, qr(view.Link))
		fmt.Println("  Scan it with the phone's camera, or type the address into the dev client.")
		fmt.Printf("  The phone is on the same network as this machine; `kitt down %s` stops it.\n", lane.Name)
	}
	for _, note := range view.Notes {
		fmt.Println("  note    " + note)
	}
	// The last line: it is what the dashboard draws its code from.
	if opts["link"] != "" {
		fmt.Println(view.Link)
	}
	return nil
}
