// Avatar: a LAM Gaussian-splat head driven by lip-sync frames that the
// server sends in lockstep with the audio. Frames are shown after the same
// delay the browser applies to that audio (its jitter buffer), and blended
// between frames so motion stays smooth at any refresh rate.

const MOTION_HEADER = 4;          // uint32 sequence before the weights
const KEEP_MS = 1000;             // frame history kept for interpolation
const STALE_MS = 120;             // past the newest frame: relax to neutral
const RELAX_TAU_MS = 90;          // neutral relaxation time constant
const CHAT_STATE = { standby: "Idle", listening: "Listening", thinking: "Thinking", speaking: "Responding", yielding: "Listening" };

export class Avatar {
  constructor(el) {
    this.el = el;           // { canvas, load, fps, sync, take }
    this.names = null;      // blendshape names, in frame order
    this.frames = [];       // { at, w: Float32Array }
    this.delayMs = 80;
    this.state = "standby";
    this.last = null;       // last weights returned, for relaxing
    this.nextBlink = performance.now() + 2500;
    this.blinkAt = -1;
    this.fpsCount = 0;
    this.fpsSince = performance.now();
    this.received = 0;
  }

  // create loads the avatar described by info (from /api/avatar).
  static async create(el, info) {
    if (!info?.enabled) return null;
    const a = new Avatar(el);
    try {
      await a.load(info);
    } catch (err) {
      el.load.textContent = "avatar failed to load";
      console.error(err);
      return null;
    }
    return a;
  }

  async load(info) {
    const { GaussianSplatRenderer } = await import(info.renderer);
    const pct = (p) => { this.el.load.textContent = `loading avatar · ${Math.round(p * 100)}%`; };
    await GaussianSplatRenderer.getInstance(this.el.canvas, info.model, {
      getExpressionData: () => this.expression(),
      getChatState: () => CHAT_STATE[this.state] ?? "Idle",
      downloadProgress: pct,
      loadProgress: (p) => { if (p >= 1) this.el.load.hidden = true; },
    });
    this.el.load.hidden = true;
  }

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
    // Bound the buffer here too: rendering (which also prunes) pauses in
    // background tabs while frames keep arriving.
    let drop = 0;
    while (drop < this.frames.length - 2 && this.frames[drop + 1].at < now - KEEP_MS) drop++;
    if (drop) this.frames.splice(0, drop);
  }

  // Called by the renderer every animation frame.
  expression() {
    const now = performance.now();
    this.tickFps(now);
    if (!this.names) return {};
    const t = now - this.delayMs;
    let drop = 0;
    while (drop < this.frames.length - 2 && this.frames[drop + 1].at < t - KEEP_MS) drop++;
    if (drop) this.frames.splice(0, drop);

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
      // Idle, the renderer throttles itself; a low number there is not news.
      this.el.fps.textContent = this.state === "standby" ? "idle" : `${this.fps} fps`;
      this.fpsCount = 0;
      this.fpsSince = now;
    }
  }
}
