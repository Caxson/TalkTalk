// Feelings on the face. The server tags reply segments with an affect
// (happy, surprised, ...) as they start playing; Mood eases each one in
// and out and turns them into ARKit weights that every renderer can use
// on top of lip-sync. Pure, so it is testable without a renderer.

// ARKit weights at full intensity: strong enough to read at avatar size,
// short of caricature. They sit under speech, which keeps its mouth.
export const AFFECTS = {
  happy: { mouthSmileLeft: 0.75, mouthSmileRight: 0.75, cheekSquintLeft: 0.5, cheekSquintRight: 0.5, eyeSquintLeft: 0.35, eyeSquintRight: 0.35, browInnerUp: 0.1 },
  surprised: { browInnerUp: 0.8, browOuterUpLeft: 0.8, browOuterUpRight: 0.8, eyeWideLeft: 0.7, eyeWideRight: 0.7, jawOpen: 0.12 },
  thinking: { browDownLeft: 0.45, browDownRight: 0.15, mouthPressLeft: 0.4, mouthPressRight: 0.4, eyeLookUpLeft: 0.45, eyeLookUpRight: 0.45, mouthLeft: 0.2 },
  shy: { mouthSmileLeft: 0.45, mouthSmileRight: 0.45, cheekSquintLeft: 0.45, cheekSquintRight: 0.45, eyeLookDownLeft: 0.5, eyeLookDownRight: 0.5, browInnerUp: 0.3 },
  sad: { browInnerUp: 0.8, mouthFrownLeft: 0.65, mouthFrownRight: 0.65, eyeLookDownLeft: 0.25, eyeLookDownRight: 0.25, eyeSquintLeft: 0.15, eyeSquintRight: 0.15 },
};

// Head pose that goes with each feeling, relative (-1..1 ≈ ±30°): a tilt
// while thinking, eyes-down for shy and sad, chin up when surprised. The
// head says more than the brows at avatar size, in any art style.
export const POSES = {
  happy: { roll: 0.08, pitch: 0.05 },
  surprised: { roll: 0, pitch: 0.15 },
  thinking: { roll: 0.25, pitch: 0.12 },
  shy: { roll: -0.18, pitch: -0.25 },
  sad: { roll: -0.06, pitch: -0.22 },
};

const ATTACK_MS = 180;   // easing in: quick, a feeling shows with the words
const RELEASE_MS = 600;  // easing out: slower, faces relax gradually
const LINGER_MS = 2500;  // a feeling outlasts the speech that carried it

export class Mood {
  constructor() {
    this.current = null;   // affect being shown, or null for neutral
    this.until = 0;        // hold the current affect at least until then
    this.levels = Object.fromEntries(Object.keys(AFFECTS).map((k) => [k, 0]));
  }

  // feel switches to an affect ("neutral" or unknown names relax the face).
  feel(name, now) {
    this.current = AFFECTS[name] ? name : null;
    this.until = now + LINGER_MS;
  }

  // update advances one drawn frame and returns the blended ARKit weights.
  // While the assistant speaks the feeling is held; after it, it lingers
  // a little, then relaxes.
  update(now, dt, speaking) {
    if (speaking) this.until = Math.max(this.until, now + LINGER_MS);
    const active = this.current && now < this.until ? this.current : null;
    const out = {};
    for (const [name, shape] of Object.entries(AFFECTS)) {
      const target = name === active ? 1 : 0;
      const tau = target > this.levels[name] ? ATTACK_MS : RELEASE_MS;
      this.levels[name] += (target - this.levels[name]) * (1 - Math.exp(-dt / tau));
      if (this.levels[name] < 1e-3) continue;
      for (const [k, w] of Object.entries(shape)) out[k] = Math.max(out[k] ?? 0, w * this.levels[name]);
    }
    return out;
  }

  // pose returns the head pose for the current levels (call after update).
  pose() {
    let roll = 0, pitch = 0;
    for (const [name, p] of Object.entries(POSES)) {
      roll += p.roll * this.levels[name];
      pitch += p.pitch * this.levels[name];
    }
    return { roll, pitch };
  }
}
