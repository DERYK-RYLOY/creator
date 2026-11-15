#!/usr/bin/env python3
"""tools/ascii-art.py — render the Creator marks as animated braille ASCII GIFs.

The Creator identity already lives inside the TUI as a tick-driven braille dot
field (internal/tui). This script gives the *repository* the same treatment:
the artwork in docs/assets is resampled onto a 2x4 braille lattice and
re-rendered as animated dot-field GIFs with a scan beam, phosphor bloom,
sparkles, rising motes and a breathing tide — the same family of effects the
mission-control wordmark uses.

Outputs (written to docs/assets AND cmd/webserver/assets, which embeds them):

  creator-banner-ascii.gif   the sci-fi CREATOR wordmark, beam sweep + twinkle
  creator-gear-ascii.gif     the gear-letters CREATOR wordmark, neon scan
  creator-logo-ascii.gif     the gear 'C' — comet arc orbits the ring, motes orbit the aperture
  creator-*-ascii.png        matching stills (held "sustain" frame) for prefers-reduced-motion

Requires: Python 3 + Pillow (pip install pillow). Run from the repo root:

  python tools/ascii-art.py

Deterministic: every sparkle/particle is hash-seeded, so regenerating after a
tune-up produces byte-comparable output. Do not hand-edit the generated GIFs —
edit this script and re-run.
"""

import math
import os
import random
import sys

from PIL import Image, ImageChops, ImageDraw, ImageFilter

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC_DIR = os.path.join(REPO, "docs", "assets")
DESTS = [SRC_DIR, os.path.join(REPO, "cmd", "webserver", "assets")]

# ---------------------------------------------------------------- palette --
# Brand ramp: deep teal -> teal -> ice -> near-white. Every shade in the
# output comes from this ramp, so the GIFs sit cleanly on black anywhere.
RAMP_STOPS = [
    (0.00, (10, 38, 40)),
    (0.28, (13, 84, 82)),
    (0.52, (20, 184, 166)),   # teal-500
    (0.72, (56, 189, 248)),   # sky-400
    (0.88, (150, 243, 255)),  # ice
    (1.00, (234, 254, 255)),  # molten near-white
]

BG_TOP = (5, 8, 12)
BG_BOT = (9, 13, 18)
INK = (222, 250, 252)

CELL_W, CELL_H = 8, 16          # braille cell box in pixels (2x4 dots each)
DOT_R = 1.72                    # max dot radius in pixels (keeps the lattice visible)
BLOOM = 2.2                     # phosphor bloom blur radius
BLOOM_GAIN = 0.66               # how hot the bloom composite runs
GIF_COLORS = 96                # shared palette size (keeps GIFs small)
STILL_T = 0.55                  # which loop position the static still freezes
T_OFFSET = 0.30                 # GIF frame 0 shows a sustained, ignited mark (seamless loop)


def ramp(t):
    t = max(0.0, min(1.0, t))
    for i in range(len(RAMP_STOPS) - 1):
        t0, c0 = RAMP_STOPS[i]
        t1, c1 = RAMP_STOPS[i + 1]
        if t <= t1:
            f = 0.0 if t1 == t0 else (t - t0) / (t1 - t0)
            return tuple(int(round(c0[k] + (c1[k] - c0[k]) * f)) for k in range(3))
    return RAMP_STOPS[-1][1]


def hash32(*vals):
    h = 2166136261
    for v in vals:
        h ^= v & 0xffffffff
        h = (h * 16777619) & 0xffffffff
    h ^= h >> 13
    h = (h * 2654435761) & 0xffffffff
    h ^= h >> 16
    return h


def smoothstep(a, b, x):
    t = max(0.0, min(1.0, (x - a) / (b - a)))
    return t * t * (3 - 2 * t)


def ease_in_out(t):
    return t * t * (3 - 2 * t)


def envelope(t):
    """Global gain across the loop: 0.14 ember floor, fast attack, slow release.

    Identical at t=0 and t=1, so the GIF loops seamlessly and never shows a
    dead-black frame.
    """
    a = smoothstep(0.0, 0.045, t)
    b = smoothstep(0.90, 1.0, t)
    return 0.14 + 0.86 * a * (1.0 - b)


