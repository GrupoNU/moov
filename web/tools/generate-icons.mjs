/*
 * Rasterizes Moov's mark to PNG with no dependencies.
 *
 * The mark is simple enough to rasterize analytically: a rounded-rect plate and
 * a stroked polyline "M". Both are signed-distance functions, sampled 4x4 per
 * pixel for antialiasing. Node's built-in zlib does the PNG compression, so
 * this needs no canvas, no sharp, no resvg — which is the whole point.
 */

import { deflateSync } from "node:zlib";
import { writeFileSync } from "node:fs";
import { argv } from "node:process";

const BRAND = [0x5b, 0x5b, 0xd6];
const WHITE = [0xff, 0xff, 0xff];

// --- geometry, in the favicon's 32x32 user space -------------------------

const VB = 32;
const PLATE_RADIUS = 8.5;
const STROKE_W = 2.6;

/*
 * The "M" polyline, read off the favicon path.
 *
 * favicon.svg draws: M9,22 V11.4, then a curve down-right to the valley, then
 * a curve up-right to the second peak, then V22. The two curves share their
 * endpoint at the valley — that shared low point is what makes it an M rather
 * than an N, and getting it wrong is exactly the bug the first render had.
 *
 * The valley y is 11.4 + 6.6 = 18 (the `l 5.4 6.6` of the path's first curve);
 * x is 9 + 5.4 = 14.4, and the second peak mirrors it at 14.4 + 5.4 = 19.8.
 * The favicon rounds the two apexes with short curves; at icon sizes a
 * round-joined polyline is within a pixel of that, so the joins do the work.
 */
const M_RAW = [
  [9, 22],
  [9, 11.4],
  [14.4, 18],
  [19.8, 11.4],
  [19.8, 22],
];

/*
 * Centred horizontally. The raw path spans x 9..19.8, whose midpoint is 14.4 —
 * not the plate's 16 — so drawn as-authored the mark sits visibly left of
 * centre. The favicon gets away with it at 16px; at 512px on a launcher it
 * reads as a mistake. Shifting by the difference keeps the SAME geometry and
 * only fixes where it sits.
 */
const M_SHIFT = VB / 2 - (9 + 19.8) / 2;
const M_POINTS = M_RAW.map(([x, y]) => [x + M_SHIFT, y]);

/** Distance from p to the segment ab. */
function segDist(px, py, ax, ay, bx, by) {
  const vx = bx - ax;
  const vy = by - ay;
  const wx = px - ax;
  const wy = py - ay;
  const len2 = vx * vx + vy * vy;
  let t = len2 === 0 ? 0 : (wx * vx + wy * vy) / len2;
  t = Math.max(0, Math.min(1, t));
  const dx = px - (ax + t * vx);
  const dy = py - (ay + t * vy);
  return Math.hypot(dx, dy);
}

/** Signed distance to the M stroke's centreline (negative = inside stroke). */
function strokeSdf(x, y) {
  let best = Infinity;
  for (let i = 0; i + 1 < M_POINTS.length; i += 1) {
    const [ax, ay] = M_POINTS[i];
    const [bx, by] = M_POINTS[i + 1];
    best = Math.min(best, segDist(x, y, ax, ay, bx, by));
  }
  // Round caps and joins fall out of the distance field for free.
  return best - STROKE_W / 2;
}

/** Signed distance to a rounded rect covering [x0,x1] x [y0,y1] with radius r. */
function roundRectSdf(x, y, x0, y0, x1, y1, r) {
  const cx = (x0 + x1) / 2;
  const cy = (y0 + y1) / 2;
  const hx = (x1 - x0) / 2 - r;
  const hy = (y1 - y0) / 2 - r;
  const qx = Math.abs(x - cx) - hx;
  const qy = Math.abs(y - cy) - hy;
  return Math.hypot(Math.max(qx, 0), Math.max(qy, 0)) + Math.min(Math.max(qx, qy), 0) - r;
}

/**
 * Renders one icon.
 *
 * `inset` is the fraction of the canvas the plate leaves empty on each side.
 * For the "any" purpose it is 0 (edge to edge, the classic app-icon look).
 * For "maskable" it is sized so the whole mark lives inside the 40%-radius
 * safe circle a launcher may crop to — which is what maskable actually
 * requires, and why a maskable icon is not just the same file renamed.
 */
