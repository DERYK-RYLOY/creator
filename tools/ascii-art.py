#!/usr/bin/env python3
"""tools/ascii-art.py — Creator's identity art as animated braille ASCII GIFs.

Everything here renders as REAL braille glyph text (Unicode braille patterns,
U+2800-U+28FF, 2x4 dots per cell) on a pure black background — the same visual
language as the wordmark inside `creator tui`.

The banner is a faithful port of the production TUI hero engine
(internal/tui/braille.go + internal/tui/hero.go + internal/tui/wordmark_data.go):
same master mask (decoded from the Go source, so the art can never drift from
the binary), same box-sample downscale with heroKeep/staggered decimation, same
dotState physics — depth planes, layered-sine idle tide, pow-sharpened caustics,
3 s heartbeat energy fronts with spring puff through the volume, an alternating
comet that white-heats dots, exponential sparkles and rising bubbles with
fading wakes. Same ramp shaping (pow 1.4 through the brand teal). The only
substitutions: the theme ramp is the Creator teal brand instead of a runtime
theme, and the field is taller than a terminal hero (48 dot rows, not 32).

The gear wordmark and the gear 'C' keep their own choreography (a scanning
beam sweep / a comet orbiting the ring under a heartbeat) but the same
presence/brightness model: a dot exists where the artwork has energy, and
colour is the hottest sub-dot in its cell — exactly how the TUI inks a cell.

Outputs (written to docs/assets AND cmd/webserver/assets, which embeds them):

  creator-banner-ascii.gif   the word CREATOR as the TUI hero — rings, comet, wave tide
  creator-gear-ascii.gif     the gear-letters CREATOR — scan beam + twinkle
  creator-logo-ascii.gif     the gear 'C' — comet arc on a heartbeat, mote sparkles
  creator-*-ascii.png        matching stills for prefers-reduced-motion

Requires: Python 3 + Pillow. A font with braille coverage is required
(seguisym.ttf on Windows; DejaVu Sans / Noto Symbols2 on Linux; Menlo on
macOS). If none is found the script aborts rather than silently degrading.

Run from the repo root:  python tools/ascii-art.py

Deterministic: sparkles and particles are hash-seeded, so regenerating after a
tune-up produces byte-comparable output. Do not hand-edit the generated GIFs —
edit this script and re-run.
"""

import math
import os
import re
import sys

from PIL import Image, ImageChops, ImageDraw, ImageFilter, ImageFont

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC_DIR = os.path.join(REPO, "docs", "assets")
DESTS = [SRC_DIR, os.path.join(REPO, "cmd", "webserver", "assets")]
TUI_SRC = os.path.join(REPO, "internal", "tui")

# ---------------------------------------------------------------- palette --
# Brand ramp: deep teal -> teal -> ice -> near-white molten tip. Every colour
# in the output comes from this ramp, on pure black.
RAMP_STOPS = [
    (0.00, (4, 20, 22)),
    (0.18, (14, 66, 66)),
    (0.38, (18, 122, 112)),
    (0.58, (34, 199, 181)),   # bright teal
    (0.76, (128, 234, 220)),  # ice-teal
    (0.90, (178, 246, 255)),  # ice
    (1.00, (240, 255, 255)),  # molten near-white
]

BG = (0, 0, 0)               # pure black — the art floats, no panel behind it
GIF_COLORS = 80
STILL_FRAME = {"creator-banner-ascii": 15, "creator-gear-ascii": 0, "creator-logo-ascii": 2}

DISPLAY_GAMMA = 1.0          # display mapping: identity — ramp mids stay luminous         # q-lift into the brand ramp (display shaping, not physics)
BLOOM_GAIN = 0.62            # glow strength for glyph mode

# ----------------------------------------------------------- TUI physics ---
# Verbatim ports of internal/tui/braille.go. Names mirror the Go originals.

HEARTBEAT = 3.0      # s between energy fronts
ENABLE_CAUSTICS = True    # the pool-bottom light bands ARE the wave you asked for
CAUSTIC_GAIN = 4.2          # display weight on the TUI's caustic term (verbatim value: 0.25 max)
BRIGHT_STEPS = 12           # quantize per-dot brightness — identical beads fall into the same
                            # palette entry, which is what keeps LZW cheap
