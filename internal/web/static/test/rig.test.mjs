// Unit tests for the ARKit → avatar mappings. Run: node --test internal/web/static/test
import { test } from "node:test";
import assert from "node:assert/strict";
import { live2d, perfectSync, toCubism, vowels } from "../rig.js";

test("vowels: open jaw is aa, sum stays ≤ 1", () => {
  assert.equal(vowels({}).aa, 0);
  assert.equal(vowels({ jawOpen: 0.5 }).aa, 1);
  const v = vowels({ jawOpen: 0.5, mouthSmileLeft: 1, mouthSmileRight: 1, mouthPucker: 1, mouthFunnel: 1 });
  assert.ok(v.aa + v.ih + v.ou <= 1 + 1e-9);
});

test("perfectSync: recognises ARKit expressions in any case", () => {
  const arkit = ["JawOpen", "EyeBlinkLeft", "MouthSmileLeft", "BrowInnerUp", "MouthPucker", "MouthFunnel", "CheekPuff", "MouthRollUpper", "happy"];
  const idx = perfectSync(arkit);
  assert.ok(idx);
  assert.equal(idx.get("jawopen"), "JawOpen");
  assert.equal(perfectSync(["aa", "ih", "ou", "ee", "oh", "blink", "happy"]), null);
});

test("live2d: neutral face is the model's own default", () => {
  const p = live2d({});
  assert.equal(p.ParamEyeLOpen, 0);
  assert.equal(p.ParamMouthForm, 0);
  assert.equal(p.ParamMouthOpenY, 0);
  assert.equal(p.ParamEyeBallX, 0);
  assert.equal(p.ParamAngleX, 0);
});

test("live2d: jaw opens the mouth, blink closes the eye, smile curves the mouth", () => {
  const p = live2d({ jawOpen: 0.5, eyeBlinkLeft: 1, mouthSmileLeft: 0.8, mouthSmileRight: 0.8 });
  assert.ok(p.ParamMouthOpenY > 0.6);
  assert.equal(p.ParamEyeLOpen, -1);
  assert.ok(p.ParamEyeROpen < -0.3 && p.ParamEyeROpen > -1, "a smile narrows the eye into its arc");
  assert.ok(p.ParamEyeRSmile > 0.7);
  assert.ok(p.ParamMouthForm > 0.5);
});

test("live2d: gaze, head and pose follow behavior, clamped", () => {
  const p = live2d({ eyeLookOutLeft: 1, eyeLookInRight: 1 }, { gaze: { x: 0.5, y: 0 }, headYaw: Math.PI, nod: 0.1 }, { roll: 0.3, pitch: -0.2 });
  assert.equal(p.ParamEyeBallX, 1);
  assert.equal(p.ParamAngleX, 1);
  assert.ok(p.ParamAngleY < -0.2);
  assert.equal(p.ParamAngleZ, 0.3);
  for (const v of Object.values(p)) assert.ok(Number.isFinite(v));
});

test("toCubism: relative values land in each model's own range", () => {
  const mouthForm = [-2, 1, 1]; // Hiyori: rests at a smile, the top of its range
  assert.equal(toCubism(0, mouthForm), 1);
  assert.equal(toCubism(1, mouthForm), 1);
  assert.equal(toCubism(-1, mouthForm), -2);
  const eyeOpen = [0, 1, 1.2];
  assert.equal(toCubism(-1, eyeOpen), 0);
  assert.ok(Math.abs(toCubism(1, eyeOpen) - 1.2) < 1e-9);
  assert.equal(toCubism(0.5, [-30, 0, 30]), 15);
});
