"""One conversation's renderer, run in its own process.

Ditto (antgroup/ditto-talkinghead, online TensorRT pipeline) turns audio
windows into frames of the character's idle video with the face speaking.
Its file writer is replaced by an H.264 encoder, so each frame leaves as an
access unit tagged with its index. A fresh process per conversation keeps
TensorRT state private: a second pipeline in one process crashes on
teardown.
"""

import multiprocessing as mp
import os
import sys
import traceback
from fractions import Fraction

from protocol import FPS

DITTO_DIR = os.environ.get("DITTO_DIR", "/root/autodl-tmp/ditto")
DITTO_CFG = "checkpoints/ditto_cfg/v0.4_hubert_cfg_trt_online.pkl"
DITTO_TRT = "checkpoints/ditto_trt_custom"
CHUNK = (3, 5, 2)  # Ditto's online chunking: 3 past, 5 new, 2 future frames
FRAMES_FOREVER = 25 * 60 * 60 * 6  # Ditto wants a total; a conversation never reaches it
BITRATE = 2_500_000


class Encoder:
    """Stands in for Ditto's video writer: encodes each frame to H.264."""

    def __init__(self, out_q: mp.Queue, first_index: int):
        self.out_q = out_q
        self.index = first_index
        self.ctx = None

    def _open(self, height: int, width: int):
        import av

        ctx = av.CodecContext.create("libx264", "w")
        ctx.width, ctx.height, ctx.pix_fmt = width, height, "yuv420p"
        ctx.framerate, ctx.time_base = Fraction(FPS), Fraction(1, FPS)
        ctx.bit_rate = BITRATE
        # Browser-friendly and low-latency: baseline, no B-frames, a key frame
        # every second, parameter sets repeated so a late joiner can decode.
        ctx.options = {"preset": "veryfast", "tune": "zerolatency", "profile": "baseline",
                       "g": str(FPS), "bf": "0", "x264-params": "repeat-headers=1"}
        self.ctx = ctx

    def __call__(self, frame_rgb, fmt="rgb"):
        import av

        if self.ctx is None:
            self._open(*frame_rgb.shape[:2])
        frame = av.VideoFrame.from_ndarray(frame_rgb, format="rgb24")
        frame.pts = self.index
        self.index += 1
        for packet in self.ctx.encode(frame):
            # zerolatency: one packet per frame, tagged with that frame's pts.
            self.out_q.put(("frame", int(packet.pts), bytes(packet)))

    def close(self):
        pass  # the conversation ends here; nothing to flush to


def run(character: dict, first_index: int, in_q: mp.Queue, out_q: mp.Queue):
    """Process body: set up Ditto for the character, then turn each audio
    window from in_q into frames on out_q until None arrives."""
    try:
        sys.path.insert(0, DITTO_DIR)
        os.chdir(DITTO_DIR)
        from stream_pipeline_online import StreamSDK

        sdk = StreamSDK(DITTO_CFG, DITTO_TRT)
        sdk.setup(character["source"], "/tmp/ditto_unused.mp4", **character.get("setup", {}))
        try:
            sdk.writer.close()  # the file writer Ditto opened; ours replaces it
        except Exception:
            pass
        sdk.writer = Encoder(out_q, first_index)
        sdk.setup_Nd(N_d=FRAMES_FOREVER, fade_in=-1, fade_out=-1, ctrl_info={})
        h, w = sdk.source_info["img_rgb_lst"][0].shape[:2]
        out_q.put(("ready", w, h))
        while True:
            window = in_q.get()
            if window is None:
                break
            sdk.run_chunk(window, CHUNK)
    except Exception:
        out_q.put(("error", traceback.format_exc()))
    finally:
        out_q.put(("done",))
        os._exit(0)  # skip TensorRT/mediapipe teardown, which crashes
