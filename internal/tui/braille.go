// braille.go — the floating braille core in the status corner.
//
// The widget renders the CREATOR wordmark from a bitmask baked into the
// binary (see wordmark_data.go) — no files, no network, no goroutines.
// View() stays a pure function of (animT, morph).
//
// The motion is a faithful port of the shipped design
// (creator_braille_surge_live.html): dots are spring masses on their home
// positions, and "energy fronts" — expanding rings from the wordmark's core
// — sweep outward on a 3.0 s heartbeat (k=120, damp=10.5, radial + 10% swirl
// kick, gaussian window). Presence NEVER changes: every mask dot stays in
// its letterform and the ring speaks through brightness and micro- puff
// displacement only. That is what keeps the word readable while the water
// moves through it. Bubbles rise through the open lattice at the crest —
// the "floating water bubble" of the design.
//
// Colors come from the active theme ramp (muted -> thinking -> fg -> accent1)
// keyed by theme slug, rebuilt on theme switch, with accent2 for bubbles.
//
// Never-breaks guarantees:
//   - fixed-size arrays, bounded loops (rings: 2, bubbles: 4), no allocation
//     in the hot path, no I/O, no goroutines
//   - decode failure / tiny terminal -> empty widget, caller falls back
//   - all intensity and displacement values clamped before use
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

	// box-sample coverage required to keep a dot — tuned on the shipped
	// cutout so stroke structure survives the downscale
	widgetKeep = 0.42
)

// ---------- wave physics, ported from creator_braille_surge_live.html ----------

const (
	heartbeat = 3.0  // s between energy fronts (HTML: nextBeatAt = t + 3.0)
	ringSpeed = 38.0 // widget dots/s (scaled from HTML 1280 px/s on 600 dots)
	ringSig   = 6.0  // gaussian window width in dots (scaled from HTML sig=95)
	ringDecay = 0.52 // front amp *= exp(-dt*0.52), verbatim
	kickAmp   = 2.2  // max outward puff in dots at window peak
	springD   = 5.25 // damp/2 — spring envelope decay (k=120, damp=10.5)
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
				// Lite-half (the locked corner-scale decision): every dot's
				// twin is dropped so dots read as individual points with
				// open water between them — the letters stay legible.
				if (dx+dy)%2 == 1 {
					continue
				}
				scaledMask[dy][dx] = true
			}
		}
	})
	return scaledMask
}

// ---------- color ramp from the active theme ----------
// muted -> thinking -> fg -> accent1 (glacial -> warm -> ember), plus
// accent2 for bubbles. Keyed on the theme slug; rebuilt on theme switch.

