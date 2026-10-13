// Unit tests for feelings on the face. Run: node --test internal/web/static/test
import { test } from "node:test";
import assert from "node:assert/strict";
import { Mood } from "../affect.js";

const run = (m, t0, t1, speaking) => {
  let out;
  for (let t = t0; t <= t1; t += 1000 / 60) out = m.update(t, 1000 / 60, speaking);
  return out;
};

test("a feeling eases in within a few hundred ms", () => {
  const m = new Mood();
  m.feel("happy", 0);
  const early = run(m, 0, 50, true);
  const later = run(m, 50, 600, true);
  assert.ok(early.mouthSmileLeft < 0.3, "not instant");
  assert.ok(later.mouthSmileLeft > 0.5, "fully shown by ~0.6 s");
});

test("held while speaking, lingers, then relaxes", () => {
  const m = new Mood();
  m.feel("surprised", 0);
  run(m, 0, 5000, true);
  const lingering = run(m, 5000, 6500, false);
  assert.ok(lingering.browInnerUp > 0.6, "still shown 1.5 s after speech");
  const relaxed = run(m, 6500, 11000, false);
  assert.ok((relaxed.browInnerUp ?? 0) < 0.01, "relaxed a few seconds after the linger");
});

test("switching feelings cross-fades; neutral relaxes", () => {
  const m = new Mood();
  m.feel("happy", 0);
  run(m, 0, 1000, true);
  m.feel("sad", 1000);
  const mid = run(m, 1000, 1100, true);
  assert.ok(mid.mouthSmileLeft > 0 && mid.mouthFrownLeft > 0, "both during the switch");
  const after = run(m, 1100, 4000, true);
  assert.ok(after.mouthFrownLeft > 0.4 && (after.mouthSmileLeft ?? 0) < 0.01);
  m.feel("neutral", 4000);
  assert.deepEqual(run(m, 4000, 9000, true), {});
});

test("unknown names are neutral", () => {
  const m = new Mood();
  m.feel("smug", 0);
  assert.deepEqual(run(m, 0, 1000, true), {});
});

test("pose follows the feeling and relaxes with it", () => {
  const m = new Mood();
  m.feel("thinking", 0);
  run(m, 0, 800, true);
  const p = m.pose();
  assert.ok(p.roll > 0.2 && p.pitch > 0.1, "head tilts and looks up while thinking");
  m.feel("neutral", 800);
  run(m, 800, 6000, true);
  const q = m.pose();
  assert.ok(Math.abs(q.roll) < 0.01 && Math.abs(q.pitch) < 0.01);
});