DRIFT_GAIN = 2.4          # idle sway amplified for the large glyph scale (display, not physics)
TIDE_GAIN = 1.9           # radial tide amplitude boost so the word visibly breathes (display)
AMB_KEEP = 0.55           # weight of the calm idle field kept at morph=1, so beads stay luminous (display)
RING_SPEED = 38.0    # dots/s at widget scale
RING_SIG = 6.0       # gaussian window of an expanding front, in dots
RING_DECAY = 0.52    # front amplitude exp(-dt*0.52)
KICK_AMP = 2.2       # max outward puff in dots
SPRING_D = 5.25      # damp/2
SPRING_W = 9.61      # sqrt(k - (damp/2)^2)
WIDGET_DOT_W = 88    # field-scale reference for ringSpeed

BRAILLE_BIT = [(0x01, 0x08), (0x02, 0x10), (0x04, 0x20), (0x40, 0x80)]


def ramp(t):
    t = max(0.0, min(1.0, t))
    for i in range(len(RAMP_STOPS) - 1):
        t0, c0 = RAMP_STOPS[i]
        t1, c1 = RAMP_STOPS[i + 1]
        if t <= t1:
            f = 0.0 if t1 == t0 else (t - t0) / (t1 - t0)
            return tuple(int(round(c0[k] + (c1[k] - c0[k]) * f)) for k in range(3))
    return RAMP_STOPS[-1][1]


def clamp01(v):
    return 0.0 if v < 0.0 else (1.0 if v > 1.0 else v)


def hash_phase(x, y):
    v = ((x * 2654435761) ^ (y * 2246822519) ^ 0x9E3779B9) & 0xFFFFFFFF
    v ^= v >> 15
    v = (v * 2246822519) & 0xFFFFFFFF
    v ^= v >> 13
    return float(v % 1000) / 1000.0


def hash32(*vals):
    h = 2166136261
    for v in vals:
        h ^= v & 0xFFFFFFFF
        h = (h * 16777619) & 0xFFFFFFFF
    h ^= h >> 13
    h = (h * 2654435761) & 0xFFFFFFFF
    h ^= h >> 16
    return h


def smoothstep(a, b, x):
    t = max(0.0, min(1.0, (x - a) / (b - a)))
    return t * t * (3 - 2 * t)


def ease_in_out(t):
    return t * t * (3 - 2 * t)


def envelope(t):
    """Gear/logo loop gain: ember floor, fast attack, slow release (seamless)."""
    a = smoothstep(0.0, 0.045, t)
    b = smoothstep(0.90, 1.0, t)
    return 0.14 + 0.86 * a * (1.0 - b)


def depth_of(x, y, w, h):
    fx = float(x) / float(w) * math.pi * 2
    fy = float(y) / float(h) * math.pi * 2
    n = math.sin(fx * 1.7 + math.cos(fy * 1.3 + 0.6) * 1.25) * 0.52
    n += math.sin(fy * 2.35 + fx * 0.85 + 1.7) * 0.31
    n += math.cos(fx * 3.15 - fy * 2.6 + 0.4) * 0.17
    return 0.60 + 0.40 * clamp01(n * 0.5 + 0.5)


def caustic(x, y, t):
    fx, fy = float(x), float(y)
    a = math.sin(fx * 0.155 + t * 0.52 + math.sin(fy * 0.21 - t * 0.37) * 1.65)
    b = math.sin(fy * 0.185 - t * 0.33 + math.sin(fx * 0.12 + t * 0.45) * 1.5)
    c = a * b
    return clamp01(math.pow(abs(c), 3.2))


def comet_glow(x, w, t):
    beat = math.floor(t / HEARTBEAT)
    phase = math.fmod(t, HEARTBEAT) / 1.35
    if phase >= 1.0:
        return 0.0
    span = float(w) + 90.0
    if int(beat) % 2 == 0:
        pos = phase * span - 45.0
    else:
        pos = float(w) + 45.0 - phase * span
    d = float(x) - pos
    return math.exp(-(d * d) / (2 * 8.5 * 8.5)) * (1.0 - phase)


