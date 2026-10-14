"""Exercise a running worker like the TalkTalk server does: stream a WAV in
real time (20 ms chunks, with silence around it), collect the frames, and
report per-frame latency (frame received vs. its audio sent). Writes the
frames with the audio to an mp4 for a visual check.

    python probe_client.py --addr 127.0.0.1:9800 --character rin --wav x.wav --out probe.mp4
"""

import argparse
import asyncio
import json
import struct
import subprocess
import time
import wave

import numpy as np

import protocol as P


async def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--addr", default="127.0.0.1:9800")
    ap.add_argument("--character", default="rin")
    ap.add_argument("--wav", required=True)
    ap.add_argument("--out", default="probe.mp4")
    ap.add_argument("--lead", type=float, default=8.0, help="seconds of silence before the speech")
    ap.add_argument("--tail", type=float, default=3.0)
    args = ap.parse_args()

    with wave.open(args.wav) as w:
        assert w.getframerate() == P.SAMPLE_RATE and w.getnchannels() == 1
        speech = np.frombuffer(w.readframes(w.getnframes()), dtype="<i2")
    silence = lambda s: np.zeros(int(s * P.SAMPLE_RATE), dtype="<i2")
    audio = np.concatenate([silence(args.lead), speech, silence(args.tail)])

    host, port = args.addr.rsplit(":", 1)
    reader, writer = await asyncio.open_connection(host, int(port))
    writer.write(P.pack_json(P.START, {"character": args.character}))
    sent_at = {}           # frame index -> time its last sample was sent
    frames, arrived = {}, {}
    info = {}

    async def receive():
        while True:
            kind, payload = await P.read_message(reader)
            if kind == P.READY:
                info.update(json.loads(payload))
                print("ready", info, flush=True)
            elif kind == P.VIDEO:
                k = struct.unpack(">I", payload[:4])[0]
                frames[k], arrived[k] = payload[4:], time.time()
            elif kind == P.ERROR:
                print("error", payload.decode(), flush=True)

    rx = asyncio.create_task(receive())
    chunk = P.SAMPLE_RATE // 50  # 20 ms
    t0 = time.time()
    for i in range(0, len(audio), chunk):
        writer.write(P.pack(P.AUDIO, audio[i:i + chunk].tobytes()))
        end = i + chunk
        if end % P.SAMPLES_PER_FRAME == 0:
            sent_at[end // P.SAMPLES_PER_FRAME - 1] = time.time()
        await writer.drain()
        await asyncio.sleep(max(0, t0 + (i + chunk) / P.SAMPLE_RATE - time.time()))
    await asyncio.sleep(3)
    writer.write(P.pack(P.STOP)); await writer.drain()
    rx.cancel()

    lat = sorted(arrived[k] - sent_at[k] for k in frames if k in sent_at)
    if not lat:
        print("no frames"); return
    q = lambda p: lat[min(len(lat) - 1, int(p * len(lat)))]
    first, last = min(frames), max(frames)
    print(f"frames {len(frames)} ({first}..{last}, missing {last - first + 1 - len(frames)}), "
          f"latency p50 {q(.5):.2f}s p95 {q(.95):.2f}s max {lat[-1]:.2f}s", flush=True)

    # Mux: the frames from `first` on, with the matching audio.
    h264 = b"".join(frames[k] for k in range(first, last + 1) if k in frames)
    open(args.out + ".h264", "wb").write(h264)
    a = audio[first * P.SAMPLES_PER_FRAME:(last + 1) * P.SAMPLES_PER_FRAME]
    with wave.open(args.out + ".wav", "wb") as w:
        w.setnchannels(1); w.setsampwidth(2); w.setframerate(P.SAMPLE_RATE); w.writeframes(a.tobytes())
    subprocess.run(["ffmpeg", "-loglevel", "error", "-y", "-r", str(P.FPS), "-i", args.out + ".h264",
                    "-i", args.out + ".wav", "-c:v", "copy", "-c:a", "aac", "-shortest", args.out], check=True)
    print("wrote", args.out)


asyncio.run(main())
