// Rigging: how one ARKit-52 face stream drives each kind of avatar. Pure
// functions, so the mappings are testable without a renderer.

const clamp = (x, lo, hi) => Math.min(hi, Math.max(lo, x));
const clamp01 = (x) => clamp(x, 0, 1);

// clamp01((x - 0.15) / 0.45): VMagicMirror's dead zone and full-scale.
const mapClamp = (x) => clamp01((x - 0.15) / 0.45);

// vowels maps ARKit weights to VRM's aa/ih/ou (VMagicMirror, MIT), keeping
// their sum ≤ 1.
export function vowels(e) {
  const g = (k) => e[k] ?? 0;
  let aa = mapClamp(2.0 * g("jawOpen"));
  let ih = mapClamp(0.6 * (g("mouthSmileLeft") + g("mouthSmileRight")) - 0.1);
  let ou = mapClamp(0.4 * (g("mouthPucker") + g("mouthFunnel") + g("jawForward")));
  const sum = aa + ih + ou;
  if (sum > 1) { aa /= sum; ih /= sum; ou /= sum; }
  return { aa, ih, ou };
}

// Shapes a Perfect Sync model must have; a model with these is driven by
// ARKit weights directly.
const PERFECT_SYNC_PROBE = ["jawopen", "eyeblinkleft", "mouthsmileleft", "browinnerup", "mouthpucker", "mouthfunnel", "cheekpuff", "mouthrollupper"];

// perfectSync indexes a model's expressions by lower-cased name when it
// has the ARKit set (authors capitalise them variously: EyeBlinkLeft,
// eyeBlinkLeft), or returns null for an ordinary VRM.
export function perfectSync(expressionNames) {
  const index = new Map(expressionNames.map((n) => [n.toLowerCase(), n]));
  return PERFECT_SYNC_PROBE.every((n) => index.has(n)) ? index : null;
}

// live2d maps ARKit weights plus behavior (smile, gaze, nod, head yaw in
// radians) onto Cubism's standard parameters, following the formulas of
// open trackers (ExARLive2D, Heonys/live2d-web). The mouth is boosted a
// little: Live2D mouths read small at avatar size.
export function live2d(e, b = {}) {
  const g = (k) => e[k] ?? 0;
  const smile = Math.max((g("mouthSmileLeft") + g("mouthSmileRight")) / 2, b.smile ?? 0);
  const frown = (g("mouthFrownLeft") + g("mouthFrownRight")) / 2;
  const gx = (g("eyeLookOutLeft") - g("eyeLookInLeft") + g("eyeLookInRight") - g("eyeLookOutRight")) / 2 + (b.gaze?.x ?? 0);
  const gy = (g("eyeLookUpLeft") + g("eyeLookUpRight") - g("eyeLookDownLeft") - g("eyeLookDownRight")) / 2 + (b.gaze?.y ?? 0);
  // A smile narrows the eyes (the Duchenne squint).
  const squint = (side) => Math.max(g(`eyeSquint${side}`), smile * 0.35);
  const deg = (rad) => clamp((rad * 180) / Math.PI, -30, 30);
  return {
    ParamEyeLOpen: clamp01(1 - g("eyeBlinkLeft") - squint("Left") * 0.4 + g("eyeWideLeft") * 0.2),
    ParamEyeROpen: clamp01(1 - g("eyeBlinkRight") - squint("Right") * 0.4 + g("eyeWideRight") * 0.2),
    ParamEyeLSmile: clamp01(smile),
    ParamEyeRSmile: clamp01(smile),
    ParamEyeBallX: clamp(gx, -1, 1),
    ParamEyeBallY: clamp(gy, -1, 1),
    ParamBrowLY: clamp(Math.max(g("browInnerUp"), g("browOuterUpLeft")) - g("browDownLeft"), -1, 1),
    ParamBrowRY: clamp(Math.max(g("browInnerUp"), g("browOuterUpRight")) - g("browDownRight"), -1, 1),
    ParamMouthOpenY: clamp01(1.4 * Math.max(g("jawOpen"), g("mouthFunnel") * 0.65)),
    ParamMouthForm: clamp(smile - frown - g("mouthPucker") * 0.5, -1, 1),
    ParamCheek: clamp01(smile * 0.8 + g("cheekPuff")),
    ParamAngleX: deg(b.headYaw ?? 0),
    ParamAngleY: deg(-(b.nod ?? 0)),
  };
}
