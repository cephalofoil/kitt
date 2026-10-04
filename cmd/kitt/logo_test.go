package main

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// A drawing is only sound when its rows are one width, its height fits its
// characters, and no cell of a quadrant or sextant drawing needs two colours.
func TestLogoDrawingsAreWellFormed(t *testing.T) {
	type drawing struct {
		name       string
		rows       []string
		cellWidth  int
		cellHeight int
		oneColour  bool
	}
	for _, d := range []drawing{
		{"logoWord", logoWord, 1, 2, false},
		{"logoWordSmall", logoWordSmall, 1, 2, false},
		{"logoTubeCoarse", logoTubeCoarse, 1, 2, false},
		{"logoWordNarrow", logoWordNarrow, 2, 2, true},
		{"logoTube", logoTube, 2, 3, true},
		{"logoTubeSmall", logoTubeSmall, 2, 3, true},
		{"logoTubeMark", logoTubeMark, 2, 3, true},
		{"logoTubeBead", logoTubeBead, 2, 3, true},
	} {
		if len(d.rows)%d.cellHeight != 0 {
			t.Errorf("%s: %d rows do not fill lines of %d", d.name, len(d.rows), d.cellHeight)
		}
		for y, row := range d.rows {
			if len(row) != len(d.rows[0]) || len(row)%d.cellWidth != 0 {
				t.Errorf("%s: row %d is %d wide, the first %d", d.name, y, len(row), len(d.rows[0]))
			}
		}
		if !d.oneColour {
			continue
		}
		for y := 0; y+d.cellHeight <= len(d.rows); y += d.cellHeight {
			for x := 0; x+d.cellWidth <= len(d.rows[y]); x += d.cellWidth {
				seen := map[byte]bool{}
				for dy := 0; dy < d.cellHeight; dy++ {
					for dx := 0; dx < d.cellWidth; dx++ {
						if pixel := d.rows[y+dy][x+dx]; pixel != '.' {
							seen[pixel] = true
						}
					}
				}
				if len(seen) > 1 {
					t.Errorf("%s: the cell at row %d, column %d holds two colours", d.name, y, x)
				}
			}
		}
	}
}

func TestSextantCharacters(t *testing.T) {
	for bits, want := range map[int]string{0: " ", 1: "\U0001FB00", 20: "\U0001FB13", 21: "▌", 22: "\U0001FB14", 42: "▐", 43: "\U0001FB28", 62: "\U0001FB3B", 63: "█"} {
		if got := sextant(bits); got != want {
			t.Errorf("sextant(%d) = %q, want %q", bits, got, want)
		}
	}
}

func TestLogoFollowsWidthAndMode(t *testing.T) {
	t.Setenv("KITT_LOGO", "")
	if logo(30) != nil {
		t.Error("a narrow dashboard gets no logo")
	}
	if lines := logo(120); len(lines) != 4 || lipgloss.Width(lines[0]) != len(logoWord[0]) {
		t.Errorf("the default is the four-line wordmark, got %d lines", len(lines))
	}
	if !logoNamesItself(120) {
		t.Error("the wordmark spells the name")
	}
	t.Setenv("KITT_LOGO", "tube")
	if lines := logo(120); len(lines) != 3 || logoNamesItself(120) {
		t.Error("the tube alone is three lines and does not spell the name")
	}
	t.Setenv("KITT_LOGO", "text")
	if logo(120) != nil {
		t.Error("KITT_LOGO=text turns the logo off")
	}
}