# ------------------------------------------------------------- mask prep --
def alpha_mask(path, cols, rows):
    """Coverage mask of a transparent-cutout PNG, resampled to the dot grid."""
    src = Image.open(path).convert("RGBA")
    a = src.getchannel("A")
    bbox = a.getbbox()
    if bbox:
        src = src.crop(bbox)
    dot_cols, dot_rows = cols * 2, rows * 4
    small = src.resize((dot_cols, dot_rows), Image.LANCZOS)
    a = small.getchannel("A")
    px = a.load()
    return [[px[x, y] / 255.0 for x in range(dot_cols)] for y in range(dot_rows)]


def neon_mask(path, cols, rows):
    """Coverage mask of art that is neon-on-black.

    Used for the gear wordmarks and the gear-C icon: crushes the background
    and the dark fill plates to near-zero, keeps the hot teal strokes and the
    concrete greys, then gaussian-smooths the lattice so the GIF palette can
    cluster (this is what keeps the files small — film grain doesn't compress).
    """
    src = Image.open(path)
    if src.mode == "RGBA":
        a = src.getchannel("A")
        bbox = a.getbbox()
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
            # neon teal: strong g/b, weak r; concrete: g≈b mid-high — both pass.
            cov = (hi * 0.92 + lo * 0.42 - r * 0.78 - 46.0) / 175.0
            row.append(max(0.0, min(1.0, cov)))
        rows_out.append(row)
    return rows_out


def mask_aspect(path, mask=True):
    src = Image.open(path)
    a = src.getchannel("A") if src.mode == "RGBA" else src.convert("L")
    bbox = a.getbbox()
    if bbox:
        return (bbox[2] - bbox[0]) / (bbox[3] - bbox[1])
    return src.width / src.height


