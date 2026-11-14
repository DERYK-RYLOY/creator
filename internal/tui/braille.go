// braille.go — the floating braille core: corner widget + welcome hero.
//
// The widget renders the CREATOR wordmark from a bitmask baked into the
// binary (see wordmark_data.go) — no files, no network, no goroutines.
// View() stays a pure function of (animT, morph).
//
// The motion model, refined from the original surge design:
//
//   - Depth planes. Every lattice dot carries a smooth pseudo-depth from a
//     layered sine field. Deep planes drift on a phase-shifted clock, puff
//     less under the energy front and render dimmer, so the whole mark reads
//     as a volumetric object suspended in water instead of a flat decal.
//   - Natural tide. Idle drift is a smooth vector field (not per-dot white
//     noise), so the letters sway like something buoyant, and the brightness
//     breathes outward from the core as a slow radial tide.
//   - Caustics. Two interfering filaments of light (pow-sharpened product of
//     orthogonal sine waves) slide across the mark — the dancing bands you
//     see at the bottom of a pool. Present even at rest; flood when thinking.
//   - Energy fronts (heartbeat). Expanding rings still sweep the word every
//     3 s (k=120, damp=10.5, radial + 10% swirl kick, gaussian window) — but
//     now depth scales the displacement and the leading edge rides a hotter
//     color ramp, so fronts move *through* the volume, not over it.
//   - Comet. Once per heartbeat a narrow gaussian glare travels the mark
//     left→right (alternating direction), white-heating the dots it crosses.
//   - Sparkles. Rare lattice points flare and decay exponentially while the
//     agent thinks — like glitter catching a passing beam.
//   - Bubbles. Rising water dots with a two-cell fading wake trail.
//
// Presence NEVER changes: mask dots keep their letterforms and every effect
// speaks through brightness and micro-displacement only. The word stays
// readable at every frame, exactly like the design it refines.
//
// Never-breaks guarantees:
//   - fixed-size arrays, bounded loops (rings: 2, bubbles: 4), no goroutines,
//     no I/O after decode (once, fail-safe)
//   - decode failure / tiny terminal -> empty widget, caller falls back
//   - all intensity and displacement values clamped before use
//   - sparkles/comet use closed-form envelopes of (t mod period) — no RNG
//     state, no per-frame allocation in the hot path beyond the builder
package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

const (
	widgetDotW  = 88  // dots across (Lite-half: ~443 dots visible)
	widgetDotH  = 16  // dots tall
	widgetCols  = 44  // braille cells across (2 dot-cols per cell)
	widgetRows  = 4   // braille cells tall (4 dot-rows per cell)
	srcMaskW    = 600 // source mask width in dots
	srcMaskH    = 96  // source mask height in dots
	srcRowBytes = srcMaskW / 8

	// box-sample coverage required to keep a dot — stroke structure must
	// survive the downscale; 0.34 keeps the letters full without smearing
	widgetKeep = 0.34
)

// ---------- wave physics ----------

const (
	heartbeat = 3.0  // s between energy fronts
	ringSpeed = 38.0 // widget dots/s (scaled from HTML 1280 px/s on 600 dots)
	ringSig   = 6.0  // gaussian window width in dots
	ringDecay = 0.52 // front amp *= exp(-dt*0.52), verbatim
	kickAmp   = 2.2  // max outward puff in dots at window peak
	springD   = 5.25 // damp/2 — spring envelope decay
	springW   = 9.61 // sqrt(k - (damp/2)^2) — damped natural frequency
)

// ---------- source mask decode (once, fail-safe) ----------

var (
	srcMask   [srcMaskH][srcMaskW]bool
	srcMaskOK bool
	srcOnce   sync.Once
)

func decodeWordmark() bool {
	srcOnce.Do(func() {
		srcMaskOK = decodeWordmarkBits(wordmarkBits)
	})
	return srcMaskOK
}

func decodeWordmarkBits(raw string) bool {
	var got [srcMaskH][srcMaskW]bool
	rows := 0
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if rows >= srcMaskH || len(line) != srcRowBytes*2 {
			return false
		}
		for bx := 0; bx < srcRowBytes; bx++ {
			v, err := strconv.ParseUint(line[bx*2:bx*2+2], 16, 8)
			if err != nil {
				return false
			}
			for b := 0; b < 8; b++ {
				if v&(1<<uint(7-b)) != 0 {
					got[rows][bx*8+b] = true
				}
			}
		}
		rows++
	}
	if rows != srcMaskH {
		return false
	}
	srcMask = got
	return true
}

