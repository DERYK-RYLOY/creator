#!/usr/bin/env python3
"""tools/ascii-art.py — Creator's identity art as animated braille ASCII GIFs.

Everything here renders as REAL braille glyph text (Unicode braille patterns,
U+2800-U+28FF, 2x4 dots per cell) on a pure black background — the same visual
language as the wordmark inside `creator tui`.


The gear wordmark and the gear 'C' keep their own choreography (a scanning
beam sweep / a comet orbiting the ring under a heartbeat) but the same
presence/brightness model: a dot exists where the artwork has energy, and
colour is the hottest sub-dot in its cell — exactly how the TUI inks a cell.

Outputs (written to docs/assets AND cmd/webserver/assets, which embeds them):

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
STILL_FRAME = {"creator-gear-ascii": 0, "creator-logo-ascii": 2}

DISPLAY_GAMMA = 1.0          # display mapping: identity — ramp mids stay luminous         # q-lift into the brand ramp (display shaping, not physics)
BLOOM_GAIN = 0.62            # glow strength for glyph mode

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


# ------------------------------------------------------------ art masks ---
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
    build_gear()
    build_logo()


if __name__ == "__main__":
    sys.exit(main())