def sparkle(x, y, t):
    h = hash_phase(x * 3 + 17, y * 5 + 29)
    if h < 0.845:
        return 0.0
    dt = math.fmod(t + h * 7.31, HEARTBEAT)
    if dt > 0.45:
        return 0.0
    return math.exp(-dt * 9.5) * (0.72 + 0.28 * math.sin(t * 38.0 + h * 29.0))


def dot_state(x, y, field_w, field_h, t, morph):
    cx = (field_w - 1) / 2.0
    cy = (field_h - 1) / 2.0
    dx = float(x) - cx
    dy = float(y) - cy
    d = math.hypot(dx, dy)
    if d > 0.5:
        inv = 1.0 / d
        dirx, diry = dx * inv, dy * inv
    else:
        d = 0.0
        dirx = diry = 0.0
    depth = depth_of(x, y, field_w, field_h)

    unit = field_w / WIDGET_DOT_W
    spd = RING_SPEED * unit

    swx = -diry * 0.10   # 10% tangential swirl on the outward kick
    swy = dirx * 0.10

    ph = hash_phase(x, y)
    fx = float(x) * 0.24
    fy = float(y) * 0.31
    idle_x = (0.23 * math.sin(t * 0.42 + fy * 1.15 + depth * 2.2) + 0.07 * math.sin(t * 0.9 + ph * 6.28)) * DRIFT_GAIN
    idle_y = (0.20 * math.cos(t * 0.36 + fx * 1.05 - depth * 1.8) + 0.06 * math.cos(t * 0.8 + ph * 4.1 + 1.3)) * DRIFT_GAIN
    tide = math.sin(t * 0.62 - d * (0.115 / max(unit, 0.6)) + 0.9) * 0.12
    idle_bright = 0.585 + tide * TIDE_GAIN + 0.065 * math.sin(t * 0.9 + ph * 6.28 + 1.7)

    glow = 0.0
    puff_x = puff_y = 0.0
    beat = math.floor(t / HEARTBEAT) * HEARTBEAT
    for k in range(2):
        tb = beat - k * HEARTBEAT
        u = t - tb
        if u <= 0:
            continue
        amp = math.exp(-RING_DECAY * u)
        if amp < 0.02:
            continue
        r = 4.0 + spd * u
        off = (d - r) / RING_SIG
        if off > 2.4 or off < -2.4:
            continue
        w = math.exp(-off * off) * amp
        glow += w
        u_a = u - (d - 4.0) / spd
        if u_a <= 0 or u_a >= 1.2:
            continue
        amp_a = math.exp(-RING_DECAY * ((d - 4.0) / spd))
        env = KICK_AMP * amp_a * math.exp(-SPRING_D * u_a) * math.sin(SPRING_W * u_a)
        puff_scale = 0.55 + 0.45 * depth
        puff_x = max(puff_x, env * (dirx + swx) * puff_scale)
        puff_y = max(puff_y, env * (diry + swy) * puff_scale)

    caus = caustic(x, y, t) * (0.10 + 0.15 * clamp01(morph)) * CAUSTIC_GAIN if ENABLE_CAUSTICS else 0.0
    comet = comet_glow(x, field_w, t) * (0.45 + 0.55 * clamp01(morph))
    spark = 0.0
    if morph > 0.35:
        spark = sparkle(x, y, t) * clamp01((morph - 0.35) / 0.65)

    wave_bright = (0.50 + 0.10 * math.sin(t * 2.094)) * 0.50 + min(1.35, glow * 1.15) * 0.88 + comet * 0.72 + caus + spark * 0.95

    blend = clamp01(morph)
    eff_idle = 1.0 - blend * (1.0 - AMB_KEEP)   # keep some calm field even at full energy
    x_off = idle_x * (1 - blend) + puff_x * blend
    y_off = idle_y * (1 - blend) + puff_y * blend
    bright = idle_bright * eff_idle + clamp01(wave_bright) * (1.0 - eff_idle)
    bright *= 0.72 + 0.28 * depth
    if BRIGHT_STEPS:
        bright = math.floor(bright * BRIGHT_STEPS + 0.5) / BRIGHT_STEPS
    bright = min(1.0, max(0.06, bright))
    if x_off > 1.2:
        x_off = 1.2
    elif x_off < -1.2:
        x_off = -1.2
    if y_off > 1.2:
        y_off = 1.2
    elif y_off < -1.2:
        y_off = -1.2
    return x_off, y_off, bright


