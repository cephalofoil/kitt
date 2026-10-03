package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// run executes a program and returns its trimmed stdout. A non-zero exit is an
// error carrying the first line of stderr.
func run(dir string, name string, args ...string) (string, error) {
	return runTimeout(dir, 30*time.Second, name, args...)
}

func runTimeout(dir string, limit time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return strings.TrimSpace(out.String()), fmt.Errorf("%s %s: %s", name, firstWord(args), firstLine(msg))
	}

	return strings.TrimSpace(out.String()), nil
}

// runBytes is run for binary output (a screenshot).
func runBytes(limit time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()

	return exec.CommandContext(ctx, name, args...).Output()
}

// shell builds a command that runs one line through the platform's shell, the
// line passed through untouched so quotes and && mean what the author wrote.
func shell(dir string, line string) *exec.Cmd {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd")
		setRawCmdLine(cmd, `cmd /S /C "`+line+`"`)
	} else {
		cmd = exec.Command("sh", "-c", line)
	}
	cmd.Dir = dir

	return cmd
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

func firstWord(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// norm makes a path comparable: absolute, forward slashes, lower case on Windows.
func norm(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = filepath.ToSlash(filepath.Clean(path))
	path = strings.TrimPrefix(path, "//?/")
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

// within reports whether path is dir or lies beneath it.
func within(path, dir string) bool {
	p, d := norm(path), norm(dir)
	return p == d || strings.HasPrefix(p, d+"/")
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func listening(port int) bool {
	if port <= 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 250*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slug turns a title into a branch-safe name: lower case ASCII, dashes, capped.
func slug(s string, max int) string {
	s = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss", "Ä", "ae", "Ö", "oe", "Ü", "ue").Replace(s)
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > max {
		s = strings.Trim(s[:max], "-")
		if i := strings.LastIndex(s, "-"); i > max/2 {
			s = s[:i]
		}
	}
	return s
}

func readJSON(path string, into any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, into)
}

// writeJSON replaces the file in one step, so a reader never sees half of it.
func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func fail(format string, args ...any) error {
	return errors.New(fmt.Sprintf(format, args...))
}

// flags splits arguments into positionals and --name[=value] options. Names in
// `bools` take no value.
func flags(args []string, bools ...string) ([]string, map[string]string) {
	isBool := map[string]bool{}
	for _, b := range bools {
		isBool[b] = true
	}
	var rest []string
	opts := map[string]string{}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") || arg == "--" {
			rest = append(rest, arg)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		switch {
		case hasValue:
			opts[name] = value
		case isBool[name]:
			opts[name] = "true"
		case i+1 < len(args):
			i++
			opts[name] = args[i]
		default:
			opts[name] = "true"
		}
	}

	return rest, opts
}
