// Photoreal renderer: LAM Gaussian-splat head (aigc3d/LAM_WebRender). It
// drives its own render loop and asks for the expression every frame.
//
// With a background photo (the picture the head was made from) the head
// is composited onto it: the photo supplies hair, body and room, and only
// the face of the render shows, fitted onto the photo's face by landmarks
// and feathered into it. Without one, or if fitting fails, the bare head
// is shown as before.

import { anchors, apply, coverFit, faceMask, similarity } from "./facealign.js";
import { imageLandmarker } from "./perception.js";

const CHAT_STATE = { standby: "Idle", listening: "Listening", thinking: "Thinking", speaking: "Responding", yielding: "Listening" };
// Where the photo is anchored in the frame (object-position): faces sit
// in the upper part of a portrait, so keep the head in view.
const PHOTO_POS = { x: 0.5, y: 0.25 };

export async function mount(avatar, info) {
  if (info.background) {
    try {
      await mountOnPhoto(avatar, info);
      return;
    } catch (err) {
      console.warn("lam: compositing onto the photo failed, showing the bare head:", err);
      avatar.el.canvas.replaceChildren();
      document.body.classList.remove("avatar-photo");
    }
  }
  document.body.classList.add("avatar-lam");
  await startRenderer(avatar, avatar.el.canvas, info, false);
}

// still: keep the renderer's own head and body animation off. On a photo
// the head must stay where it was fitted; only the face moves.
async function startRenderer(avatar, container, info, still) {
  const { GaussianSplatRenderer } = await import(info.renderer);
  await GaussianSplatRenderer.getInstance(container, info.model, {
    getExpressionData: () => {
      const e = avatar.expression();
      const b = avatar.act();
      avatar.mouth = e.jawOpen ?? 0;
      // No bones to turn here: answer smiles and follow with the eyes only.
      for (const side of ["Left", "Right"]) e[`mouthSmile${side}`] = Math.max(e[`mouthSmile${side}`] ?? 0, b.smile * 0.6);
      const gx = b.gaze.x * 0.5, gy = b.gaze.y * 0.5;
      e.eyeLookOutLeft = Math.max(0, gx); e.eyeLookInRight = Math.max(0, gx);
      e.eyeLookInLeft = Math.max(0, -gx); e.eyeLookOutRight = Math.max(0, -gx);
      e.eyeLookUpLeft = e.eyeLookUpRight = Math.max(0, gy);
      e.eyeLookDownLeft = e.eyeLookDownRight = Math.max(0, -gy);
      return e;
    },
    getChatState: () => (still ? "Idle" : CHAT_STATE[avatar.state] ?? "Idle"),
    downloadProgress: (p) => avatar.progress(p),
  });
}

async function mountOnPhoto(avatar, info) {
  if (!info.perception) throw new Error("no face landmarker to fit the head with");
  document.body.classList.add("avatar-photo");
  const host = avatar.el.canvas;
  const photo = Object.assign(new Image(), { className: "still-photo", src: info.background, alt: "" });
  const layer = Object.assign(document.createElement("div"), { className: "lam-layer" });
  layer.style.visibility = "hidden"; // until it is fitted
  host.append(photo, layer);

  const [landmarker] = await Promise.all([imageLandmarker(info.perception), photo.decode()]);
  const photoFace = detect(landmarker, photo);
  if (!photoFace) throw new Error("no face found in the background photo");
  const photoPts = anchors(photoFace, photo.naturalWidth, photo.naturalHeight);

  await startRenderer(avatar, layer, info, true);
  const canvas = layer.querySelector("canvas");
  if (!canvas) throw new Error("renderer has no canvas");
  const headPts = await renderedFace(landmarker, canvas);
  if (!headPts) throw new Error("no face found in the rendered head");

  // Anchors are kept relative to the layer's size so the fit survives resizes.
  const rel = headPts.map((p) => ({ x: p.x / canvas.width, y: p.y / canvas.height }));
  const fit = () => {
    const lw = layer.clientWidth, lh = layer.clientHeight;
    const local = rel.map((p) => ({ x: p.x * lw, y: p.y * lh }));
    const toFrame = coverFit(photo.naturalWidth, photo.naturalHeight, host.clientWidth, host.clientHeight, PHOTO_POS.x, PHOTO_POS.y);
    const m = similarity(local, photoPts.map((p) => apply(toFrame, p)));
    layer.style.transform = `matrix(${m.a}, ${m.b}, ${m.c}, ${m.d}, ${m.e}, ${m.f})`;
    layer.style.maskImage = layer.style.webkitMaskImage = faceMask(local);
  };
  photo.style.objectPosition = `${PHOTO_POS.x * 100}% ${PHOTO_POS.y * 100}%`;
  new ResizeObserver(fit).observe(host);
  fit();
  layer.style.visibility = "";
  avatar.el.take.textContent += " · photo";
}

function detect(landmarker, source) {
  const r = landmarker.detect(source);
  return r.faceLandmarks?.[0] ?? null;
}

// renderedFace reads the head back from the WebGL canvas. The buffer is
// only valid during the frame it was drawn in, so copy inside an animation
// frame, and retry while the splats are still fading in.
async function renderedFace(landmarker, canvas) {
  const copy = document.createElement("canvas");
  for (let attempt = 0; attempt < 30; attempt++) {
    await new Promise((r) => setTimeout(r, 150));
    await new Promise((r) => requestAnimationFrame(r));
    copy.width = canvas.width;
    copy.height = canvas.height;
    copy.getContext("2d").drawImage(canvas, 0, 0);
    const face = detect(landmarker, copy);
    if (face) return anchors(face, copy.width, copy.height);
  }
  return null;
}
