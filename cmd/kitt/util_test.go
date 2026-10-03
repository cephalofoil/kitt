package main

import (
	"os"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := run(dir, "git", args...); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

// A proof made on uncommitted work must still stand once exactly that work is
// committed, and must fall when the work changes.
func TestTreeOfSurvivesTheCommit(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "t@example.com")
	git(t, dir, "config", "user.name", "t")
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "one\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "first")
	clean := treeOf(dir)

	write("a.txt", "two\n")
	write("b.txt", "new\n")
	dirty := treeOf(dir)
	if dirty == "" || dirty == clean {
		t.Fatalf("uncommitted work must change the tree: clean %q, dirty %q", clean, dirty)
	}
	if status, _ := run(dir, "git", "status", "--porcelain"); status == "" {
		t.Fatal("reading the tree must not stage or commit anything")
	}

	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "second")
	if committed := treeOf(dir); committed != dirty {
		t.Fatalf("committing the same content must keep the tree: before %q, after %q", dirty, committed)
	}

	write("a.txt", "three\n")
	if treeOf(dir) == dirty {
		t.Fatal("a later change must change the tree")
	}
}

func TestPortsOfLanesNeverMeet(t *testing.T) {
	apps := []App{{Name: "mobile", Port: 8081}, {Name: "api", Port: 8000}, {Name: "admin", Port: 3003}, {Name: "web", Port: 3001}}
	seen := map[int]string{}
	for slot := 0; slot < 8; slot++ {
		for _, app := range apps {
			port := app.port(slot)
			if other, taken := seen[port]; taken {
				t.Fatalf("port %d is given to both %s and %s (slot %d)", port, other, app.Name, slot)
			}
			seen[port] = app.Name
		}
	}
}

func TestSlug(t *testing.T) {
	if got := slug("Rehauling Mealkit UI: Größe & Farben!", 32); got != "rehauling-mealkit-ui-groesse" {
		t.Fatalf("slug: %q", got)
	}
}
