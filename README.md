# TalkTalk

A real-time conversational digital human that listens as well as it talks.

TalkTalk is a Go control plane that holds a live voice conversation with you
through the browser: streaming speech recognition, an LLM, streaming speech
synthesis, and an avatar whose face and head are driven by a single stream of
motion parameters (ARKit 52 blendshapes + head pose). The avatar keeps
reacting while you speak, not only when it is its turn.

> Status: **M3** — voice conversation with a lip-synced avatar that also
> sees you: it follows your face with its eyes, smiles back, nods into your
> pauses, and won't mistake its own voice from your speakers for you.

## Quick start

Requires Go 1.24+ (the toolchain is fetched automatically), cgo and libopus.

```bash
brew install opus pkg-config          # Debian/Ubuntu: apt install libopus-dev pkg-config
cp .env.example .env                  # fill TALKTALK_LLM_API_KEY and DASHSCOPE_API_KEY
make run                              # http://localhost:8080
```

Optional avatar (macOS shown):

```bash
brew install onnxruntime
make assets                           # ~460 MB: lip-sync model, renderers, avatars, face landmarker
echo 'TALKTALK_LIPSYNC_MODEL=assets/models/wav2arkit_cpu.onnx' >> .env
```

Open the page, press **Begin conversation** (or Space), allow the microphone
and talk. Use headphones or a laptop's built-in speakers with browser echo
cancellation; the assistant keeps listening while it speaks.

`make help` lists every target; `make check` runs what CI runs.

## How a turn works

```
browser mic ─Opus/WebRTC─▶ Go ─16 kHz PCM─▶ streaming ASR (DashScope Paraformer)
                                                │ final sentence
                                                ▼
browser ◀─Opus/WebRTC── 20 ms playout ◀── TTS per sentence ◀── streaming LLM (DeepSeek)
   ▲                         (CosyVoice)      (first sentence cut early)
   └─ data channel: state, transcript, latency
```

- **Speculative replies.** A reply starts the moment the recognizer closes a
  sentence. If you keep talking before any sound plays, it is withdrawn and
  your sentences are answered together.
- **Soft barge-in.** When you start talking over the assistant it yields at
  once, ducking to a quarter volume within a few frames, and stops only when
  recognized words confirm it: two characters, or one (「停」) when the camera
  also sees your mouth moving. A cough or a 「嗯」 just dips the volume for a
  moment. History keeps only what was heard.
- **Echo.** Its own voice coming back through the microphone is recognized
  three ways: the words match what it just said (checked during the reply
  and for 2 s after it ends, when the last sentence's echo is still
  arriving), or the camera sees you on screen with a still mouth (for short
  sounds only, so a misreading camera can never lock you out). In a test
  that fed the assistant's own voice back into the microphone at 30% with
  no echo cancellation at all, it was never interrupted by itself and never
  answered its own words.
- **Cast.** Characters picked in the page (each call keeps one): Mio (VRoid
  3D anime) and Hiyori (Live2D 2D anime). A photoreal character can be
  added through a cast file (see below). Each has its own CosyVoice voice and persona. One motion
  stream drives them all; renderers differ only in how much of it they
  show: VRM vowel shapes via VMagicMirror's mapping, Live2D standard
  parameters, or the ARKit shapes directly (LAM, and any "Perfect Sync"
  VRM with all 52 ARKit shapes, added through a cast file). Micro-expression
  data comes from real faces, so the motion side is trained on people and
  any body can wear it. `TALKTALK_CHARACTER` sets the default;
  `TALKTALK_CAST_FILE` replaces the roster with a JSON list of
  `{id, name, tagline, kind: vrm|live2d|lam, model, animation, voice,
  persona, credit}` (paths relative to `assets/`), which is how to add a
  model you downloaded yourself. Only characters whose files exist are
  offered.
- **Feelings.** The model opens each reply with a feeling tag
  (`[happy]`, `[surprised]`, `[thinking]`, `[shy]`, `[sad]`, `[neutral]`)
  and adds one where the mood turns. Tags are stripped before speech and
  travel with their sentence, so the face changes when that sentence is
  heard: smile and smiling eyes, raised brows and wide eyes, a head tilt
  and upward glance, lowered eyes, a frown. Feelings ease in over ~0.2 s,
  sit under lip-sync, and linger 2.5 s after the speech before relaxing.
  Live2D values are mapped through each model's own parameter ranges.
- **Photoreal video (Rin, Yuna).** Real-looking people rendered on a GPU
  worker (`worker/photoreal`, Ditto with TensorRT): it gets each moment's
  audio `TALKTALK_PHOTOREAL_LEAD_MS` (default 1200) before it plays and
  returns H.264 frames that go out with that audio on a WebRTC video track,
  so the mouth follows the voice within a frame. Head, hair and body move
  as one picture. Set `TALKTALK_PHOTOREAL_ADDR` (an SSH tunnel to the
  worker) and put each character's still in `assets/avatars/`; without a
  worker they are not offered. Costs: replies start about the lead later,
  and after a barge-in the lips may keep moving for up to the lead.
