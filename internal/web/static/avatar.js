// Avatar motion, independent of how the face is drawn. Lip-sync frames
// arrive with the audio's packets but skip the jitter buffer, so they are
// shown after the same delay the browser applies to that audio, and
// blended between frames so motion stays smooth at any refresh rate.
// Renderers (vrm.js, live2d.js, lam.js) read expression() once per drawn frame.

import { Behavior } from "./behavior.js";

const RENDERERS = { vrm: "./vrm.js", lam: "./lam.js", live2d: "./live2d.js" };
const MOTION_HEADER = 4;   // uint32 sequence before the weights
const KEEP_MS = 1000;      // frame history kept for interpolation
const STALE_MS = 120;      // past the newest frame: relax to neutral
const RELAX_TAU_MS = 90;   // neutral relaxation time constant

export class Avatar {
  constructor(el) {
    this.el = el;           // { canvas, load, fps, sync, take }
    this.names = null;      // blendshape names, in frame order
    this.frames = [];       // { at, w: Float32Array }
    this.delayMs = 80;
    this.state = "standby";
    this.last = null;
    this.nextBlink = performance.now() + 2500;
    this.blinkAt = -1;
    this.fpsCount = 0;
    this.fpsSince = performance.now();
    this.received = 0;
    this.mouth = 0;         // renderer-reported mouth opening, for checks
    this.behavior = new Behavior();
    this.reading = null;    // latest camera reading, if any
    this.micLevel = () => 0;
    this.lastAct = performance.now();
    this.acted = null;      // latest behavior output, for checks
  }

  perceive(reading) { this.reading = reading; }

  // act advances behavior one drawn frame (renderers call it once per frame).
  act() {
    const now = performance.now();
    const dt = Math.min(100, now - this.lastAct);
    this.lastAct = now;
    this.acted = this.behavior.update(now, dt, this.state, this.reading, this.micLevel());
    return this.acted;
  }

  // create mounts the renderer for a character (from /api/characters); a
  // character without a model is voice-only.
  static async create(el, info) {
    if (!info?.model) return null;
    const a = new Avatar(el);
    try {
      const mod = await import(RENDERERS[info.kind] ?? "./vrm.js");
      await mod.mount(a, info);
      el.load.hidden = true;
    } catch (err) {
      el.load.textContent = "avatar failed to load";
      console.error(err);
      return null;
    }
    return a;
  }

  progress(p) { this.el.load.textContent = `loading avatar · ${Math.round(p * 100)}%`; }
  setNames(names) { this.names = names; }
  setState(state) { this.state = state; }
  setDelay(ms) { this.delayMs = ms; this.el.sync.textContent = `sync ${Math.round(ms)}ms`; }

  // A binary motion frame from the data channel.
  onMotion(buf) {
    if (!this.names) return;
    const bytes = new Uint8Array(buf);
    if (bytes.length !== MOTION_HEADER + this.names.length) return;
    const w = new Float32Array(this.names.length);
    for (let i = 0; i < w.length; i++) w[i] = bytes[MOTION_HEADER + i] / 255;
    const now = performance.now();
    this.frames.push({ at: now, w });
    this.received++;
    // Bound the buffer here too: renderers pause in background tabs while
    // frames keep arriving.
    this.prune(now);
  }

  prune(t) {
    let drop = 0;
    while (drop < this.frames.length - 2 && this.frames[drop + 1].at < t - KEEP_MS) drop++;
    if (drop) this.frames.splice(0, drop);
  }

  // expression returns { arkitName: weight } for this drawn frame, with a
  // procedural blink folded into eyeBlinkLeft/Right.
  expression() {
    const now = performance.now();
    this.tickFps(now);
    if (!this.names) return {};
    const t = now - this.delayMs;
    this.prune(t);

    let w = this.sample(t);
    if (!w) {
      // No motion due: ease the face back to neutral instead of freezing.
      const k = Math.exp(-16 / RELAX_TAU_MS);
      w = this.last ? this.last.map((v) => v * k) : new Float32Array(this.names.length);
    }
    this.last = w;
    const out = {};
    this.names.forEach((n, i) => { out[n] = w[i]; });
    const blink = this.blink(now);
    out.eyeBlinkLeft = Math.max(out.eyeBlinkLeft ?? 0, blink);
    out.eyeBlinkRight = Math.max(out.eyeBlinkRight ?? 0, blink);
    return out;
  }

  // Interpolated weights at time t, or null when nothing is due.
  sample(t) {
    const f = this.frames;
    if (f.length === 0 || t < f[0].at) return null;
    let i = f.length - 1;
    while (i > 0 && f[i].at > t) i--;
    if (i === f.length - 1) return t - f[i].at > STALE_MS ? null : f[i].w;
    const a = f[i], b = f[i + 1];
    const k = Math.min(1, Math.max(0, (t - a.at) / Math.max(1, b.at - a.at)));
    return a.w.map((v, j) => v + (b.w[j] - v) * k);
  }

  // Procedural blink every 2.5-6 s: 70 ms closing, 110 ms opening.
  blink(now) {
    if (this.blinkAt < 0 && now >= this.nextBlink) this.blinkAt = now;
    if (this.blinkAt < 0) return 0;
    const e = now - this.blinkAt;
    if (e > 180) {
      this.blinkAt = -1;
      this.nextBlink = now + 2500 + Math.random() * 3500;
      return 0;
    }
    return e < 70 ? e / 70 : 1 - (e - 70) / 110;
  }

  tickFps(now) {
    this.fpsCount++;
    if (now - this.fpsSince >= 1000) {
      this.fps = Math.round((this.fpsCount * 1000) / (now - this.fpsSince));
      // Idle, renderers may throttle themselves; a low number there is not news.
      this.el.fps.textContent = this.state === "standby" ? "idle" : `${this.fps} fps`;
      this.fpsCount = 0;
      this.fpsSince = now;
    }
  }
}
