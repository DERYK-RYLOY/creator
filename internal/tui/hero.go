// hero.go — the full-width welcome hero: the CREATOR wordmark rendered as
// a large braille volumetric field, shown when the transcript is empty.
//
// Same physics as the corner widget (fronts, tide, caustics, comet,
// sparkles, bubble wake) but downscaled from the master mask to fit the
// terminal and sampled at the theme keep-threshold. Pure function of
// (animT, morph, maxCols): the only cache is the downscaled lattice,
// rebuilt when the terminal resizes.

package tui

import (
	"strings"
	"sync"
)

const (
	heroMaxCols = 112 // widest the hero will ever paint
	heroMinCols = 36
	heroKeep    = 0.24
)

type heroCache struct {
	dotW, dotH int
	mask       [][]bool
}

var (
	heroMu     sync.Mutex
	heroCached = heroCache{}
)

// heroDims picks the lattice size for a maximum cell width. Dots keep the
// master mask aspect (600x96); height is rounded to whole braille rows.
func heroDims(maxCols int) (dotW, dotH, cellW, cellH int) {
	if maxCols > heroMaxCols {
		maxCols = heroMaxCols
	}
	if maxCols < heroMinCols {
		maxCols = heroMinCols
	}
	cellW = maxCols
	dotW = cellW * 2
	dotH = dotW * srcMaskH / srcMaskW
	// snap dotH to whole braille cells (multiple of 4)
	dotH = (dotH / 4) * 4
	if dotH < 8 {
		dotH = 8
	}
	cellH = dotH / 4
	return dotW, dotH, cellW, cellH
}

// heroMask downscales the master mask into a dotW x dotH boolean lattice,
// box-sampled against heroKeep. Cached per size.
func heroMask(dotW, dotH int) [][]bool {
	heroMu.Lock()
	defer heroMu.Unlock()
	if heroCached.dotW == dotW && heroCached.dotH == dotH && len(heroCached.mask) == dotH {
		return heroCached.mask
	}
	if !decodeWordmark() {
		grid := make([][]bool, dotH)
		for y := range grid {
			grid[y] = make([]bool, dotW)
		}
		return grid
	}
	out := make([][]bool, dotH)
	for dy := 0; dy < dotH; dy++ {
		row := make([]bool, dotW)
		sy0 := dy * srcMaskH / dotH
		sy1 := (dy + 1) * srcMaskH / dotH
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for dx := 0; dx < dotW; dx++ {
			sx0 := dx * srcMaskW / dotW
			sx1 := (dx + 1) * srcMaskW / dotW
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var sum float64
			for y := sy0; y < sy1; y++ {
				for x := sx0; x < sx1; x++ {
					if srcMask[y][x] {
						sum++
					}
				}
			}
			if sum/float64((sy1-sy0)*(sx1-sx0)) > heroKeep {
				// staggered decimation — the hero reads as a lattice of
				// points in water instead of a filled decal
				if (dx+dy)%2 == 0 {
					row[dx] = true
				}
			}
		}
		out[dy] = row
	}
	heroCached = heroCache{dotW: dotW, dotH: dotH, mask: out}
	return out
}

// renderWordmarkHero renders the welcome hero. maxCols caps the width in
// terminal cells (the caller centers the result itself).
func renderWordmarkHero(t, morph float64, maxCols int) string {
	if !decodeWordmark() {
		return ""
	}
	dotW, dotH, cellW, cellH := heroDims(maxCols)
	mask := heroMask(dotW, dotH)
	grid, cellQ := newDotGrid(dotH, dotW)
	for y := 0; y < dotH; y++ {
		for x := 0; x < dotW; x++ {
			if !mask[y][x] {
				continue
			}
			xOff, yOff, bright := dotState(x, y, dotW, dotH, t, morph)
			ix := clampInt(int(float64(x)+xOff+0.5), 0, dotW-1)
			iy := clampInt(int(float64(y)+yOff+0.5), 0, dotH-1)
			if grid[iy][ix] < bright {
				grid[iy][ix] = bright
				cellQ[iy][ix] = bright
			}
		}
	}
	if morph > 0.5 {
		placeBubbles2(grid, cellQ, t, morph, dotW, dotH)
	}
	return encodeDotField(grid, cellQ, dotW, dotH, cellW, cellH)
}

// centerBlock centers every line of a pre-rendered block to width w.
func centerBlock(block string, w int) []string {
	lines := strings.Split(block, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		lw := cellWidth(ln)
		pad := (w - lw) / 2
		if pad < 0 {
			pad = 0
		}
		out = append(out, strings.Repeat(" ", pad)+ln)
	}
	return out
}
