package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The logo, as pixels: "ki" in white, "tt" and the tube in the dashboard's own
// blue. It takes four terminal lines.
//
// The wordmark is drawn with half blocks: two pixel rows per line, eight in all.
// The tube needs more than that to keep its shape (cap, body, the crimped edge
// at its lower left), so it is drawn with sextants: two by three pixels per
// cell, twelve rows in the same four lines.
var (
	logoWord = []string{
		"ww.......ww....bb.....bb..",
		"ww.......ww....bb.....bb..",
		"ww...........bbbbbb.bbbbbb",
		"ww..www..ww..bbbbbb.bbbbbb",
		"ww.www...ww....bb.....bb..",
		"wwwww....ww....bb.....bb..",
		"ww.www...ww....bbbb...bbbb",
		"ww..www.wwww....bbb....bbb",
	}
	logoTube = []string{
		"..................",
		"............bb....",
		"...........bbbb...",
		".........bb.bbbb..",
		".......bbbbb.bb...",
		"......bbbbbbb.....",
		".....bbbbbbbb.....",
		"..b.bbbbbbbb......",
		".bbb..bbbb........",
		"..bbb..bb.........",
		"....bbb...........",
		".....bb...........",
	}
	// The narrow wordmark: six pixel rows in quadrants, each pixel half a cell
	// wide, so the strokes stay thick in half the width. No cell holds both colours.
	logoWordNarrow = []string{
		"www.......www....bbb......bbb...",
		"www.......www....bbb......bbb...",
		"www..www.......bbbbbbbb.bbbbbbbb",
		"wwwwww....www....bbb......bbb...",
		"wwwwww....www....bbb......bbb...",
		"www..www.wwwww...bbbbbb...bbbbbb",
	}
	// The tube as a mark of its own, about the size of Claude Code's figure: nine
	// cells by three lines, in sextants. Cap, gap, body, gap, the crimped end.
	logoTubeMark = []string{
		"............bbb...",
		"...........bbbbb..",
		".........bb.bbbb..",
		".......bbbbb.bb...",
		".....bbbbbbbb.....",
		"..b.bbbbbbbb......",
		".bbb.bbbbbb.......",
		"..bbb.bbb.........",
		"...bbb............",
	}
	// The same with a bead of putty leaving the cap.
	logoTubeBead = []string{
		"..........bbbb....",
		".........bbbbbb.ww",
		".......bb.bbbbb.ww",
		".....bbbbb.bbb..w.",
		"...bbbbbbbb.......",
		"b.bbbbbbbbb.......",
		"bb.bbbbbbb........",
		".bb.bbbbb.........",
		"..bb.bb...........",
	}
	// The compact lockup, three lines. Its wordmark is six pixels high in half
	// blocks: square pixels that tile without seams. Only the tube, which needs
	// the finer grid to stay a tube, is in sextants (nine pixel rows).
	logoWordSmall = []string{
		"ww......ww....bb.....bb..",
		"ww............bb.....bb..",
		"ww..ww..ww..bbbbbb.bbbbbb",
		"ww.ww...ww....bb.....bb..",
		"wwww....ww....bbbb...bbbb",
		"ww.www.wwww....bbb....bbb",
	}
	logoTubeSmall = []string{
		"..............",
		".........bb...",
		".......b.bbb..",
		".....bbbb.b...",
		"....bbbbbb....",
		"...bbbbbb.....",
		".bb.bbbb......",
		"..bbb.........",
		"....b.........",
	}
	// The tube for terminals whose font has no sextants: half blocks, coarser.
	logoTubeCoarse = []string{
		".........bbb",
		"......bb.bbb",
		"....bbbbb.b.",
		"...bbbbbb...",
		".b.bbbbb....",
		"bbb.bbb.....",
		".bbb.b......",
		"..bb........",
	}
	logoWhite = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	logoBlue  = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
)

func logoStyle(pixel byte) (lipgloss.Style, bool) {
	switch pixel {
	case 'w':
		return logoWhite, true
	case 'b':
		return logoBlue, true
	}
	return lipgloss.Style{}, false
}

// halfBlocks draws rows of pixels two rows per line. An empty pixel stays the
// terminal's own background.
func halfBlocks(rows []string) []string {
	var lines []string
	for y := 0; y < len(rows); y += 2 {
		var line strings.Builder
		for x := 0; x < len(rows[y]); x++ {
			upper, hasUpper := logoStyle(rows[y][x])
			lower, hasLower := logoStyle(rows[y+1][x])
			switch {
			case !hasUpper && !hasLower:
				line.WriteString(" ")
			case hasUpper && !hasLower:
				line.WriteString(upper.Render("▀"))
			case !hasUpper && hasLower:
				line.WriteString(lower.Render("▄"))
			case rows[y][x] == rows[y+1][x]:
				line.WriteString(upper.Render("█"))
			default:
				line.WriteString(upper.Background(lower.GetForeground()).Render("▀"))
			}
		}
		lines = append(lines, line.String())
	}
	return lines
}

// sextants draws rows of pixels three rows per line and two pixels per cell,
// with the block sextants of Unicode's legacy computing symbols. A cell has one
// colour: that of its first pixel.
func sextants(rows []string) []string {
	var lines []string
	for y := 0; y+2 < len(rows); y += 3 {
		var line strings.Builder
		for x := 0; x+1 < len(rows[y]); x += 2 {
			bits := 0
			style, styled := lipgloss.Style{}, false
			for i, pixel := range []byte{rows[y][x], rows[y][x+1], rows[y+1][x], rows[y+1][x+1], rows[y+2][x], rows[y+2][x+1]} {
				if found, ok := logoStyle(pixel); ok {
					bits |= 1 << i
					if !styled {
						style, styled = found, true
					}
				}
			}
			line.WriteString(style.Render(sextant(bits)))
		}
		lines = append(lines, line.String())
	}
	return lines
}

