// Photoreal renderer: LAM Gaussian-splat head (aigc3d/LAM_WebRender). It
// drives its own render loop and asks for the expression every frame.

const CHAT_STATE = { standby: "Idle", listening: "Listening", thinking: "Thinking", speaking: "Responding", yielding: "Listening" };

export async function mount(avatar, info) {
  document.body.classList.add("avatar-lam");
  const { GaussianSplatRenderer } = await import(info.renderer);
  await GaussianSplatRenderer.getInstance(avatar.el.canvas, info.model, {
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
    getChatState: () => CHAT_STATE[avatar.state] ?? "Idle",
    downloadProgress: (p) => avatar.progress(p),
  });
}
