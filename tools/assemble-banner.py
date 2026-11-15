#!/usr/bin/env python3
"""Assemble the README banner WebP from the frames tools/render-banner.mjs captures.

Every 2nd captured frame (20 fps -> 10 fps) goes into a looping animated WebP
with full alpha at the canvas's native 1816x344. Requires Pillow.

Usage: python tools/assemble-banner.py [framesDir] [outFile]
  [framesDir]  directory of frame_*.png from render-banner.mjs (default: banner-frames)
  [outFile]    output WebP (default: docs/assets/creator-banner.webp)
"""
import os
import sys
import time

from PIL import Image

frames_dir = sys.argv[1] if len(sys.argv) > 1 else "banner-frames"
out_file = sys.argv[2] if len(sys.argv) > 2 else os.path.join("docs", "assets", "creator-banner.webp")

names = sorted(f for f in os.listdir(frames_dir) if f.startswith("frame_") and f.endswith(".png"))
names = names[::2]  # 20 fps -> 10 fps
if not names:
    sys.exit(f"no frame_*.png in {frames_dir}")

t0 = time.time()
frames = [Image.open(os.path.join(frames_dir, f)) for f in names]
os.makedirs(os.path.dirname(out_file), exist_ok=True)
frames[0].save(
    out_file,
    save_all=True,
    append_images=frames[1:],
    duration=100,  # ms per frame -> 10 fps playback for 120 frames
    loop=0,
    quality=60,
    method=4,
)
print(f"wrote {out_file}: {len(frames)} frames, {os.path.getsize(out_file) / 1e6:.2f} MB, {time.time() - t0:.1f}s")
