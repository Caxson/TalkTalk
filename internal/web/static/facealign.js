// Fitting a rendered head onto a photo of the same face. Both pictures go
// through the face landmarker; three anchors (eye centres, mouth centre)
// give a similarity transform (scale, rotation, shift) from the render to
// the photo. Pure apart from detection, so the geometry is testable.

// MediaPipe face mesh indices: eye corners and inner lips.
const LEFT_EYE = [33, 133];
const RIGHT_EYE = [362, 263];
const MOUTH = [13, 14];

const mid = (lm, [a, b], w, h) => ({ x: ((lm[a].x + lm[b].x) / 2) * w, y: ((lm[a].y + lm[b].y) / 2) * h });

// anchors returns the three anchor points in pixels for normalized landmarks.
export function anchors(lm, w, h) {
  return [mid(lm, LEFT_EYE, w, h), mid(lm, RIGHT_EYE, w, h), mid(lm, MOUTH, w, h)];
}

// similarity fits dst ≈ s·R(θ)·src + t by least squares (Umeyama, 2D) and
// returns it as a CSS-style matrix { a, b, c, d, e, f }: x' = a·x + c·y + e,
// y' = b·x + d·y + f.
export function similarity(src, dst) {
  const n = src.length;
  const mean = (ps) => ps.reduce((m, p) => ({ x: m.x + p.x / n, y: m.y + p.y / n }), { x: 0, y: 0 });
  const ms = mean(src), md = mean(dst);
  let sxx = 0, sxy = 0, var_ = 0;
  for (let i = 0; i < n; i++) {
    const ax = src[i].x - ms.x, ay = src[i].y - ms.y;
    const bx = dst[i].x - md.x, by = dst[i].y - md.y;
    sxx += ax * bx + ay * by;  // cos part
    sxy += ax * by - ay * bx;  // sin part
    var_ += ax * ax + ay * ay;
  }
  const a = sxx / var_, b = sxy / var_; // s·cosθ, s·sinθ
  return { a, b, c: -b, d: a, e: md.x - (a * ms.x - b * ms.y), f: md.y - (b * ms.x + a * ms.y) };
}

export function apply(m, p) {
  return { x: m.a * p.x + m.c * p.y + m.e, y: m.b * p.x + m.d * p.y + m.f };
}

// coverFit maps a picture's pixels to an element showing it with
// object-fit: cover at the given object-position (fractions).
export function coverFit(iw, ih, ew, eh, px = 0.5, py = 0.5) {
  const s = Math.max(ew / iw, eh / ih);
  return { a: s, b: 0, c: 0, d: s, e: (ew - iw * s) * px, f: (eh - ih * s) * py };
}

// faceMask is a CSS mask showing an ellipse around the face (in the
// layer's own pixels), feathered so the render melts into the photo.
export function faceMask([le, re, mouth]) {
  const eye = Math.hypot(re.x - le.x, re.y - le.y);
  const cx = (le.x + re.x + mouth.x) / 3, cy = (le.y + re.y) / 2 * 0.6 + mouth.y * 0.4 - 0.15 * eye;
  return `radial-gradient(ellipse ${1.25 * eye}px ${1.75 * eye}px at ${cx}px ${cy}px, #000 62%, transparent 100%)`;
}
