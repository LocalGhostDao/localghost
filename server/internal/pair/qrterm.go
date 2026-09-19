package pair

import "strings"

// Matrix is a square QR grid. Dark(x, y) is true for a dark module. This is the seam: any QR
// encoder that can report dark modules plugs in here. The renderer below is independent of how the
// matrix was produced.
type Matrix interface {
	Size() int
	Dark(x, y int) bool
}

// RenderTerminal draws the QR using Unicode half-block characters, two modules per character cell
// so it is not stretched 2:1. Colours are set explicitly via ANSI (black on white) so it scans
// regardless of the terminal theme, and a 4-module quiet zone is added, which scanners require.
//
// Per cell pair (top, bottom):
//
//	dark,dark  -> full block, dark,light -> upper half, light,dark -> lower half, light,light -> space
func RenderTerminal(m Matrix) string {
	const (
		reset = "\x1b[0m"
		// white background, black foreground: theme-independent, dark modules read as black.
		colour = "\x1b[47;30m"
		full   = "\u2588" // both halves dark
		upper  = "\u2580" // top dark
		lower  = "\u2584" // bottom dark
		blank  = " "      // both light
	)
	const quiet = 4
	n := m.Size()

	dark := func(x, y int) bool {
		// Outside the matrix is the quiet zone: always light.
		if x < 0 || y < 0 || x >= n || y >= n {
			return false
		}
		return m.Dark(x, y)
	}

	var b strings.Builder
	// Step two rows at a time over the matrix plus quiet zone on every side.
	for y := -quiet; y < n+quiet; y += 2 {
		b.WriteString(colour)
		for x := -quiet; x < n+quiet; x++ {
			top := dark(x, y)
			bottom := dark(x, y+1)
			switch {
			case top && bottom:
				b.WriteString(full)
			case top && !bottom:
				b.WriteString(upper)
			case !top && bottom:
				b.WriteString(lower)
			default:
				b.WriteString(blank)
			}
		}
		b.WriteString(reset)
		b.WriteString("\n")
	}
	return b.String()
}

// RenderTerminalCells draws the QR with one full character cell pair per module , two spaces
// on a black or white BACKGROUND, no glyphs at all. Half-block rendering depends on the font's
// block glyphs filling their cell exactly and on the terminal leaving no gap between lines; many
// do neither, which puts a light hairline through every second module row and is the likeliest
// reason dense frames only scanned from a distance. Background colour paints the whole cell.
// Twice the rows of the half-block form, so the caller uses it only where it fits (cellsFit).
func RenderTerminalCells(m Matrix) string {
	const (
		reset = "\x1b[0m"
		dark  = "\x1b[40m  "
		light = "\x1b[47m  "
	)
	const quiet = 4
	n := m.Size()
	var b strings.Builder
	for y := -quiet; y < n+quiet; y++ {
		for x := -quiet; x < n+quiet; x++ {
			if x >= 0 && y >= 0 && x < n && y < n && m.Dark(x, y) {
				b.WriteString(dark)
			} else {
				b.WriteString(light)
			}
		}
		b.WriteString(reset)
		b.WriteString("\n")
	}
	return b.String()
}