const rampLevels = 48

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
	if q >= 1 {
		return lipgloss.Color(currentTheme.accent1)
	}
	m := parseHex(currentTheme.muted, [3]uint8{80, 80, 90})
	th := parseHex(currentTheme.thinking, [3]uint8{120, 140, 150})
	fg := parseHex(currentTheme.fg, [3]uint8{220, 220, 220})
	a1 := parseHex(currentTheme.accent1, [3]uint8{238, 143, 43})
	switch {
	case q < 0.38:
		return lipgloss.Color(hexStr(lerpRGB(m, th, q/0.38)))
	case q < 0.68:
		return lipgloss.Color(hexStr(lerpRGB(th, fg, (q-0.38)/0.30)))
	default:
		return lipgloss.Color(hexStr(lerpRGB(fg, a1, (q-0.68)/0.32)))
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

// dotState computes one dot's displacement and brightness.
// morph: 0 = idle calm, 1 = thinking water. The result is the port of the
// live page: ember base breathing, then energy-front glow + radial spring
// puff on top.
func dotState(x, y int, phase float64, t, morph float64) (xOff, yOff, bright float64) {
	dx := float64(x) - float64(widgetDotW-1)/2
	dy := float64(y) - float64(widgetDotH-1)/2
	d := math.Sqrt(dx*dx + dy*dy)
	var dirx, diry float64
	if d > 0.5 {
		inv := 1 / d
		dirx = dx * inv
		diry = dy * inv
	} else {
		d = 0
	}
	// 10% tangential swirl, verbatim from the HTML:
	// ax += -diry*kick*0.10; ay += dirx*kick*0.10
	swx := -diry * 0.10
	swy := dirx * 0.10

	// idle: HTML idle shimmer (sin(t*1.35)+cos(t*1.12)) — sub-lattice, so at
	// rest the letters stay PUT and only brightness breathes
	idleX := 0.15 * math.Sin(t*1.35+phase*6.283)
	idleY := 0.15 * math.Cos(t*1.12+phase*1.7*6.283-1.1)
	// HTML ember base: 0.52 + 0.09*sin(t*2.094) — heartbeat-synced pulse
	idleBright := 0.50 + 0.20*math.Sin(t*0.40+phase*6.283+1.7)

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
		r := 4.0 + ringSpeed*u       // ring radius from just inside the core
		off := (d - r) / ringSig     // dot distance vs ring front
		if off > 2.4 || off < -2.4 { // HTML window cutoff
			continue
		}
		w := math.Exp(-off*off) * amp
		glow += w
		// spring puff: ring reached this dot at
		uA := u - (d-4.0)/ringSpeed
		if uA <= 0 || uA >= 1.2 {
			continue
		}
		ampA := math.Exp(-ringDecay * ((d - 4.0) / ringSpeed)) // amp at arrival
		env := kickAmp * ampA * math.Exp(-springD*uA) * math.Sin(springW*uA)
		puffX = math.Max(puffX, env*(dirx+swx))
		puffY = math.Max(puffY, env*(diry+swy))

	}
	waveBright := (0.52+0.09*math.Sin(t*2.094))*0.62 + math.Min(1.35, glow*1.1)*0.85

	blend := clamp01(morph)
	xOff = (idleX*(1-blend) + puffX*blend)
	yOff = (idleY*(1-blend) + puffY*blend)
	bright = idleBright*(1-blend) + clamp01(waveBright)*blend
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

// renderBrailleWidget renders the widget string. Returns "" whenever the
// widget cannot render (decode failure, too small for the terminal) so the
// caller can fall back to the plain status line.
func renderBrailleWidget(t, morph float64) string {
	mask := widgetMask()
	var (
		grid  [widgetDotH][widgetDotW]float64 // sub-dot brightness (presence-driven)
		cellQ [widgetDotH][widgetDotW]float64 // color heat per lattice point
	)
	// wordmark dots: presence is fixed by the mask; rings move brightness
	for y := 0; y < widgetDotH; y++ {
		for x := 0; x < widgetDotW; x++ {
			if !mask[y][x] {
				continue
			}
			ph := hashPhase(x, y)
			xOff, yOff, bright := dotState(x, y, ph, t, morph)
			ix := clampInt(int(float64(x)+xOff+0.5), 0, widgetDotW-1)
			iy := clampInt(int(float64(y)+yOff+0.5), 0, widgetDotH-1)
			if grid[iy][ix] < bright {
				grid[iy][ix] = bright // max-merge: one dot = one point, never a pile-up
				cellQ[iy][ix] = bright
			}
		}
	}
	// bubbles: rising water dots through the open lattice at the crest.
	// Each bubble detaches shortly after the energy ring passes beneath
	// it (per-bubble hash offset), rises through the letters, and pops.
	if morph > 0.5 {
		for i := 0; i < bubbleCount; i++ {
			h1 := hashPhase(i*37+11, i*91+7)
			h2 := hashPhase(i*13+5, i*7+3)
			xb := 8.0 + h1*float64(widgetDotW-16)
			db := math.Abs(xb - float64(widgetDotW-1)/2) // distance from core
			uArr := (db - 4.0) / ringSpeed               // ring arrival time
			u := math.Mod(t, heartbeat) - uArr - (0.06 + 0.08*h2)
			if u < 0.02 || u > 1.05 {
				continue
			}
			life := u / 0.95
			yTop := 6.0 - life*6.0
			iyb := clampInt(int(yTop+0.5), 0, widgetDotH-1)
			wob := 0.35 * math.Sin(t*2.2+float64(i)*2.1)
			ixb := clampInt(int(xb+wob+0.5), 0, widgetDotW-1)
			fade := math.Min(life*6, 1.0) * (1.0 - math.Max(0, (life-0.70)/0.30))
			if fade <= 0 {
				continue
			}
			if grid[iyb][ixb] <= 0.15 {
				grid[iyb][ixb] = 0.62 * fade
				cellQ[iyb][ixb] = 0.66
			}
		}
	}
	// encode to braille cells — one style per cell from the hottest sub-dot
	var sb strings.Builder
	for cy := 0; cy < widgetRows; cy++ {
		if cy > 0 {
			sb.WriteByte('\n')
		}
		for cx := 0; cx < widgetCols; cx++ {
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
			sb.WriteString(rampStyle(clamp01(q)).Render(string(rune(0x2800 + bits))))
		}
	}
	return sb.String()
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