// ---------- Lite-half dot lattice at widget scale ----------

var (
	scaledMask [widgetDotH][widgetDotW]bool
	scaledOnce sync.Once
)

func widgetMask() [widgetDotH][widgetDotW]bool {
	scaledOnce.Do(func() {
		for dy := 0; dy < widgetDotH; dy++ {
			sy0 := dy * srcMaskH / widgetDotH
			sy1 := (dy + 1) * srcMaskH / widgetDotH
			if sy1 <= sy0 {
				sy1 = sy0 + 1
			}
			for dx := 0; dx < widgetDotW; dx++ {
				sx0 := dx * srcMaskW / widgetDotW
				sx1 := (dx + 1) * srcMaskW / widgetDotW
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
				if sum/float64((sy1-sy0)*(sx1-sx0)) <= widgetKeep {
					continue
				}
				scaledMask[dy][dx] = true
			}
		}
	})
	return scaledMask
}

// ---------- color ramp from the active theme ----------
// muted -> thinking -> fg -> accent1, with a molten near-white tip for the
// comet glare. Keyed on the theme slug; rebuilt on theme switch.

const rampLevels = 64

var (
	rampCache     [rampLevels]lipgloss.Style
	rampCacheSlug string
	rampCacheMu   sync.Mutex
)

func parseHex(s string, fallback [3]uint8) [3]uint8 {
	s = strings.TrimSpace(strings.TrimPrefix(s, "#"))
	if len(s) != 6 {
		return fallback
	}
	var v uint64
	for _, c := range s {
		v <<= 4
		var n uint64
		switch {
		case c >= '0' && c <= '9':
			n = uint64(c - '0')
		case c >= 'a' && c <= 'f':
			n = uint64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			n = uint64(c-'A') + 10
		default:
			return fallback
		}
		v |= n
	}
	return [3]uint8{uint8(v >> 16), uint8(v >> 8), uint8(v)}
}