def place_bubbles(grid, cell_q, t, morph, field_w, field_h):
    unit = field_w / WIDGET_DOT_W
    spd = RING_SPEED * unit
    for i in range(4):
        h1 = hash_phase(i * 37 + 11, i * 91 + 7)
        h2 = hash_phase(i * 13 + 5, i * 7 + 3)
        xb = 8.0 * unit + h1 * float(field_w - 16.0 * unit)
        d_cx = abs(xb - (field_w - 1) / 2.0)
        u_arr = (d_cx - 4.0) / spd
        u = math.fmod(t, HEARTBEAT) - u_arr - (0.06 + 0.08 * h2)
        if u < 0.02 or u > 1.05:
            continue
        life = u / 0.95
        y_top = 6.0 * unit - life * 6.0 * unit
        iyb = min(field_h - 1, max(0, int(y_top + 0.5)))
        wob = 0.35 * math.sin(t * 2.2 + float(i) * 2.1)
        ixb = min(field_w - 1, max(0, int(xb + wob + 0.5)))
        fade = min(life * 6.0, 1.0) * (1.0 - max(0.0, (life - 0.70) / 0.30))
        if fade <= 0:
            continue
        wake = [1.0, 0.38, 0.16]
        for k in range(3):
            yy = iyb + k
            if yy >= field_h:
                break
            val = 0.62 * fade * wake[k]
            if grid[yy][ixb] <= 0.15:
                grid[yy][ixb] = val
                cell_q[yy][ixb] = 0.72 if k == 0 else 0.55


# ------------------------------------------------------------ art masks ---
def master_mask():
    """Decode the production master mask out of internal/tui/wordmark_data.go.

    Parsing the Go source keeps the GIF in lockstep with the binary — if the
    mask is regenerated there, re-running this script picks it up.
    """
    src = open(os.path.join(TUI_SRC, "wordmark_data.go"), encoding="utf-8").read()
    m = re.search(r"wordmarkBits\s*=\s*\n?\s*`([0-9a-fA-F\n]+)`", src)
    if not m:
        raise RuntimeError("wordmarkBits blob not found in internal/tui/wordmark_data.go")
    rows = []
    for line in m.group(1).strip().splitlines():
        line = line.strip()
        if not line:
            continue
        bits = bin(int(line, 16))[2:].rjust(len(line) * 4, "0")
        rows.append([c == "1" for c in bits])
    if len(rows) != 96:
        raise RuntimeError(f"master mask decode failed: expected 96 rows, got {len(rows)}")
    if any(len(r) != 600 for r in rows):
        raise RuntimeError("master mask decode failed: row width mismatch")
    return rows


def hero_lattice(mask, dot_w, dot_h, keep=0.24, decimate=True):
    """Box-sample the master mask down to a dot lattice with the hero's
    staggered decimation — the points-in-water look, verbatim from hero.go.
    decimate=False keeps every surviving dot (square braille dots don't need
    the terminal's vertical thinning; the GIF reads denser and richer)."""
    mh, mw = len(mask), len(mask[0])
    out = []
    for dy in range(dot_h):
        sy0 = dy * mh // dot_h
        sy1 = (dy + 1) * mh // dot_h
        if sy1 <= sy0:
            sy1 = sy0 + 1
        row = []
        for dx in range(dot_w):
            sx0 = dx * mw // dot_w
            sx1 = (dx + 1) * mw // dot_w
            if sx1 <= sx0:
                sx1 = sx0 + 1
            total = 0
            for y in range(sy0, sy1):
                for x in range(sx0, sx1):
                    if mask[y][x]:
                        total += 1
            cell = (sy1 - sy0) * (sx1 - sx0)
            on = total / cell > keep and (not decimate or (dx + dy) % 2 == 0)
            row.append(on)
        out.append(row)
    return out