# ------------------------------------------------------------- rendering --
def make_background(w, h):
    img = Image.new("RGB", (w, h))
    px = img.load()
    for y in range(h):
        f = y / max(1, h - 1)
        c = tuple(int(BG_TOP[k] + (BG_BOT[k] - BG_TOP[k]) * f) for k in range(3))
        for x in range(w):
            px[x, y] = c
    d = ImageDraw.Draw(img, "RGBA")
    step = 26
    for gy in range(step // 2, h, step):
        for gx in range(step // 2, w, step):
            d.ellipse([gx - 0.7, gy - 0.7, gx + 0.7, gy + 0.7], fill=(255, 255, 255, 12))
    # faint horizontal scanline every 4px
    for sy in range(0, h, 4):
        d.line([(0, sy), (w, sy)], fill=(255, 255, 255, 5))
    return img


def vignette(img, strength=0.32):
    """Darken the corners by up to `strength`, keep the centre untouched."""
    w, h = img.size
    m = Image.new("L", (w, h), 0)
    d = ImageDraw.Draw(m)
    d.ellipse([-w * 0.22, -h * 0.55, w * 1.22, h * 1.55], fill=255)
    m = m.filter(ImageFilter.GaussianBlur(max(8, int(w * 0.07))))
    mult = m.point(lambda v: int((1.0 - (1.0 - v / 255.0) * strength) * 255)).convert("RGB")
    return ImageChops.multiply(img, mult)


def render_dots_frame(mask, cols, rows, dots_cfg, t, frame_index, motes):
    """One frame. dots_cfg: dict with beam/tide parameters for this artwork."""
    dot_cols, dot_rows = cols * 2, rows * 4
    w, h = cols * CELL_W + 2, rows * CELL_H + 2
    base = make_background(w, h)

    dots = Image.new("RGB", (w, h), (0, 0, 0))
    dd = ImageDraw.Draw(dots)

    W_norm = max(1, dot_cols - 1)
    H_norm = max(1, dot_rows - 1)

    # ---- envelope: ember floor everywhere, quick ignite attack, slow release
    attack = envelope(t)
    draw_front = ease_in_out(smoothstep(0.02, 0.26, t)) * 1.10 - 0.055  # in dot-grid x units
    tide = 0.78 + 0.30 * math.sin(2 * math.pi * (2 * t) - math.pi / 2.6)
    if dots_cfg.get("heartbeat"):
        tide *= 1.0 + 0.34 * math.exp(-((t - 0.34) / 0.045) ** 2) + 0.22 * math.exp(-((t - 0.485) / 0.06) ** 2)

    beam_t = max(0.0, min(1.0, (t - 0.16) / 0.62))
    beam_x = -0.14 + 1.30 * ease_in_out(beam_t)   # in normalized x
    beam_sigma = dots_cfg.get("beam_sigma", 0.052)
    scan_ang = 2 * math.pi * (t - 0.14) / 0.72 if dots_cfg.get("radial") else None

    cx, cy = dot_cols / 2.0, dot_rows / 2.0

    cov_floor = dots_cfg.get("cov_floor", 0.045)

    for y in range(dot_rows):
        ty = y / H_norm
        for x in range(dot_cols):
            cov = mask[y][x]
            if cov <= cov_floor:
                continue
            nx = x / W_norm

            # draw-on reveal front: a white-hot leading edge ignites the mark.
            # Ember floor keeps the mark faintly visible ahead of the front.
            reveal = 0.14 + 0.86 * smoothstep(draw_front, draw_front - 0.085, nx + (y / H_norm) * 0.012)
            if reveal <= 0.004:
                continue
            lead = math.exp(-((nx - draw_front) / 0.016) ** 2) if draw_front < 1.05 else 0.0

            # energy boost: linear sweeping beam OR radial comet arc
            if scan_ang is not None:
                ang = math.atan2(y - cy, x - cx)
                d = (ang - scan_ang + math.pi) % (2 * math.pi) - math.pi
                head = math.exp(-(d * d) / (2 * 0.30 ** 2))
                behind = (scan_ang - ang) % (2 * math.pi)
                tail = math.exp(-behind / 1.15) if behind > 0 else 0.0
                boost = 1.0 + 1.9 * head + 1.05 * tail
            else:
                d = abs(nx - beam_x)
                head = math.exp(-(d * d) / (2 * beam_sigma ** 2))
                behind = beam_x - nx
                tail = math.exp(-behind / 0.16) if behind > 0 else 0.0
                boost = 1.0 + 1.65 * head + 0.85 * tail

            # hash-seeded twinkle — a few cells detonate white for two frames
            tw = hash32(x, y, frame_index // 2, dots_cfg.get("salt", 7))
            sparkle = tw % 977 == 0 and cov > 0.30

            gain = attack * tide
            v = cov ** 0.92 * gain * boost
            if sparkle:
                v = 1.9
            v = min(2.6, v)

            # colour: vertical brand ramp, driven hot by the boost term
            heat = min(1.0, 0.30 + 0.42 * ty + 0.32 * (boost - 1.0) / 1.6 + (0.55 if sparkle else 0.0) + 0.9 * lead)
            r, g, b = ramp(heat)
            hot = min(1.0, (v - 1.0) / 1.3) if v > 1.0 else 0.0
            if hot > 0:
                r = int(r + (255 - r) * hot)
                g = int(g + (255 - g) * hot)
                b = int(b + (255 - b) * hot)
            scale = min(1.0, 0.24 + 0.76 * min(1.0, v))
            col = (int(r * scale), int(g * scale), int(b * scale))

            rad = DOT_R * math.sqrt(min(1.0, cov)) * (1.0 + 0.55 * lead + (0.35 if sparkle else 0.0))
            px_ = 1 + x * (CELL_W / 2) + CELL_W / 4
            py_ = 1 + y * (CELL_H / 4) + CELL_H / 8
            dd.ellipse([px_ - rad, py_ - rad, px_ + rad, py_ + rad], fill=col)

    # phosphor bloom: blurred copy of the dot layer screened underneath
    bloom = dots.filter(ImageFilter.GaussianBlur(BLOOM))
    bloom = bloom.point(lambda v: int(v * BLOOM_GAIN))
    base = ImageChops.screen(base, bloom)
    base = ImageChops.screen(base, dots)

    # motes — seeded rising sparks / orbiting aperture dust
    md = ImageDraw.Draw(base, "RGBA")
    for m in motes:
        draw_mote(md, m, t, w, h, dots_cfg)

    return vignette(base)


def seed_motes(n, kind, seed):
    rnd = random.Random(seed)
    motes = []
    for _ in range(n):
        m = {
            "kind": kind,
            "x": rnd.uniform(0.06, 0.94),
            "phase": rnd.uniform(0.0, 1.0),
            "speed": rnd.uniform(0.55, 1.15),
            "size": rnd.uniform(1.1, 2.3),
            "wobble": rnd.uniform(0.4, 1.0),
            "orbit_r": rnd.uniform(0.30, 0.52),   # logo aperture orbit radii (fraction of half-width)
            "dir": 1 if rnd.random() < 0.75 else -1,
        }
        motes.append(m)
    return motes


def draw_mote(d, m, t, w, h, cfg):
    if m["kind"] == "rise":
        yy = (m["phase"] + t * m["speed"]) % 1.0
        py_ = h - yy * (h + 30)
        px_ = m["x"] * w + math.sin(yy * 9 + m["phase"] * 40) * 10 * m["wobble"]
        fade = math.sin(math.pi * yy) ** 1.4
        a = int(200 * fade)
        if a <= 8:
            return
        col = (150, 243, 255, a)
        s = m["size"]
        d.ellipse([px_ - s, py_ - s, px_ + s, py_ + s], fill=col)
        d.ellipse([px_ - s * 0.4, py_ + s, px_ + s * 0.4, py_ + s * 3.2], fill=(150, 243, 255, a // 3))
    else:  # orbit the aperture
        ang = 2 * math.pi * (m["phase"] + t * m["speed"] * 0.36 * m["dir"])
        cx, cy = w / 2.0, h / 2.0
        rr = m["orbit_r"] * min(w, h) * 0.5
        px_ = cx + math.cos(ang) * rr
        py_ = cy + math.sin(ang) * rr * 0.96
        a = int(190 * (0.55 + 0.45 * math.sin(2 * math.pi * (t * 2 + m["phase"]))))
        if a <= 8:
            return
        s = m["size"]
        d.ellipse([px_ - s, py_ - s, px_ + s, py_ + s], fill=(150, 243, 255, a))
        # wake
        for k in range(1, 4):
            pa = ang - k * 0.16 * m["dir"]
            wx = cx + math.cos(pa) * rr
            wy = cy + math.sin(pa) * rr * 0.96
            d.ellipse([wx - s * 0.7, wy - s * 0.7, wx + s * 0.7, wy + s * 0.7], fill=(94, 234, 212, a // (k + 1)))


# ------------------------------------------------------------------ main --
def grid_for(src_path, cols):
    ar = mask_aspect(src_path)
    rows = max(8, round(cols / (ar * (CELL_H / CELL_W))))
    return rows


def build(name, src_path, cols, cfg, n_frames, duration, motes_kind=None, motes_n=0, seed=1234):
    rows = grid_for(src_path, cols)
    loader = neon_mask if cfg.get("neon") else alpha_mask
    mask = loader(src_path, cols, rows)
    motes = seed_motes(motes_n, motes_kind, seed) if motes_n else []
    frames = []
    for f in range(n_frames):
        t = (T_OFFSET + f / n_frames) % 1.0
        frames.append(render_dots_frame(mask, cols, rows, cfg, t, f, motes))
    gif_name = f"{name}.gif"
    still_name = f"{name}.png"
    for dest in DESTS:
        os.makedirs(dest, exist_ok=True)
        out = os.path.join(dest, gif_name)
        shared = frames[0].quantize(colors=GIF_COLORS, method=Image.MEDIANCUT)
        p_frames = [fr.quantize(colors=GIF_COLORS, method=Image.MEDIANCUT, palette=shared, dither=Image.Dither.NONE) for fr in frames]
        p_frames[0].save(out, save_all=True, append_images=p_frames[1:],
                         duration=duration, loop=0, optimize=True)
        still = frames[int(((STILL_T - T_OFFSET) % 1.0) * n_frames)]
        still.save(os.path.join(dest, still_name), optimize=True)
        print(f"{gif_name:26s} {os.path.getsize(out) // 1024:5d} KB  ({cols}x{rows} cells, {n_frames}f)")


def main():
    # Banner — white wireframe-cut Sci-Fi CREATOR (alpha silhouette)
    build("creator-banner-ascii", os.path.join(SRC_DIR, "creator-scifi.png"),
          cols=140, cfg={"salt": 7, "beam_sigma": 0.052},
          n_frames=40, duration=55, motes_kind="rise", motes_n=22, seed=11)
    # Gear-letters CREATOR — neon teal on black (luminance cut)
    build("creator-gear-ascii", os.path.join(SRC_DIR, "creator-gear.png"),
          cols=132, cfg={"salt": 19, "beam_sigma": 0.058, "neon": True},
          n_frames=38, duration=55, motes_kind="rise", motes_n=24, seed=29)
    # The gear 'C' — neon strokes on black (luminance cut), comet arc orbit
    build("creator-logo-ascii", os.path.join(SRC_DIR, "icon-512.png"),
          cols=72, cfg={"salt": 31, "radial": True, "heartbeat": True, "neon": True, "cov_floor": 0.10},
          n_frames=44, duration=55, motes_kind="orbit", motes_n=10, seed=47)


if __name__ == "__main__":
    sys.exit(main())
