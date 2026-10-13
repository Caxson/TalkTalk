// 2D anime renderer: a Live2D Cubism model (untitled-pixi-live2d-engine on
// PixiJS 8). The model keeps its own idle motions, breathing and physics;
// each frame the ARKit stream is mapped onto Cubism's standard parameters
// (rig.js) on top of them, so speech and reactions ride on a living body.

import { live2d as rig, toCubism } from "./rig.js";

// Head-and-shoulders still: the model is drawn this many frame-heights
// tall, hanging from just above the frame's top edge.
const FRAMING = { height: 2.5, top: -0.04 };
// Parameters the motions also animate: ours are added to theirs.
const ADDITIVE = new Set(["ParamBrowLY", "ParamBrowRY", "ParamBrowLAngle", "ParamBrowRAngle", "ParamBrowLForm", "ParamBrowRForm", "ParamAngleZ"]);
// Head turn and nod go through the engine's focus, which it applies before
// physics, so hair and ribbons swing with them (focus 1 = 30°).
const HEAD = new Set(["ParamAngleX", "ParamAngleY"]);

function loadScript(src) {
  return new Promise((resolve, reject) => {
    const s = Object.assign(document.createElement("script"), { src, onload: resolve, onerror: () => reject(new Error(`failed to load ${src}`)) });
    document.head.append(s);
  });
}

export async function mount(avatar, info) {
  document.body.classList.add("avatar-live2d");
  // The engine expects Cubism Core as a global, loaded beside it.
  if (!window.Live2DCubismCore) await loadScript(info.renderer.replace(/[^/]+$/, "live2dcubismcore.min.js"));
  const [{ Application, extensions }, { Live2DModel, Live2DPlugin }] = await Promise.all([import("pixi.js"), import(info.renderer)]);
  extensions.add(Live2DPlugin);

  const host = avatar.el.canvas;
  const app = new Application();
  await app.init({ resizeTo: host, preference: "webgl", backgroundAlpha: 0, antialias: true, autoDensity: true, resolution: Math.min(window.devicePixelRatio, 2) });
  host.append(app.canvas);

  avatar.progress(0.3);
  const model = await Live2DModel.from(info.model, { autoFocus: false, autoHitTest: false });
  avatar.progress(1);
  app.stage.addChild(model);

  const internal = model.internalModel;
  internal.eyeBlink = undefined; // blinks come with the motion stream
  const core = internal.coreModel;
  // The Cubism framework addresses parameters by id handle, not by name;
  // a plain string silently matches nothing. Each parameter also has its
  // own [min, default, max], which relative rig values are mapped into.
  const params = new Map();
  const param = (name) => {
    if (!params.has(name)) {
      const handle = internal.getIdSafe(name);
      const i = core.getParameterIndex(handle);
      const known = i < core.getParameterCount();
      params.set(name, known && {
        handle,
        range: [core.getParameterMinimumValue(i), core.getParameterDefaultValue(i), core.getParameterMaximumValue(i)],
      });
    }
    return params.get(name);
  };
  const baseHeight = model.height / model.scale.y;
  const frame = () => {
    const { width: w, height: h } = app.screen;
    model.scale.set((h * FRAMING.height) / baseHeight);
    model.anchor.set(0.5, 0);
    model.position.set(w / 2, h * FRAMING.top);
  };
  app.renderer.on("resize", frame);
  frame();

  avatar.l2d = { core, param, internal, model }; // for in-page debugging
  internal.on("beforeModelUpdate", () => {
    const e = avatar.expression();
    const b = avatar.act();
    const p = rig(e, { ...b, headYaw: b.gaze.x * 0.25 }, avatar.pose);
    // Applied on the next frame's physics; one frame (8-16 ms) of lag.
    internal.focusController.focus(p.ParamAngleX, p.ParamAngleY, true);
    for (const [name, v] of Object.entries(p)) {
      const q = !HEAD.has(name) && param(name);
      if (!q) continue; // a parameter this model does not have
      if (ADDITIVE.has(name)) core.addParameterValueById(q.handle, toCubism(v, q.range) - q.range[1]);
      else core.setParameterValueById(q.handle, toCubism(v, q.range));
    }
    // Debug overrides (window.__talktalk.param, model units) win over everything.
    for (const [name, v] of Object.entries(avatar.paramOverride ?? {})) {
      const q = param(name);
      if (q) core.setParameterValueById(q.handle, v);
    }
    avatar.mouth = p.ParamMouthOpenY;
    avatar.headYaw = p.ParamAngleX * (Math.PI / 6); // radians, as vrm.js reports
  });
}
