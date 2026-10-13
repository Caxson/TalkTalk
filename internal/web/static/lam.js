// Photoreal renderer: LAM Gaussian-splat head (aigc3d/LAM_WebRender). It
// drives its own render loop and asks for the expression every frame.

const CHAT_STATE = { standby: "Idle", listening: "Listening", thinking: "Thinking", speaking: "Responding", yielding: "Listening" };

export async function mount(avatar, info) {
  document.body.classList.add("avatar-lam");
  const { GaussianSplatRenderer } = await import(info.renderer);
  await GaussianSplatRenderer.getInstance(avatar.el.canvas, info.model, {
    getExpressionData: () => {
      const e = avatar.expression();
      avatar.mouth = e.jawOpen ?? 0;
      return e;
    },
    getChatState: () => CHAT_STATE[avatar.state] ?? "Idle",
    downloadProgress: (p) => avatar.progress(p),
  });
}