def neon_mask(path, cols, rows):
    src = Image.open(path)
    if src.mode == "RGBA":
        bbox = src.getchannel("A").getbbox()
    else:
        bbox = None
    if bbox:
        src = src.crop(bbox)
    src = src.convert("RGB")
    dot_cols, dot_rows = cols * 2, rows * 4
    small = src.resize((dot_cols, dot_rows), Image.LANCZOS)
    smooth = small.filter(ImageFilter.GaussianBlur(0.55))
    px = smooth.load()
    rows_out = []
    for y in range(dot_rows):
        row = []
        for x in range(dot_cols):
            r, g, b = px[x, y]
            hi = max(g, b)
            lo = min(g, b)
            cov = (hi * 0.92 + lo * 0.42 - r * 0.78 - 46.0) / 175.0
            row.append(max(0.0, min(1.0, cov)))
        rows_out.append(row)
    return rows_out


def mask_aspect(path):
    src = Image.open(path)
    a = src.getchannel("A") if src.mode == "RGBA" else src.convert("L")
    bbox = a.getbbox()
    if bbox:
        return (bbox[2] - bbox[0]) / (bbox[3] - bbox[1])
    return src.width / src.height


# --------------------------------------------------------- glyph encoder ---
BRAILLE_CANDIDATES = [
    r"C:\Windows\Fonts\seguisym.ttf",
    r"C:\Windows\Fonts\seguiemj.ttf",
    "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
    "/usr/share/fonts/truetype/noto/NotoSansSymbols2-Regular.ttf",
    "/System/Library/Fonts/Menlo.ttc",
]

_font_cache = {}


def braille_font(em):
    if em not in _font_cache:
        for p in BRAILLE_CANDIDATES:
            if os.path.exists(p):
                try:
                    _font_cache[em] = ImageFont.truetype(p, em)
                    return _font_cache[em]
                except Exception:
                    continue
        raise RuntimeError("no font with braille coverage found (tried: " + ", ".join(BRAILLE_CANDIDATES) + ")")
    return _font_cache[em]


def cell_metrics(font):
    adv = font.getlength("⣿")
    bbox = font.getbbox("⣿")
    return adv, bbox


def encode_braille_canvas(grid, cell_q, cols, rows, em, blur=0.0):
    """Draw a dot lattice as real braille glyph cells on pure black.

    grid / cell_q: dotW x dotH floats (brightness; presence = value > 0).
    One colour per cell, from the hottest sub-dot — the TUI's encodeDotField
    feeds pow(q, 1.4) through the ramp; we lift with pow(q, DISPLAY_GAMMA) so
    the quiet idle tide still reads as luminous ice on black. Physics is
    untouched — this is display mapping only.
    """
    font = braille_font(em)
    adv, _ = cell_metrics(font)
    # integer-rounded cumulative x so cells never drift apart
    xs = [round(i * adv) for i in range(cols)] + [round(cols * adv)]
    w, h = xs[-1], rows * em
    img = Image.new("RGB", (w, h), BG)
    d = ImageDraw.Draw(img)
    for cy in range(rows):
        y = cy * em + 2  # 2px optical centring inside the EM box
        for cx in range(cols):
            bits = 0
            q = 0.0
            for dyi in range(4):
                for dxi in range(2):
                    gy = cy * 4 + dyi
                    gx = cx * 2 + dxi
                    v = grid[gy][gx]
                    if v <= 0.0:
                        continue
                    bits |= BRAILLE_BIT[dyi][dxi]
                    cq = cell_q[gy][gx]
                    if cq > q:
                        q = cq
            if bits:
                hot = q ** DISPLAY_GAMMA
                col = ramp(hot)
                d.text((xs[cx], y), chr(0x2800 + bits), font=font, fill=col)
    if blur > 0:
        base = Image.new("RGB", (w, h), BG)
        glow = img.filter(ImageFilter.GaussianBlur(blur)).point(lambda v: int(v * BLOOM_GAIN))
        base = ImageChops.screen(base, glow)
        return ImageChops.screen(base, img)
    return img


def crossfade_seam(frames, k=10):
    """Glide the tail of the loop into frame 0 so the GIF wraps seamlessly."""
    n = len(frames)
    f0 = frames[0]
    for i in range(n - k, n):
        w = ((i - (n - k) + 1) / k) ** 2
        frames[i] = Image.blend(frames[i], f0, w)
    frames[-1] = f0.copy()


