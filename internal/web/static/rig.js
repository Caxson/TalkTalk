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
// radians) and pose (head roll and pitch, -1..1) onto Cubism's standard
// parameters, following open trackers (ExARLive2D, Heonys/live2d-web).
// Values are relative: 0 is the model's own default and ±1 its range in
// that direction (toCubism converts), because models differ: Hiyori's
// mouth, for one, rests at a smile that is the top of its range.
export function live2d(e, b = {}, pose = {}) {
  const g = (k) => e[k] ?? 0;
  const smile = Math.max((g("mouthSmileLeft") + g("mouthSmileRight")) / 2, b.smile ?? 0);
  const frown = (g("mouthFrownLeft") + g("mouthFrownRight")) / 2;
  const gx = (g("eyeLookOutLeft") - g("eyeLookInLeft") + g("eyeLookInRight") - g("eyeLookOutRight")) / 2 + (b.gaze?.x ?? 0);
  const gy = (g("eyeLookUpLeft") + g("eyeLookUpRight") - g("eyeLookDownLeft") - g("eyeLookDownRight")) / 2 + (b.gaze?.y ?? 0);
  // A Cubism smile-eye is an arc that shows as the eye closes, so a smile
  // both raises EyeSmile and lowers EyeOpen (the Duchenne squint).
  const eyeSmile = clamp01(Math.max(smile, 1.4 * Math.max(g("cheekSquintLeft"), g("cheekSquintRight"))));
  const eyeOpen = (side) => clamp(-g(`eyeBlink${side}`) - 0.6 * eyeSmile - 0.3 * g(`eyeSquint${side}`) + g(`eyeWide${side}`), -1, 1);
  // Brows: inner ends up reads as worry, both down as concentration, and
  // a smile softens their shape.
  const browAngle = (side) => clamp(g(`browOuterUp${side}`) - g("browInnerUp") + 0.5 * g(`browDown${side}`), -1, 1);
  const browForm = (side) => clamp(0.5 * smile - 0.6 * g("browInnerUp") - 0.6 * g(`browDown${side}`), -1, 1);
  const unit = (rad) => clamp(rad / (Math.PI / 6), -1, 1); // ±30° is a model's usual head range
  return {
    ParamEyeLOpen: eyeOpen("Left"),
    ParamEyeROpen: eyeOpen("Right"),
    ParamEyeLSmile: eyeSmile,
    ParamEyeRSmile: eyeSmile,
    ParamEyeBallX: clamp(gx, -1, 1),
    ParamEyeBallY: clamp(gy, -1, 1),
    ParamBrowLY: clamp(Math.max(g("browInnerUp"), g("browOuterUpLeft")) - g("browDownLeft"), -1, 1),
    ParamBrowRY: clamp(Math.max(g("browInnerUp"), g("browOuterUpRight")) - g("browDownRight"), -1, 1),
    ParamBrowLAngle: browAngle("Left"),
    ParamBrowRAngle: browAngle("Right"),
    ParamBrowLForm: browForm("Left"),
    ParamBrowRForm: browForm("Right"),
    ParamMouthOpenY: clamp01(1.4 * Math.max(g("jawOpen"), g("mouthFunnel") * 0.65)),
    ParamMouthForm: clamp(smile - frown - g("mouthPucker") * 0.5, -1, 1),
    ParamCheek: clamp01(Math.max(smile * 0.8, 1.5 * g("cheekSquintLeft")) + g("cheekPuff")),
    ParamAngleX: unit(b.headYaw ?? 0),
    ParamAngleY: clamp(unit(-(b.nod ?? 0)) + (pose.pitch ?? 0), -1, 1),
    ParamAngleZ: clamp(pose.roll ?? 0, -1, 1),
  };
}

// toCubism turns a relative value (0 = default, ±1 = the range's ends)
// into a model's own units, given its [min, default, max].
export function toCubism(v, [min, def, max]) {
  return v < 0 ? def + v * (def - min) : def + v * (max - def);
}
