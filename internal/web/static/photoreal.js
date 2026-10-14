// Photoreal characters: video rendered on the GPU worker arrives as a WebRTC
// video track in the same stream as the voice. Until the first frame plays,
// the frame shows the character's still photo.
//
// The voice plays through the video element rather than the page's audio
// element: one element rendering both tracks of the stream keeps them in
// sync, which two elements would not.

export function mountPhotoreal(host, info, labels = {}) {
  document.body.classList.add("avatar-video");
  const still = Object.assign(new Image(), { className: "still-photo", src: info.background ?? "", alt: "" });
  const video = Object.assign(document.createElement("video"), { className: "still-video", playsInline: true, autoplay: true });
  host.replaceChildren(still, video);
  video.addEventListener("playing", () => host.classList.add("video-live"));
  // Slate marks: frames actually presented per second, and drops.
  let last = null;
  setInterval(() => {
    const q = video.getVideoPlaybackQuality?.();
    if (!q || !labels.fps) return;
    if (last && video.srcObject) {
      const shown = q.totalVideoFrames - last.totalVideoFrames - (q.droppedVideoFrames - last.droppedVideoFrames);
      labels.fps.textContent = `${shown} fps`;
      if (labels.sync) labels.sync.textContent = `drop ${q.droppedVideoFrames}`;
    } else if (!video.srcObject) {
      labels.fps.textContent = "still";
    }
    last = { totalVideoFrames: q.totalVideoFrames, droppedVideoFrames: q.droppedVideoFrames };
  }, 1000);

  return {
    // show plays the call's stream (voice + video) and resolves true once
    // the element plays it, so the caller can silence its own copy of the
    // voice only when this one is really audible.
    async show(stream) {
      if (video.srcObject !== stream) {
        video.srcObject = stream;
        video.muted = false;
      }
      try {
        await video.play();
        return true;
      } catch (err) {
        console.warn("photoreal: video play failed:", err);
        return false;
      }
    },
    clear() {
      video.srcObject = null;
      host.classList.remove("video-live");
    },
    get playing() { return host.classList.contains("video-live"); },
    get element() { return video; },
  };
}
