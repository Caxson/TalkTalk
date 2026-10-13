// Unit tests for the avatar's reflexes. Run: node --test internal/web/static/test
import { test } from "node:test";
import assert from "node:assert/strict";
import { Behavior } from "../behavior.js";

// run drives the behavior at 60 fps from t0 to t1 (ms) with fixed inputs.
function run(b, t0, t1, { state = "listening", reading = null, mic = 0 } = {}) {
  let out;
  for (let t = t0; t <= t1; t += 1000 / 60) {
    const r = reading ? { ...reading, at: t } : null;
    out = b.update(t, 1000 / 60, state, r, typeof mic === "function" ? mic(t) : mic);
  }
  return out;
}

const face = (o = {}) => ({ face: true, smile: 0, cx: 0.5, cy: 0.45, ...o });

test("answers a smile after a human beat, and eases off", () => {
  const b = new Behavior();
  run(b, 0, 1000, { reading: face() });
  let out = run(b, 1000, 1300, { reading: face({ smile: 0.9 }) });
  assert.ok(out.smile < 0.1, `smiled back within 300 ms: ${out.smile}`);
  out = run(b, 1300, 2500, { reading: face({ smile: 0.9 }) });
  assert.ok(out.smile > 0.8, `did not smile back: ${out.smile}`);
  out = run(b, 2500, 4500, { reading: face({ smile: 0 }) });
  assert.ok(out.smile < 0.1, `kept smiling: ${out.smile}`);
});

test("a faint smile is not mirrored", () => {
  const b = new Behavior();
  const out = run(b, 0, 3000, { reading: face({ smile: 0.2 }) });
  assert.equal(out.smile, 0);
});

test("looks toward the face, mirrored as the user sees the screen", () => {
  const b = new Behavior();
  // Face on the left of the raw camera image = user to their right = screen right.
  let out = run(b, 0, 1500, { reading: face({ cx: 0.2 }) });
  assert.ok(out.gaze.x > 0.4, `gaze.x ${out.gaze.x}`);
  out = run(b, 1500, 3000, { reading: face({ cx: 0.8 }) });
  assert.ok(out.gaze.x < -0.4, `gaze.x ${out.gaze.x}`);
  out = run(b, 3000, 4500, {});
  assert.ok(Math.abs(out.gaze.x) < 0.05, `no face: gaze should return to centre, got ${out.gaze.x}`);
});

test("stale readings count as no face", () => {
  const b = new Behavior();
  let out;
  for (let t = 0; t < 2000; t += 16) out = b.update(t, 16, "listening", { ...face({ smile: 0.9, cx: 0.1 }), at: 0 }, 0);
  assert.equal(out.present, false);
  assert.ok(out.smile < 0.05 && Math.abs(out.gaze.x) < 0.05);
});

test("nods once into a short pause while the user talks", () => {
  const b = new Behavior();
  // talk 0-2 s, pause 2-2.5 s, talk again
  const mic = (t) => (t < 2000 || t > 2500 ? 0.6 : 0.02);
  let maxNod = 0, nods = 0, prev = 0;
  for (let t = 0; t < 3500; t += 1000 / 60) {
    const o = b.update(t, 1000 / 60, "listening", null, mic(t));
    if (o.nod > 0 && prev === 0) nods++;
    prev = o.nod;
    maxNod = Math.max(maxNod, o.nod);
  }
  assert.equal(nods, 1);
  assert.ok(maxNod > 0.08, `nod too small: ${maxNod}`);
});

test("no nods while the assistant speaks, or in long silences", () => {
  const b = new Behavior();
  let any = false;
  for (let t = 0; t < 3000; t += 16) any ||= b.update(t, 16, "speaking", null, t < 1000 ? 0.6 : 0).nod > 0;
  for (let t = 3000; t < 9000; t += 16) any ||= b.update(t, 16, "listening", null, 0).nod > 0;
  assert.equal(any, false);
});

test("nods are rate-limited", () => {
  const b = new Behavior();
  // Rapid talk/pause cycles every 900 ms for 6 s.
  const mic = (t) => (t % 900 < 500 ? 0.6 : 0.02);
  let nods = 0, prev = 0;
  for (let t = 0; t < 6000; t += 1000 / 60) {
    const o = b.update(t, 1000 / 60, "listening", null, mic(t));
    if (o.nod > 0 && prev === 0) nods++;
    prev = o.nod;
  }
  assert.ok(nods >= 1 && nods <= 2, `nods = ${nods} in 6 s`);
});

test("eyes settle while the user talks", () => {
  const b = new Behavior();
  assert.ok(run(b, 0, 500, { mic: 0.6 }).saccadeScale < 0.5);
  assert.equal(run(b, 500, 3000, { mic: 0 }).saccadeScale, 1);
});
