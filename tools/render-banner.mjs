// Capture frames from the Braille Shockwave canvas for tools/assemble-banner.py.
//
// The landing page drives its animation off requestAnimationFrame with a real
// clock, so we inject a virtual 20 fps clock (50 ms per rAF) before any page
// script runs and snapshot canvas.toDataURL('image/png') on every frame. Each
// frame is exactly 50 ms of the page's own animTime - fully deterministic and
// at the full animation speed, not slow-motion headless capture.
// The canvas has a transparent background; toDataURL preserves the alpha.
//
// Requirements: node, puppeteer-core (npm i puppeteer-core), Microsoft Edge.
// Usage: node tools/render-banner.mjs <input.html> [outDir] [frameCount]
//   <input.html>  path to the landing-page HTML (docs/index.html)
//   [outDir]      frame output directory (default: banner-frames)
//   [frameCount]  frames at 20 fps (default: 240 = 12 s)
import { launch } from 'puppeteer-core';
import { writeFileSync, mkdirSync, rmSync, existsSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const [, , inputArg, outDirArg, frameArg] = process.argv;
if (!inputArg) {
  console.error('usage: node tools/render-banner.mjs <input.html> [outDir] [frameCount]');
  process.exit(1);
}
const input = resolve(inputArg);
const outDir = resolve(outDirArg || 'banner-frames');
const frameCount = parseInt(frameArg || '240', 10);
const FRAME_MS = 50; // 20 fps virtual clock

rmSync(outDir, { recursive: true, force: true });
mkdirSync(outDir, { recursive: true });

const edgeCandidates = [
  'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
  'C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe',
  '/usr/bin/microsoft-edge',
  '/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge',
];
const executablePath = process.env.EDGE_PATH || edgeCandidates.find(existsSync);
if (!executablePath) {
  console.error('Microsoft Edge not found; set EDGE_PATH to its executable.');
  process.exit(1);
}

const browser = await launch({
  executablePath,
  headless: 'new',
  protocolTimeout: 900000,
  args: ['--force-color-profile=srgb', '--hide-scrollbars'],
});
const page = await browser.newPage();
// The canvas lays out at 100vw / aspect-ratio 1816x344 at dpr 1.
await page.setViewport({ width: 1816, height: 344, deviceScaleFactor: 1 });

await page.evaluateOnNewDocument((frameMs, total) => {
  window.__frames = [];
  let virtual = 0;
  let n = 0;
  const orig = window.requestAnimationFrame.bind(window);
  window.requestAnimationFrame = (cb) => orig(() => {
    virtual += frameMs;
    n += 1;
    cb(virtual);
    const c = document.getElementById('dotCanvas');
    if (c && window.__frames.length < total) window.__frames.push(c.toDataURL('image/png'));
  });
}, FRAME_MS, frameCount);

await page.goto(pathToFileURL(input).href, { waitUntil: 'load', timeout: 60000 });
await page.waitForSelector('#dotCanvas', { timeout: 10000 });

let written = 0;
while (written < frameCount) {
  await page.waitForFunction(
    (w, total) => window.__frames.length >= Math.min(w + 8, total),
    { timeout: 900000, polling: 200 },
    written, frameCount
  );
  const batch = await page.evaluate((w, total) =>
    window.__frames.splice(0, Math.min(8, total - w)), written, frameCount);
  for (const dataUrl of batch) {
    const b64 = dataUrl.replace(/^data:image\/png;base64,/, '');
    writeFileSync(join(outDir, `frame_${String(written).padStart(4, '0')}.png`), Buffer.from(b64, 'base64'));
    written += 1;
  }
  process.stdout.write(`\r${written}/${frameCount}`);
}
console.log('\ndone');
await browser.close();