function render(size, { maskable, opaque = false }) {
  const SS = 4; // supersampling factor per axis
  const px = Buffer.alloc(size * size * 4);

  // The maskable safe zone is a centred circle of diameter 80% of the icon.
  // The plate is inscribed in it: a square inscribed in a circle of radius R
  // has half-side R/sqrt(2), so the plate spans 80%/sqrt(2) ~= 56.5% of the
  // canvas. Rounded corners give a little back, so 62% is safe and less mean.
  const plateFrac = maskable ? 0.62 : 1;
  const plateSize = VB * plateFrac;
  const plateOffset = (VB - plateSize) / 2;
  const scaleToPlate = plateSize / VB;

  for (let y = 0; y < size; y += 1) {
    for (let x = 0; x < size; x += 1) {
      let plateCov = 0;
      let strokeCov = 0;
      for (let sy = 0; sy < SS; sy += 1) {
        for (let sx = 0; sx < SS; sx += 1) {
          // Sample point in viewBox space.
          const ux = ((x + (sx + 0.5) / SS) / size) * VB;
          const uy = ((y + (sy + 0.5) / SS) / size) * VB;
          // Map into the plate's own 0..32 space.
          const mx = (ux - plateOffset) / scaleToPlate;
          const my = (uy - plateOffset) / scaleToPlate;

          if (roundRectSdf(mx, my, 0, 0, VB, VB, PLATE_RADIUS) <= 0) plateCov += 1;
          if (strokeSdf(mx, my) <= 0) strokeCov += 1;
        }
      }
      const total = SS * SS;
      const plateA = plateCov / total;
      const strokeA = strokeCov / total;

      // Composite: white stroke over brand plate, the whole thing over
      // transparency. Straight (non-premultiplied) alpha, as PNG wants.
      const alpha = plateA;
      let r = BRAND[0];
      let g = BRAND[1];
      let b = BRAND[2];
      if (strokeA > 0 && alpha > 0) {
        // The stroke only paints where the plate is; clip it so an antialiased
        // stroke never bleeds outside the plate edge.
        const s = Math.min(strokeA, alpha) / alpha;
        r = Math.round(BRAND[0] * (1 - s) + WHITE[0] * s);
        g = Math.round(BRAND[1] * (1 - s) + WHITE[1] * s);
        b = Math.round(BRAND[2] * (1 - s) + WHITE[2] * s);
      }

      const o = (y * size + x) * 4;
      if (opaque && alpha < 1) {
        // iOS ignores alpha in an apple-touch-icon and composites the corners
        // on black, so a transparent-cornered file gets black corners. Filling
        // the plate's own colour behind it keeps the rounded look wherever
        // alpha IS honoured and stays on-brand where it is not.
        px[o] = Math.round(r * alpha + BRAND[0] * (1 - alpha));
        px[o + 1] = Math.round(g * alpha + BRAND[1] * (1 - alpha));
        px[o + 2] = Math.round(b * alpha + BRAND[2] * (1 - alpha));
        px[o + 3] = 255;
        continue;
      }
      px[o] = r;
      px[o + 1] = g;
      px[o + 2] = b;
      px[o + 3] = Math.round(alpha * 255);
    }
  }
  return px;
}

// --- minimal PNG encoder --------------------------------------------------

function crc32(buf) {
  let c;
  const table = crc32.table ?? (crc32.table = (() => {
    const t = new Int32Array(256);
    for (let n = 0; n < 256; n += 1) {
      c = n;
      for (let k = 0; k < 8; k += 1) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
      t[n] = c;
    }
    return t;
  })());
  let crc = -1;
  for (let i = 0; i < buf.length; i += 1) crc = table[(crc ^ buf[i]) & 0xff] ^ (crc >>> 8);
  return (crc ^ -1) >>> 0;
}

function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length, 0);
  const typeBuf = Buffer.from(type, "ascii");
  const crcBuf = Buffer.alloc(4);
  crcBuf.writeUInt32BE(crc32(Buffer.concat([typeBuf, data])), 0);
  return Buffer.concat([len, typeBuf, data, crcBuf]);
}

function encodePng(size, rgba) {
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // colour type: RGBA
  ihdr[10] = 0; // deflate
  ihdr[11] = 0; // adaptive filtering
  ihdr[12] = 0; // no interlace

  // Filter type 0 (None) on every scanline. The images are flat colour over
  // large areas, so deflate alone gets them small; per-line filter selection
  // would add code for a few hundred bytes.
  const stride = size * 4;
  const raw = Buffer.alloc((stride + 1) * size);
  for (let y = 0; y < size; y += 1) {
    raw[y * (stride + 1)] = 0;
    rgba.copy(raw, y * (stride + 1) + 1, y * stride, (y + 1) * stride);
  }

  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(raw, { level: 9 })),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

// --- main ------------------------------------------------------------------

const outDir = argv[2];
if (!outDir) {
  console.error("usage: node gen-icons.mjs <output-dir>");
  process.exit(1);
}

const targets = [
  ["icon-192.png", 192, { maskable: false }],
  ["icon-512.png", 512, { maskable: false }],
  ["icon-maskable-192.png", 192, { maskable: true }],
  ["icon-maskable-512.png", 512, { maskable: true }],
  ["apple-touch-icon.png", 180, { maskable: false, opaque: true }],
];

for (const [name, size, opts] of targets) {
  const png = encodePng(size, render(size, opts));
  writeFileSync(`${outDir}/${name}`, png);
  console.log(`${name}: ${size}x${size}, ${png.length} bytes`);
}
