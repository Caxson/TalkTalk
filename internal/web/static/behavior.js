// Behavior: what the avatar does about the person in front of it, for any
// renderer. Gaze follows the face; a smile is answered with a smile after
// a human beat; a pause in the user's speech gets a small nod; while the
// user talks, the eyes settle.

const SMILE_DELAY_MS = 350;   // people smile back after a beat, not instantly
const SMILE_TAU_MS = 300;
const GAZE_TAU_MS = 160;
const NOD = { down: 0.11, inMs: 200, outMs: 320, gapMs: 3500 };
const PAUSE = { minMs: 250, maxMs: 700, recentTalkMs: 1500 };

const smooth = (cur, target, dtMs, tau) => cur + (target - cur) * (1 - Math.exp(-dtMs / tau));

export class Behavior {
  constructor() {
    this.smile = 0;
    this.gaze = { x: 0, y: 0 };
    this.smileLine = [];      // delayed user smile
    this.talkedAt = -1e9;     // last time the user's mic was loud
    this.quietSince = 0;
    this.nodAt = -1e9;
    this.nodded = true;       // nod at most once per pause
  }

  // update advances one drawn frame. reading may be null (no camera);
  // micLevel is 0..1 from the microphone meter.
  update(now, dtMs, state, reading, micLevel) {
    const face = reading && reading.face && now - reading.at < 500 ? reading : null;

    // Smile back, delayed and eased.
    this.smileLine.push({ at: now, v: face ? Math.min(1, Math.max(0, (face.smile - 0.25) / 0.5)) : 0 });
    while (this.smileLine.length > 1 && this.smileLine[1].at <= now - SMILE_DELAY_MS) this.smileLine.shift();
    this.smile = smooth(this.smile, this.smileLine[0].v, dtMs, SMILE_TAU_MS);

    // Look at the face (the camera image is not mirrored: the user moving
    // to their right moves left in the picture, and right on screen).
    const tx = face ? -(face.cx - 0.5) * 2 : 0;
    const ty = face ? -(face.cy - 0.45) * 2 : 0;
    this.gaze.x = smooth(this.gaze.x, Math.max(-1, Math.min(1, tx)), dtMs, GAZE_TAU_MS);
    this.gaze.y = smooth(this.gaze.y, Math.max(-1, Math.min(1, ty)), dtMs, GAZE_TAU_MS);

    // Nod into short pauses while the user holds the floor.
    const talking = micLevel > 0.25;
    if (talking) {
      this.talkedAt = now;
      this.quietSince = 0;
      this.nodded = false;
    } else if (!this.quietSince) {
      this.quietSince = now;
    }
    const pause = this.quietSince ? now - this.quietSince : 0;
    if (state === "listening" && !this.nodded && pause >= PAUSE.minMs && pause <= PAUSE.maxMs
        && now - this.talkedAt < PAUSE.recentTalkMs + pause && now - this.nodAt > NOD.gapMs) {
      this.nodAt = now;
      this.nodded = true;
    }

    return {
      smile: this.smile,
      gaze: this.gaze,
      nod: this.nodCurve(now),
      // While the user talks, hold eye contact steadier.
      saccadeScale: state === "listening" && now - this.talkedAt < 800 ? 0.35 : 1,
      present: Boolean(face),
    };
  }

  // nodCurve: head pitch offset in radians for the current nod.
  nodCurve(now) {
    const e = now - this.nodAt;
    if (e < 0 || e > NOD.inMs + NOD.outMs) return 0;
    if (e < NOD.inMs) return NOD.down * Math.sin((e / NOD.inMs) * Math.PI / 2);
    return NOD.down * Math.cos(((e - NOD.inMs) / NOD.outMs) * Math.PI / 2);
  }
}
