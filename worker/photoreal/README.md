# Photoreal worker

Renders TalkTalk's photoreal characters on a GPU: talking video drawn by
[Ditto](https://github.com/antgroup/ditto-talkinghead) (Ant Group, Apache-2.0)
from a short idle clip of the character, driven by the exact audio the
listener will hear.

## How it fits

```
browser ──WebRTC (Opus voice + H.264 video, one stream)── TalkTalk server
                                                              │  TCP via SSH tunnel
                                                              ▼
                                         worker: audio in → Ditto (TensorRT) → x264 → frames out
```

- The server's player sends the worker each 20 ms of audio `TALKTALK_PHOTOREAL_LEAD_MS`
  before it plays (silence when idle, so the worker draws the idle face too).
- Frame `k` shows audio samples `[k*640, (k+1)*640)` at 16 kHz of that stream; the
  server sends it with the audio of the same moment, so lips and voice share a clock.
- One engine process per conversation (TensorRT state is per process); one per character
  is kept loaded so a call starts in ~0.3 s instead of ~10 s.

Measured on an RTX 4080 SUPER 32 GB, 720×1280 at 25 fps: worker latency p50 0.91 s,
p95 0.98 s; Ditto real-time factor 0.8; ~2.3 GB VRAM per engine; in the browser the
mouth follows the sound within one frame (0–40 ms).

## Setup (AutoDL)

```bash
ROOT=/root/autodl-tmp bash setup_autodl.sh     # env, TensorRT 8.6.1, models, engines
# put each character's idle clip in $ROOT/avatars/ and list it in characters.json
cd $ROOT/worker && python server.py --listen 127.0.0.1:9800 --characters characters.json
```

On the machine running TalkTalk, tunnel the port and point the server at it:

```bash
ssh -N -L 9800:127.0.0.1:9800 -p <port> root@<instance>
TALKTALK_PHOTOREAL_ADDR=127.0.0.1:9800 make run
```

`probe_client.py` streams a WAV through a running worker like the server does and reports
per-frame latency.

## Idle clips

4–5 s, the person facing the camera, mouth closed, eyes open **without blinking** (the
renderer adds blinks), steady head, plain lighting. A clip made from a still with an
image-to-video model and cut to a seamless loop works well.

## Licenses

Ditto: Apache-2.0. Its face detector comes from InsightFace (non-commercial models).
Generated characters must be fictional people.