def save_gif(frames, out_path, duration, loop=0, seam=True):
    if seam:
        crossfade_seam(frames, k=10)
    shared = frames[0].quantize(colors=GIF_COLORS, method=Image.MEDIANCUT)
    p_frames = [fr.quantize(colors=GIF_COLORS, method=Image.MEDIANCUT, palette=shared, dither=Image.Dither.NONE) for fr in frames]
    p_frames[0].save(out_path, save_all=True, append_images=p_frames[1:],
                     duration=duration, loop=loop, optimize=True)


def write_outputs(name, frames, duration, still_index, seam=True):
    for dest in DESTS:
        os.makedirs(dest, exist_ok=True)
        out = os.path.join(dest, name + ".gif")
        save_gif(list(frames), out, duration, seam=seam)
        frames[still_index].save(os.path.join(dest, name + ".png"), optimize=True)
        print(f"{name:26s} {os.path.getsize(out) // 1024:5d} KB  ({frames[0].width}x{frames[0].height}, {len(frames)}f)")


# ------------------------------------------------------------- builders ---
def build_banner(n_frames=75, duration=80, em=17, morph=1.0):
    """The TUI welcome hero — verbatim physics, brand teal ramp, taller lattice."""
    mask = master_mask()
    dot_w, dot_h = 300, 48   # AR-true 600x96, twice the terminal hero's density
    presence = hero_lattice(mask, dot_w, dot_h, keep=0.24, decimate=False)
    cols, rows = dot_w // 2, dot_h // 4
    frames = []
    print(f"banner lattice: {dot_w}x{dot_h} dots, {cols}x{rows} braille cells")
    for f in range(n_frames):
        t = f * (HEARTBEAT * 2.0 / n_frames)
        grid = [[0.0] * dot_w for _ in range(dot_h)]
        cell_q = [[0.0] * dot_w for _ in range(dot_h)]
        for y in range(dot_h):
            row_p = presence[y]
            for x in range(dot_w):
                if not row_p[x]:
                    continue
                x_off, y_off, bright = dot_state(x, y, dot_w, dot_h, t, morph)
                ix = min(dot_w - 1, max(0, int(x + x_off + 0.5)))
                iy = min(dot_h - 1, max(0, int(y + y_off + 0.5)))
                if grid[iy][ix] < bright:
                    grid[iy][ix] = bright
                    cell_q[iy][ix] = bright
        if morph > 0.5:
            place_bubbles(grid, cell_q, t, morph, dot_w, dot_h)
        frames.append(encode_braille_canvas(grid, cell_q, cols, rows, em, blur=0.0))
    write_outputs("creator-banner-ascii", frames, duration, STILL_FRAME["creator-banner-ascii"], seam=True)


