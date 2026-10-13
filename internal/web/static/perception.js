// Perception: reads the user's face from the camera with MediaPipe's face
// landmarker, entirely in the browser. Only a few numbers leave this file:
// whether a face is there, how much it smiles, whether the mouth is
// moving, and where the face is in the picture.

const DETECT_HZ = 15;
const MOUTH_WINDOW_MS = 400;

// mouthActive decides from recent mouth measurements whether the mouth is
// in use: the jaw, plus lip shapes for sounds made mostly with the lips
// (u, wu, b, m). An open or moving mouth is active. With too few samples
// (a face that just appeared) the answer is "active", which never vetoes:
// the server only acts on a confidently still mouth.
const MIN_SAMPLES = 4;
export function mouthActive(jaws, lips = []) {
  if (jaws.length < MIN_SAMPLES) return true;
  for (const series of [jaws, lips]) {
    if (series.length === 0) continue;
    const hi = Math.max(...series), lo = Math.min(...series);
    if (hi > 0.12 || hi - lo > 0.05) return true;
  }
  return false;
}

// perceptionMessage is what the server is told about the latest reading.
// A reading older than FRESH_MS (background tab, frozen or unplugged
// camera) says nothing about now, so it is reported as "no face".
const FRESH_MS = 250;
export function perceptionMessage(reading, now) {
  const fresh = reading && now - reading.at <= FRESH_MS;
  return { type: "perception", face: Boolean(fresh && reading.face), mouth: Boolean(fresh && reading.mouth) };
}

// The landmarker is loaded once, ahead of the first call, and warmed up
// with one detection. CPU, not GPU: on a 320x240 picture the GPU delegate
// was slower (40 ms vs 14 ms per frame) and its first detection paid
// seconds of shader compilation.
let landmarker = null;
export function preloadPerception(info) {
  landmarker ??= (async () => {
    try {
    const { FaceLandmarker, FilesetResolver } = await import(info.bundle);
    const fileset = await FilesetResolver.forVisionTasks(info.wasm);
    const lm = await FaceLandmarker.createFromOptions(fileset, {
      baseOptions: { modelAssetPath: info.model, delegate: "CPU" },
      runningMode: "VIDEO",
      numFaces: 1,
      outputFaceBlendshapes: true,
    });
    const blank = Object.assign(document.createElement("canvas"), { width: 64, height: 48 });
    blank.getContext("2d").fillRect(0, 0, 64, 48);
    lm.detectForVideo(blank, performance.now());
    return lm;
    } catch (err) {
      landmarker = null; // let a later call try again
      throw err;
    }
  })();
  return landmarker;
}

export class Perception {
  constructor(info, onReading) {
    this.info = info;
    this.onReading = onReading;
    this.jaw = [];          // { at, v } recent jawOpen, for mouth activity
    this.reading = null;
    this.stopped = false;
  }

  // start asks for the camera and begins reading. It resolves false if the
  // camera is unavailable or declined: the conversation goes on without it.
  async start() {
    try {
      this.stream = await navigator.mediaDevices.getUserMedia({
        video: { width: { ideal: 320 }, height: { ideal: 240 }, frameRate: { ideal: DETECT_HZ } },
      });
    } catch (err) {
      console.warn("perception: camera declined or unavailable:", err?.name ?? err);
      return false;
    }
    try {
      this.landmarker = await preloadPerception(this.info);
    } catch (err) {
      console.error("face landmarker unavailable", err);
      this.stream.getTracks().forEach((t) => t.stop());
      return false;
    }
    this.video = Object.assign(document.createElement("video"), { muted: true, playsInline: true, srcObject: this.stream });
    try {
      await this.video.play();
    } catch (err) {
      console.warn("perception: camera video would not play:", err?.name ?? err);
      this.stream.getTracks().forEach((t) => t.stop());
      return false;
    }
    this.loop();
    return true;
  }

  loop() {
    if (this.stopped) return;
    const now = performance.now();
    if (this.video.readyState >= 2 && now - (this.lastDetect ?? 0) >= 1000 / DETECT_HZ) {
      this.lastDetect = now;
      this.detect(now);
    }
    requestAnimationFrame(() => this.loop());
  }

  detect(now) {
    const res = this.landmarker.detectForVideo(this.video, now);
    const shapes = res.faceBlendshapes?.[0]?.categories;
    const lm = res.faceLandmarks?.[0];
    if (!shapes || !lm) {
      this.jaw = []; // a face coming back starts with an unknown mouth
      this.publish({ face: false, smile: 0, jaw: 0, mouth: false, cx: 0.5, cy: 0.5 });
      return;
    }
    const s = Object.fromEntries(shapes.map((c) => [c.categoryName, c.score]));
    const jaw = s.jawOpen ?? 0;
    const lips = Math.max(s.mouthFunnel ?? 0, s.mouthPucker ?? 0,
      ((s.mouthLowerDownLeft ?? 0) + (s.mouthLowerDownRight ?? 0)) / 2,
      ((s.mouthUpperUpLeft ?? 0) + (s.mouthUpperUpRight ?? 0)) / 2);
    this.jaw.push({ at: now, v: jaw, lips });
    while (this.jaw.length && this.jaw[0].at < now - MOUTH_WINDOW_MS) this.jaw.shift();
    const mouth = mouthActive(this.jaw.map((x) => x.v), this.jaw.map((x) => x.lips));
    let x = 0, y = 0;
    for (const p of lm) { x += p.x; y += p.y; }
    this.publish({
      face: true,
      smile: ((s.mouthSmileLeft ?? 0) + (s.mouthSmileRight ?? 0)) / 2,
      jaw,
      mouth,
      cx: x / lm.length,
      cy: y / lm.length,
    });
  }

  publish(r) {
    this.reading = { ...r, at: performance.now() };
    this.onReading?.(this.reading);
  }

  // stop releases the camera; the shared landmarker stays for the next call.
  stop() {
    this.stopped = true;
    this.stream?.getTracks().forEach((t) => t.stop());
  }
}
