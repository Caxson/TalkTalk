// Unit tests for fitting a rendered head onto a photo. Run: node --test internal/web/static/test
import { test } from "node:test";
import assert from "node:assert/strict";
import { apply, coverFit, faceMask, similarity } from "../facealign.js";

const close = (p, q, tol = 1e-6) => assert.ok(Math.hypot(p.x - q.x, p.y - q.y) < tol, `${JSON.stringify(p)} vs ${JSON.stringify(q)}`);

test("similarity recovers scale, rotation and shift", () => {
  const src = [{ x: 10, y: 20 }, { x: 50, y: 22 }, { x: 30, y: 60 }];
  const s = 1.7, th = 0.2, tx = 33, ty = -12;
  const dst = src.map((p) => ({ x: s * (Math.cos(th) * p.x - Math.sin(th) * p.y) + tx, y: s * (Math.sin(th) * p.x + Math.cos(th) * p.y) + ty }));
  const m = similarity(src, dst);
  src.forEach((p, i) => close(apply(m, p), dst[i]));
  assert.ok(Math.abs(Math.hypot(m.a, m.b) - s) < 1e-9);
});

test("similarity is a least-squares fit for noisy anchors", () => {
  const src = [{ x: 0, y: 0 }, { x: 100, y: 0 }, { x: 50, y: 80 }];
  const dst = [{ x: 1, y: 0 }, { x: 99, y: 1 }, { x: 50, y: 79 }];
  const m = similarity(src, dst);
  src.forEach((p, i) => close(apply(m, p), dst[i], 2));
});

test("coverFit matches object-fit: cover", () => {
  const m = coverFit(900, 1600, 400, 500, 0.5, 0.25); // tall photo in a 4:5 frame
  assert.equal(m.a, 400 / 900);
  close(apply(m, { x: 450, y: 0 }), { x: 200, y: (500 - 1600 * m.a) * 0.25 });
});

test("faceMask centres on the face", () => {
  const css = faceMask([{ x: 40, y: 50 }, { x: 80, y: 50 }, { x: 60, y: 90 }]);
  assert.match(css, /^radial-gradient\(ellipse 50px 70px at 60px /);
});