def build_gear(cols=132, n_frames=38, duration=55, em=16, neon=True):
    """The gear-letters CREATOR — scan beam + twinkle, as braille cells."""
    rows = max(8, round(cols / (mask_aspect(os.path.join(SRC_DIR, "creator-gear.png")) * 2)))
    mask = neon_mask(os.path.join(SRC_DIR, "creator-gear.png"), cols, rows)
    dot_cols, dot_rows = cols * 2, rows * 4
    frames = []
    for f in range(n_frames):
        t = (0.30 + f / n_frames) % 1.0   # frame 0 opens on an ignited mark
        tide = 0.78 + 0.30 * math.sin(2 * math.pi * (2 * t) - math.pi / 2.6)
        beam_t = max(0.0, min(1.0, (t - 0.16) / 0.62))
        beam_x = -0.14 + 1.30 * ease_in_out(beam_t)
        attack = envelope(t)
        draw_front = ease_in_out(smoothstep(0.02, 0.26, t)) * 1.10 - 0.055
        grid = [[0.0] * dot_cols for _ in range(dot_rows)]
        cell_q = [[0.0] * dot_cols for _ in range(dot_rows)]
        for y in range(dot_rows):
            ty = y / max(1, dot_rows - 1)
            for x in range(dot_cols):
                cov = mask[y][x]
                if cov <= 0.10:
                    continue
                nx = x / max(1, dot_cols - 1)
                reveal = 0.14 + 0.86 * smoothstep(draw_front, draw_front - 0.085, nx)
                if reveal <= 0.10:
                    continue
                d = abs(nx - beam_x)
                head = math.exp(-(d * d) / (2 * 0.058 ** 2))
                behind = beam_x - nx
                tail = math.exp(-behind / 0.16) if behind > 0 else 0.0
                boost = 1.0 + 1.65 * head + 0.85 * tail
                lead = math.exp(-((nx - draw_front) / 0.016) ** 2) if draw_front < 1.05 else 0.0
                tw = hash32(x, y, f // 2, 19)
                sparkle_on = tw % 977 == 0 and cov > 0.3
                v = cov ** 0.92 * attack * tide * reveal * boost
                if sparkle_on:
                    v = 1.6
                v = min(2.6, v)
                # presence threshold — dots switch on/off as the scan passes
                if v < 0.16:
                    continue
                iy, ix = y, x
                grid[iy][ix] = v
                cell_q[iy][ix] = min(1.0, (0.42 * ty) + (v - 1.0) / 2.6 + 0.45 + (0.5 if sparkle_on else 0.0) + 0.85 * lead)
        frames.append(encode_braille_canvas(grid, cell_q, cols, rows, em, blur=1.6))
    write_outputs("creator-gear-ascii", frames, duration, STILL_FRAME["creator-gear-ascii"], seam=False)


def build_logo(cols=78, n_frames=44, duration=55, em=15):
    """The gear 'C' — comet orbit under a heartbeat, as braille cells."""
    src = os.path.join(SRC_DIR, "icon-512.png")
    rows = max(8, round(cols / (mask_aspect(src) * 2)))
    mask = neon_mask(src, cols, rows)
    dot_cols, dot_rows = cols * 2, rows * 4
    cx, cy = dot_cols / 2.0, dot_rows / 2.0
    frames = []
    for f in range(n_frames):
        t = (0.30 + f / n_frames) % 1.0
        tide = 0.78 + 0.30 * math.sin(2 * math.pi * 2 * t - math.pi / 2.6)
        tide *= 1.0 + 0.34 * math.exp(-((t - 0.34) / 0.045) ** 2) + 0.22 * math.exp(-((t - 0.485) / 0.06) ** 2)
        attack = envelope(t)
        scan = 2 * math.pi * ((t - 0.14) / 0.72)
        draw_front = ease_in_out(smoothstep(0.02, 0.26, t)) * 1.10 - 0.055
        grid = [[0.0] * dot_cols for _ in range(dot_rows)]
        cell_q = [[0.0] * dot_cols for _ in range(dot_rows)]
        for y in range(dot_rows):
            ty = y / max(1, dot_rows - 1)
            for x in range(dot_cols):
                cov = mask[y][x]
                if cov <= 0.10:
                    continue
                nx = x / max(1, dot_cols - 1)
                reveal = 0.14 + 0.86 * smoothstep(draw_front, draw_front - 0.085, nx)
                if reveal <= 0.10:
                    continue
                ang = math.atan2(y - cy, x - cx)
                d = (ang - scan + math.pi) % (2 * math.pi) - math.pi
                head = math.exp(-(d * d) / (2 * 0.30 ** 2))
                behind = (scan - ang) % (2 * math.pi)
                tail = math.exp(-behind / 1.15) if behind > 0 else 0.0
                boost = 1.0 + 1.9 * head + 1.05 * tail
                lead = math.exp(-((nx - draw_front) / 0.016) ** 2) if draw_front < 1.05 else 0.0
                tw = hash32(x, y, f // 2, 31)
                sparkle_on = tw % 977 == 0 and cov > 0.3
                v = cov ** 0.92 * attack * tide * reveal * boost
                if sparkle_on:
                    v = 1.7
                v = min(2.6, v)
                if v < 0.16:
                    continue
                grid[y][x] = v
                cell_q[y][x] = min(1.0, 0.42 * ty + (v - 1.0) / 2.6 + 0.45 + (0.5 if sparkle_on else 0.0) + 0.85 * lead)
        frames.append(encode_braille_canvas(grid, cell_q, cols, rows, em, blur=1.6))
    write_outputs("creator-logo-ascii", frames, duration, STILL_FRAME["creator-logo-ascii"], seam=False)


def main():
    build_banner()
    build_gear()
    build_logo()


if __name__ == "__main__":
    sys.exit(main())