// quadrants draws rows of pixels two rows per line and two pixels per cell,
// with the quadrant block elements, which terminals draw without seams.
func quadrants(rows []string) []string {
	glyphs := []rune(" ▘▝▀▖▌▞▛▗▚▐▜▄▙▟█")
	var lines []string
	for y := 0; y+1 < len(rows); y += 2 {
		var line strings.Builder
		for x := 0; x+1 < len(rows[y]); x += 2 {
			bits := 0
			style, styled := lipgloss.Style{}, false
			for i, pixel := range []byte{rows[y][x], rows[y][x+1], rows[y+1][x], rows[y+1][x+1]} {
				if found, ok := logoStyle(pixel); ok {
					bits |= 1 << i
					if !styled {
						style, styled = found, true
					}
				}
			}
			line.WriteString(style.Render(string(glyphs[bits])))
		}
		lines = append(lines, line.String())
	}
	return lines
}

// sextant is the character for six pixels: bit 0 top left, 1 top right, 2 and
// 3 the middle, 4 and 5 the bottom. Four of the 64 patterns are older block
// elements and are not repeated in the sextant range.
func sextant(bits int) string {
	switch bits {
	case 0:
		return " "
	case 21:
		return "▌"
	case 42:
		return "▐"
	case 63:
		return "█"
	}
	offset := bits - 1
	if bits > 21 {
		offset--
	}
	if bits > 42 {
		offset--
	}
	return string(rune(0x1FB00 + offset))
}

// beside sets two drawings next to each other on a common baseline: the
// shorter one starts as many lines lower as it lacks.
func beside(left, right []string, gap int) []string {
	out := make([]string, len(left))
	skip := len(left) - len(right)
	for i := range left {
		out[i] = left[i]
		if i >= skip {
			out[i] += strings.Repeat(" ", gap) + right[i-skip]
		}
	}
	return out
}

// logoMode is what KITT_LOGO asks for; empty is the default, the narrow lockup.
func logoMode() string { return os.Getenv("KITT_LOGO") }

// logoNamesItself says whether the mark in use spells "kitt": where it does
// not, the dashboard writes the name beside it.
func logoNamesItself(width int) bool {
	mode := logoMode()
	return width >= 40 && mode != "text" && mode != "tube" && mode != "bead"
}

// logo is the dashboard's header mark for a given width. The default is the
// wordmark alone, four lines, the summary beside it. KITT_LOGO picks another:
// lockup (the large tube with the narrow wordmark), small (all in three lines), tube or bead (the tube
// alone), medium (three lines, wider letters), large (four lines), blocks (large, the tube in half blocks
// for a font without sextants), word (no tube), text (no logo).
func logo(width int) []string {
	mode := logoMode()
	switch {
	case mode == "text" || width < 40:
		return nil
	case mode == "lockup":
		return beside(sextants(logoTube), quadrants(logoWordNarrow), 2)
	case mode == "small":
		return beside(sextants(logoTubeMark), quadrants(logoWordNarrow), 2)
	case mode == "tube":
		return sextants(logoTubeMark)
	case mode == "bead":
		return sextants(logoTubeBead)
	case mode == "word":
		return halfBlocks(logoWord)
	case mode == "medium":
		return beside(sextants(logoTubeSmall), halfBlocks(logoWordSmall), 2)
	case mode == "blocks" && width >= 66:
		return beside(halfBlocks(logoTubeCoarse), halfBlocks(logoWord), 2)
	case mode == "large" && width >= 66:
		return beside(sextants(logoTube), halfBlocks(logoWord), 2)
	}
	return halfBlocks(logoWord)
}

// cmdLogo prints the variants, to look at them outside the dashboard.
func cmdLogo([]string) error {
	show := func(title string, lines []string) {
		fmt.Printf("\n  %s\n\n", title)
		for _, line := range lines {
			fmt.Println("  " + line)
		}
	}
	show("the default: the wordmark alone", halfBlocks(logoWord))
	show("the large tube with the narrow wordmark: KITT_LOGO=lockup", beside(sextants(logoTube), quadrants(logoWordNarrow), 2))
	show("all in three lines, with the smaller tube: KITT_LOGO=small", beside(sextants(logoTubeMark), quadrants(logoWordNarrow), 2))
	show("tube alone: KITT_LOGO=tube", sextants(logoTubeMark))
	show("tube with a bead of putty: KITT_LOGO=bead", sextants(logoTubeBead))
	show("medium, three lines, wider letters: KITT_LOGO=medium", beside(sextants(logoTubeSmall), halfBlocks(logoWordSmall), 2))
	show("large, four lines: KITT_LOGO=large", beside(sextants(logoTube), halfBlocks(logoWord), 2))
	show("large with a coarser tube, if the tubes above show as boxes: KITT_LOGO=blocks", beside(halfBlocks(logoTubeCoarse), halfBlocks(logoWord), 2))
	fmt.Printf("\n  no logo: KITT_LOGO=text  %s\n\n", accent.Render("kitt"))
	return nil
}
