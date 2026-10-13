import { test } from "node:test";
import assert from "node:assert/strict";
import { mouthActive } from "../perception.js";

test("a still, closed mouth is quiet", () => {
  assert.equal(mouthActive([0.01, 0.02, 0.015, 0.02, 0.01]), false);
  assert.equal(mouthActive([0.01, 0.02, 0.015, 0.02], [0.03, 0.02, 0.03, 0.02]), false);
});

test("a moving jaw is active", () => {
  assert.equal(mouthActive([0.01, 0.07, 0.02, 0.09, 0.03]), true);
});

test("a mouth held open is not treated as quiet", () => {
  assert.equal(mouthActive([0.3, 0.31, 0.3]), true);
});

test("too few samples is unknown, treated as active (never vetoes)", () => {
  assert.equal(mouthActive([0.01]), true);
  assert.equal(mouthActive([0.01, 0.01, 0.01]), true);
});

test("lip-only articulation counts", () => {
  // jaw barely moves, lips pucker and round: "u", "wu"
  assert.equal(mouthActive([0.02, 0.02, 0.03, 0.02], [0.02, 0.18, 0.04, 0.2]), true);
});

import { perceptionMessage } from "../perception.js";

test("only fresh readings claim a face", () => {
  const r = { face: true, mouth: false, at: 1000 };
  assert.deepEqual(perceptionMessage(r, 1100), { type: "perception", face: true, mouth: false });
  // Background tab or frozen camera: a stale reading says nothing about now.
  assert.deepEqual(perceptionMessage(r, 1600), { type: "perception", face: false, mouth: false });
  assert.deepEqual(perceptionMessage(null, 1600), { type: "perception", face: false, mouth: false });
});