func lerpRGB(a, b [3]uint8, t float64) [3]uint8 {
	if t <= 0 {
		return a
	}
	if t >= 1 {
		return b
	}
	return [3]uint8{
		uint8(float64(a[0]) + (float64(b[0])-float64(a[0]))*t + 0.5),
		uint8(float64(a[1]) + (float64(b[1])-float64(a[1]))*t + 0.5),
		uint8(float64(a[2]) + (float64(b[2])-float64(a[2]))*t + 0.5),
	}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func hexStr(c [3]uint8) string { return fmt.Sprintf("#%02x%02x%02x", c[0], c[1], c[2]) }

func rampColor(q float64) lipgloss.Color {
	if q <= 0 {
		return lipgloss.Color(currentTheme.muted)
	}
	m := parseHex(currentTheme.muted, [3]uint8{80, 80, 90})
	th := parseHex(currentTheme.thinking, [3]uint8{120, 140, 150})
	fg := parseHex(currentTheme.fg, [3]uint8{220, 220, 220})
	a1 := parseHex(currentTheme.accent1, [3]uint8{238, 143, 43})
	hot := lerpRGB(a1, [3]uint8{255, 244, 234}, 0.62)
	switch {
	case q < 0.30:
		return lipgloss.Color(hexStr(lerpRGB(m, th, q/0.30)))
	case q < 0.55:
		return lipgloss.Color(hexStr(lerpRGB(th, fg, (q-0.30)/0.25)))
	case q < 0.78:
		return lipgloss.Color(hexStr(lerpRGB(fg, a1, (q-0.55)/0.23)))
	default:
		if q > 1 {
			q = 1
		}
		return lipgloss.Color(hexStr(lerpRGB(a1, hot, (q-0.78)/0.22)))
	}
}

func rampStyle(q float64) lipgloss.Style {
	rampCacheMu.Lock()
	defer rampCacheMu.Unlock()
	if rampCacheSlug != currentSlug {
		for i := 0; i < rampLevels; i++ {
			rampCache[i] = lipgloss.NewStyle().Foreground(rampColor(float64(i) / float64(rampLevels-1)))
		}
		rampCacheSlug = currentSlug
	}
	i := clampInt(int(q*float64(rampLevels-1)+0.5), 0, rampLevels-1)
	return rampCache[i]
}

// ---------- per-frame dot field ----------

// hashPhase is deterministic per grid position — no RNG state, frames are
// pure functions of time.
func hashPhase(x, y int) float64 {
	v := uint32(x)*2654435761 ^ uint32(y)*2246822519 ^ 0x9E3779B9
	v ^= v >> 15
	v *= 2246822519
	v ^= v >> 13
	return float64(v%1000) / 1000.0
}

// depthOf returns a smooth pseudo-depth in [0.60, 1.0]. Layered sines give a
// coherent field (near planes cluster, far planes cluster) rather than
// per-dot white noise — that is what makes the parallax read as volume.
func depthOf(x, y, w, h int) float64 {
	fx := float64(x) / float64(w) * math.Pi * 2
	fy := float64(y) / float64(h) * math.Pi * 2
	n := math.Sin(fx*1.7+math.Cos(fy*1.3+0.6)*1.25) * 0.52
	n += math.Sin(fy*2.35+fx*0.85+1.7) * 0.31
	n += math.Cos(fx*3.15-fy*2.6+0.4) * 0.17
	return 0.60 + 0.40*clamp01(n*0.5+0.5)
}

// caustic is the interplay of two orthogonal light filaments. pow-sharpened
// so the bands are narrow, bright threads rather than a wash.
func caustic(x, y int, t float64) float64 {
	fx, fy := float64(x), float64(y)
	a := math.Sin(fx*0.155 + t*0.52 + math.Sin(fy*0.21-t*0.37)*1.65)
	b := math.Sin(fy*0.185 - t*0.33 + math.Sin(fx*0.12+t*0.45)*1.5)
	c := a * b
	return clamp01(math.Pow(math.Abs(c), 3.2))
}

// cometGlow sweeps one narrow gaussian glare across the field once per
// heartbeat, alternating direction. Returns 0 most of the time; peaks at
// ~1.0 at the leading edge and fades linearly to 0 as the pass completes.
func cometGlow(x, w int, t float64) float64 {
	beat := math.Floor(t / heartbeat)
	phase := math.Mod(t, heartbeat) / 1.35 // crossing takes 1.35s
	if phase >= 1 {
		return 0
	}
	span := float64(w) + 90
	var pos float64
	if int64(beat)%2 == 0 {
		pos = phase*span - 45
	} else {
		pos = float64(w) + 45 - phase*span
	}
	d := float64(x) - pos
	return math.Exp(-(d*d)/(2*8.5*8.5)) * (1 - phase)
}

// sparkle flares rare lattice points for ~0.45 s with exponential decay.
// Slot assignment is hashed off the heartbeat so flashes never synchronize.
func sparkle(x, y int, t float64) float64 {
	h := hashPhase(x*3+17, y*5+29)
	if h < 0.845 {
		return 0
	}
	dt := math.Mod(t+h*7.31, heartbeat)
	if dt > 0.45 {
		return 0
	}
	return math.Exp(-dt*9.5) * (0.72 + 0.28*math.Sin(t*38+h*29))
}

// dotState computes one dot's displacement and brightness for a field of
// (fieldW x fieldH) dots. Ring speed is scaled from the widget reference so
// the same heartbeat reads on any scale.
func dotState(x, y, fieldW, fieldH int, t, morph float64) (xOff, yOff, bright float64) {
	cx := float64(fieldW-1) / 2
	cy := float64(fieldH-1) / 2
	dx := float64(x) - cx
	dy := float64(y) - cy
	d := math.Sqrt(dx*dx + dy*dy)
	var dirx, diry float64
	if d > 0.5 {
		inv := 1 / d
		dirx = dx * inv
		diry = dy * inv
	} else {
		d = 0
	}
	depth := depthOf(x, y, fieldW, fieldH)

	unit := float64(fieldW) / float64(widgetDotW) // ring speed follows field scale
	spd := ringSpeed * unit

	// 10% tangential swirl around the outward kick
	swx := -diry * 0.10
	swy := dirx * 0.10

	ph := hashPhase(x, y)
	// idle drift: smooth vector field, depth-shifted — buoyant, not jittery
	fx := float64(x) * 0.24
	fy := float64(y) * 0.31
	idleX := 0.23*math.Sin(t*0.42+fy*1.15+depth*2.2) + 0.07*math.Sin(t*0.9+ph*6.28)
	idleY := 0.20*math.Cos(t*0.36+fx*1.05-depth*1.8) + 0.06*math.Cos(t*0.8+ph*4.1+1.3)
	// breathing tide rolls outward from the core — the "water moving through
	// the letters" feel, synced in period with the heartbeat
	tide := math.Sin(t*0.62-d*(0.115/math.Max(unit, 0.6))+0.9) * 0.12
	idleBright := 0.585 + tide + 0.065*math.Sin(t*0.9+ph*6.28+1.7)

	// energy fronts: two most recent heartbeat rings, closed form
	glow := 0.0
	puffX, puffY := 0.0, 0.0
	beat := math.Floor(t/heartbeat) * heartbeat
	for k := 0; k < 2; k++ {
		tb := beat - float64(k)*heartbeat
		u := t - tb
		if u <= 0 {
			continue
		}
		amp := math.Exp(-ringDecay * u)
		if amp < 0.02 {
			continue
		}
		r := 4.0 + spd*u // ring radius from just inside the core
		off := (d - r) / ringSig
		if off > 2.4 || off < -2.4 { // gaussian window cutoff
			continue
		}
		w := math.Exp(-off*off) * amp
		glow += w
		// spring puff: ring reached this dot at uA
		uA := u - (d-4.0)/spd
		if uA <= 0 || uA >= 1.2 {
			continue
		}
		ampA := math.Exp(-ringDecay * ((d - 4.0) / spd)) // amp at arrival
		env := kickAmp * ampA * math.Exp(-springD*uA) * math.Sin(springW*uA)
		puffScale := 0.55 + 0.45*depth // shallow planes ride the front, deep planes brace
		puffX = math.Max(puffX, env*(dirx+swx)*puffScale)
		puffY = math.Max(puffY, env*(diry+swy)*puffScale)
	}

	caus := caustic(x, y, t) * (0.10 + 0.15*clamp01(morph))
	comet := cometGlow(x, fieldW, t) * (0.45 + 0.55*clamp01(morph))
	spark := 0.0
	if morph > 0.35 {
		spark = sparkle(x, y, t) * clamp01((morph-0.35)/0.65)
	}

	waveBright := (0.50+0.10*math.Sin(t*2.094))*0.50 + math.Min(1.35, glow*1.15)*0.88 + comet*0.72 + caus + spark*0.95

	blend := clamp01(morph)
	xOff = idleX*(1-blend) + puffX*blend
	yOff = idleY*(1-blend) + puffY*blend
	bright = idleBright*(1-blend) + clamp01(waveBright)*blend
	// depth parallax shading: far planes sit lower in the water
	bright *= 0.72 + 0.28*depth
	if bright < 0.06 {
		bright = 0.06
	}
	if bright > 1 {
		bright = 1
	}
	if xOff > 1.2 {
		xOff = 1.2
	} else if xOff < -1.2 {
		xOff = -1.2
	}
	if yOff > 1.2 {
		yOff = 1.2
	} else if yOff < -1.2 {
		yOff = -1.2
	}
	return xOff, yOff, bright
}

// ---------- braille encoding ----------

// brailleBit maps (dot-row, dot-col) within a 2x4 cell to its Unicode bit.
var brailleBit = [4][2]uint8{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}

// bubbleCount bubbles rise through the crest while thinking
const bubbleCount = 4

// renderBrailleWidget renders the corner widget string. Returns "" whenever
// the widget cannot render (decode failure, too small for the terminal) so
// the caller can fall back to the plain status line.
func renderBrailleWidget(t, morph float64) string {
	mask := widgetMask()
	grid, cellQ := newDotGrid(widgetDotH, widgetDotW)
	// wordmark dots: presence is fixed by the mask; the fronts move light
	for y := 0; y < widgetDotH; y++ {
		for x := 0; x < widgetDotW; x++ {
			if !mask[y][x] {
				continue
			}
			xOff, yOff, bright := dotState(x, y, widgetDotW, widgetDotH, t, morph)
			ix := clampInt(int(float64(x)+xOff+0.5), 0, widgetDotW-1)
			iy := clampInt(int(float64(y)+yOff+0.5), 0, widgetDotH-1)
			if grid[iy][ix] < bright {
				grid[iy][ix] = bright // max-merge: one dot = one point
				cellQ[iy][ix] = bright
			}
		}
	}
	// bubbles with a two-cell fading wake
	if morph > 0.5 {
		placeBubbles2(grid, cellQ, t, morph, widgetDotW, widgetDotH)
	}
	// encode to braille cells — one style per cell from the hottest sub-dot
	return encodeDotField(grid, cellQ, widgetDotW, widgetDotH, widgetCols, widgetRows)
}

// newDotGrid allocates the brightness/heat lattices for one frame.
func newDotGrid(h, w int) (grid, cellQ [][]float64) {
	grid = make([][]float64, h)
	cellQ = make([][]float64, h)
	for y := 0; y < h; y++ {
		grid[y] = make([]float64, w)
		cellQ[y] = make([]float64, w)
	}
	return grid, cellQ
}

// encodeDotField encodes a dot lattice into braille cells, one style per
// cell from the hottest sub-dot.
func encodeDotField(grid, cellQ [][]float64, fieldW, fieldH, cellCols, cellRows int) string {
	var sb strings.Builder
	sb.Grow(cellCols*cellRows*8 + cellRows)
	for cy := 0; cy < cellRows; cy++ {
		if cy > 0 {
			sb.WriteByte('\n')
		}
		for cx := 0; cx < cellCols; cx++ {
			bits := 0
			bright, q := 0.0, 0.0
			for dyi := 0; dyi < 4; dyi++ {
				for dxi := 0; dxi < 2; dxi++ {
					gy, gx := cy*4+dyi, cx*2+dxi
					v := grid[gy][gx]
					if v <= 0.0 {
						continue
					}
					bits |= int(brailleBit[dyi][dxi])
					if v > bright {
						bright = v
					}
					if cellQ[gy][gx] > q {
						q = cellQ[gy][gx]
					}
				}
			}
			if bright <= 0.10 {
				sb.WriteRune(rune(0x2800)) // open water
				continue
			}
			// gamma push: mid-brightness sits in the cool bands of the ramp
			// so comets and sparks detonate instead of the whole word glowing
			hot := math.Pow(clamp01(q), 1.4)
			sb.WriteString(rampStyle(hot).Render(string(rune(0x2800 + bits))))
		}
	}
	return sb.String()
}

// placeBubbles2 drops rising bubbles and their fading wake into grid/cellQ.
// Each bubble detaches shortly after the energy ring passes beneath it; the
// wake below a live bubble holds 0.38 / 0.16 of the bubble's brightness and
// fades in step with it.
func placeBubbles2(grid, cellQ [][]float64, t, morph float64, fieldW, fieldH int) {
	unit := float64(fieldW) / float64(widgetDotW)
	spd := ringSpeed * unit
	for i := 0; i < bubbleCount; i++ {
		h1 := hashPhase(i*37+11, i*91+7)
		h2 := hashPhase(i*13+5, i*7+3)
		xb := 8.0*unit + h1*float64(float64(fieldW)-16*unit)
		dcx := math.Abs(xb - float64(fieldW-1)/2) // distance from core
		uArr := (dcx - 4.0) / spd                 // ring arrival time
		u := math.Mod(t, heartbeat) - uArr - (0.06 + 0.08*h2)
		if u < 0.02 || u > 1.05 {
			continue
		}
		life := u / 0.95
		yTop := 6.0*unit - life*6.0*unit
		iyb := clampInt(int(yTop+0.5), 0, fieldH-1)
		wob := 0.35 * math.Sin(t*2.2+float64(i)*2.1)
		ixb := clampInt(int(xb+wob+0.5), 0, fieldW-1)
		fade := math.Min(life*6, 1.0) * (1.0 - math.Max(0, (life-0.70)/0.30))
		if fade <= 0 {
			continue
		}
		const wake = 3
		for k := 0; k < wake; k++ {
			yy := iyb + k
			if yy >= fieldH {
				break
			}
			scale := 1.0
			val := 0.62 * fade
			if k > 0 {
				scale = []float64{0.38, 0.16}[k-1]
				val = 0.62 * fade * scale
			}
			if grid[yy][ixb] <= 0.15 {
				grid[yy][ixb] = val
				if k == 0 {
					cellQ[yy][ixb] = 0.72
				} else {
					cellQ[yy][ixb] = 0.55
				}
			}
		}
	}
}

// brailleCornerWidget is the public entry the view calls. Pure function of
// (animT, morph, availableWidth): the only side effect is the theme-keyed
// style cache.
func brailleCornerWidget(animT, morph float64, availWidth int) string {
	if !decodeWordmark() {
		return ""
	}
	if availWidth < widgetCols+8 {
		return ""
	}
	return renderBrailleWidget(animT, morph)
}
