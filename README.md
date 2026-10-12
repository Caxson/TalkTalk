# TalkTalk

A real-time conversational digital human that listens as well as it talks.

TalkTalk is a Go control plane that holds a live voice conversation with you
through the browser: streaming speech recognition, an LLM, streaming speech
synthesis, and an avatar whose face and head are driven by a single stream of
motion parameters (ARKit 52 blendshapes + head pose). The avatar keeps
reacting while you speak, not only when it is its turn.

> Status: early development. Milestone M0 (project skeleton) is in place;
> nothing user-facing works yet.

## Quick start

Requires Go 1.23+.

```bash
cp .env.example .env
make run            # serves on :8080
curl localhost:8080/healthz
```

`make help` lists every target; `make check` runs what CI runs.

## Layout

```
cmd/talktalk/       entrypoint
internal/config/    env configuration, validated at startup
internal/server/    HTTP routing, middleware, graceful shutdown
internal/httpx/     shared JSON response envelope
internal/version/   build metadata
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