- **Photoreal head on a still (experimental, not in the default cast).** A LAM
  Gaussian-splat head composited onto the
  photo it was made from: the photo gives hair, body and room, and only
  the rendered face shows, fitted onto the photo's face by MediaPipe
  landmarks (eyes and mouth, a least-squares similarity) and feathered in.
  The head holds still so the fit stays exact; the face speaks and emotes,
  and the picture breathes slightly. It renders in the browser (120 fps on
  an M2 Max), no server GPU. To make one: put a front-facing, mouth-closed
  photo of a fictional person through the LAM studio on ModelScope with
  "Export ZIP file for Chatting Avatar" ticked, save the zip and the
  photo under `assets/avatars/`, and add the character with a cast file
  (`"kind": "lam", "model": "avatars/….zip", "background": "avatars/….png"`).
  The exported zip is published on Alibaba's public OSS by the studio.
  Known limit: only the face moves inside a still head and photo, which
  reads as a mask in motion; a GPU renderer that redraws whole frames is
  the planned replacement.
- **Asset licenses.** Everything is fetched by `make assets`, never
  committed. Hiyori is Live2D's sample under the Free Material License
  (small personal/indie use, credit shown under the avatar, no
  redistribution); Cubism Core is Live2D's proprietary runtime; LAM's
  weights (and so generated heads) are CC BY-NC 4.0; the VRoid sample follows pixiv's terms.
- **Perception (camera, browser-only).** MediaPipe's face landmarker reads
  your face in the page at 15 Hz; video never leaves the browser. The
  avatar looks toward your face, answers a smile after a ~350 ms beat,
  nods into short pauses while you talk, and holds steadier eye contact
  while listening. The server receives only "a face is there" and "its
  mouth is moving", ten times a second, and uses them against echo: while
  you are on camera with a still mouth, sound the assistant hears is its
  own voice leaking back, so it neither yields nor stops. With no camera,
  or nobody in view, behaviour is unchanged.
- **Lip-sync.** Synthesized speech is run through wav2arkit (ONNX, CPU)
  on the server as it arrives; the model needs ~0.3 s of look-ahead, which
  server-side synthesis provides for free. Each blendshape frame is sent on
  an unordered data channel in the same 20 ms tick as its audio, and the
  browser delays it by the measured jitter-buffer delay. Measured end to
  end, the jaw leads the sound by 80 ms versus 90 ms for the model alone on
  the same audio (speakers open the mouth before the sound), i.e. transport
  adds about 10 ms of error.
- **Missed endpoints.** If the recognizer never closes a sentence, the last
  interim transcript is answered after 1.2 s of no change.

## Measured latency

From the end of your speech to the first reply frame leaving the server, real
providers, Chrome client on localhost (October 2026, 30+ turns):

| | p50 | p90 |
|---|---|---|
| Recognizer closes the sentence | ~0.6 s | ~0.8 s |
| + LLM first token (DeepSeek, thinking off) | ~0.6 s | ~0.9 s |
| + first audio (CosyVoice v3 flash) | ~0.4 s | ~0.5 s |
| **Total** | **~1.7 s** | **~2.0 s** |

With the avatar on, the first sentence waits for ~0.5 s of audio so its
mouth shapes are computed before it plays: +~0.12 s on average.

Barge-in, measured on 10 interruptions with -50 dBFS room noise mixed in,
no camera: 10/10 yield and stop; the assistant yields ~0.15 s after you
become audible and stops ~1.3 s after (p50; 0.48 s / 1.58 s counting from
the start of each clip, which carries ~0.3 s of lead-in silence). With the
camera seeing you talk, one character confirms and stopping is ~0.5 s.

## Tests

```bash
make test                                              # unit + in-process WebRTC
set -a; . ./.env; set +a
HTTP_PROXY= HTTPS_PROXY= TALKTALK_IT=1 go test ./internal/llm ./internal/asr ./internal/tts -run Integration -v
```

Integration tests call the real providers. Domestic endpoints are dialed
without the environment proxy unless `TALKTALK_LLM_USE_PROXY=true`.

## Layout

```
cmd/talktalk/          entrypoint and wiring
internal/dialog/       conversation engine: turns, barge-in, segmentation, latency
internal/session/      one peer + playout + engine per browser connection
internal/rtc/          Pion WebRTC peer (audio track + events data channel)
internal/playout/      20 ms paced Opus playout with progress marks and ducking
internal/vad/          energy voice-activity detector for soft barge-in
internal/lipsync/      streaming audio→ARKit blendshapes (wav2arkit on onnxruntime)
internal/asr/          streaming recognition (DashScope)
internal/tts/          sentence synthesis (CosyVoice over DashScope)
internal/llm/          OpenAI-compatible streaming chat
internal/dashscope/    DashScope duplex WebSocket task protocol
internal/audio/        PCM helpers, resampler and the libopus binding
internal/server/       HTTP API: sessions, health, static client
internal/web/static/   browser client (see DESIGN.md)
```

## Roadmap

| Milestone | Scope |
|---|---|
| M0 | Go skeleton, config, health endpoint, CI |
| M1 | WebRTC voice loop: streaming ASR → LLM → streaming TTS, barge-in |
| M2 | Browser Gaussian-splat avatar driven by audio-to-expression |
| M3 | Embodied perception: the avatar watches and reacts to your face |
| M4 | DualTalk baseline and data pipeline for listening behaviour |
| M5 | LeJEPA conversation-state encoder (turn-taking, surprise) |
| M6 | Causal generative motion model (gated on M4/M5) |
| M7 | Optional photoreal renderer on a rented GPU |

## License

[MIT](LICENSE)
