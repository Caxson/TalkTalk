# TalkTalk

A real-time conversational digital human that listens as well as it talks.

TalkTalk is a Go control plane that holds a live voice conversation with you
through the browser: streaming speech recognition, an LLM, streaming speech
synthesis, and an avatar whose face and head are driven by a single stream of
motion parameters (ARKit 52 blendshapes + head pose). The avatar keeps
reacting while you speak, not only when it is its turn.

> Status: **M1** — a full voice conversation in the browser, without an
> avatar yet. Speak, get answered, talk over it to cut it off.

## Quick start

Requires Go 1.24+ (the toolchain is fetched automatically), cgo and libopus.

```bash
brew install opus pkg-config          # Debian/Ubuntu: apt install libopus-dev pkg-config
cp .env.example .env                  # fill TALKTALK_LLM_API_KEY and DASHSCOPE_API_KEY
make run                              # http://localhost:8080
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
- **Barge-in.** Two substantive characters over the assistant stop it; fillers
  like 嗯 / 啊 are treated as backchannel. History keeps only what was heard.
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

Barge-in currently takes about 1.5 s from when you start talking until the
assistant stops; this is bounded by interim recognition latency.

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
internal/playout/      20 ms paced Opus playout with progress marks
internal/asr/          streaming recognition (DashScope)
internal/tts/          sentence synthesis (CosyVoice over DashScope)
internal/llm/          OpenAI-compatible streaming chat
internal/dashscope/    DashScope duplex WebSocket task protocol
internal/audio/        PCM helpers and the libopus binding
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
