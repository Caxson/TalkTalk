"""Photoreal worker: renders talking video for TalkTalk conversations.

Listens on TCP (reach it through an SSH tunnel; the port is not public).
For each connection: on START it takes an engine process for the
character, streams the incoming audio to it in Ditto's windows, and sends
the encoded frames back, numbered so that frame k shows audio samples
[k*640, (k+1)*640) of everything the server sent, including audio that
arrived while the engine was still starting (those frames are skipped).

Engines take ~10 s to load and register the character's clip, so one per
character is kept warm: a call takes it and a replacement starts loading.

    python server.py --listen 127.0.0.1:9800 --characters characters.json
"""

import argparse
import asyncio
import json
import logging
import multiprocessing as mp
import os
import queue
import time

import numpy as np

import engine
import protocol as P

log = logging.getLogger("photoreal")

PAD = engine.CHUNK[0] * P.SAMPLES_PER_FRAME                 # past context before frame 0
HOP = engine.CHUNK[1] * P.SAMPLES_PER_FRAME                 # new audio per window
WINDOW = sum(engine.CHUNK) * P.SAMPLES_PER_FRAME + 80       # Ditto's window, as in its inference.py


class Engine:
    """A renderer process and its queues."""

    def __init__(self, character: dict):
        ctx = mp.get_context("spawn")
        self.in_q: mp.Queue = ctx.Queue()
        self.out_q: mp.Queue = ctx.Queue()
        self.proc = ctx.Process(target=engine.run, args=(character, 0, self.in_q, self.out_q), daemon=True)
        self.proc.start()


# Engines alive at once (in calls plus warm), ~2.3 GB of GPU memory each.
MAX_ENGINES = int(os.environ.get("PHOTOREAL_MAX_ENGINES", "4"))


class Pool:
    """Keeps one loaded engine waiting per character, within MAX_ENGINES."""

    def __init__(self, characters: dict):
        self.characters = characters
        self.warm: dict[str, Engine] = {}
        self.active = 0

    def _count(self) -> int:
        return self.active + sum(1 for e in self.warm.values() if e.proc.is_alive())

    def warm_up(self, cid: str):
        if cid in self.warm and self.warm[cid].proc.is_alive():
            return
        if self._count() >= MAX_ENGINES:
            log.info("not warming %s: %d engines alive (max %d)", cid, self._count(), MAX_ENGINES)
            return
        log.info("warming an engine for %s", cid)
        self.warm[cid] = Engine(self.characters[cid])

    def take(self, cid: str) -> Engine | None:
        eng = self.warm.pop(cid, None)
        if eng is not None and eng.proc.is_alive():
            log.info("using the warm engine for %s", cid)
        elif self._count() < MAX_ENGINES:
            eng = Engine(self.characters[cid])  # cold start
        else:
            return None
        self.active += 1
        self.warm_up(cid)  # the next call's, if there is room
        return eng

    def release(self, cid: str):
        self.active -= 1
        self.warm_up(cid)


class Session:
    """Audio in, frames out, for one conversation."""

    def __init__(self, eng: Engine, writer: asyncio.StreamWriter):
        self.writer = writer
        self.in_q: mp.Queue = eng.in_q
        self.out_q: mp.Queue = eng.out_q
        self.proc = eng.proc
        self.received = 0          # samples received from the server, ever
        self.ready = False
        self.buf = np.zeros(0, dtype=np.float32)
        self.first_frame = 0
        self.skip = 0              # samples still to drop to reach first_frame
        self.started = time.time()

    def push_audio(self, pcm: bytes):
        samples = np.frombuffer(pcm, dtype="<i2").astype(np.float32) / 32768.0
        self.received += len(samples)
        if not self.ready or not self.proc.is_alive():
            return  # nothing renders it (yet, or any more): no frames for it
        if self.skip:
            drop = min(self.skip, len(samples))
            samples, self.skip = samples[drop:], self.skip - drop
        self.buf = np.concatenate([self.buf, samples])
        while len(self.buf) >= WINDOW:
            self.in_q.put(self.buf[:WINDOW].copy())
            self.buf = self.buf[HOP:]

    def on_ready(self):
        # Start rendering at the next frame boundary of the received audio.
        self.skip = -self.received % P.SAMPLES_PER_FRAME
        self.first_frame = (self.received + self.skip) // P.SAMPLES_PER_FRAME
        self.buf = np.zeros(PAD, dtype=np.float32)  # Ditto's leading context, as in inference.py
        self.ready = True

    async def stop(self):
        try:
            self.in_q.put(None)
        except Exception:
            pass
        # Off the event loop: other conversations keep streaming meanwhile.
        await asyncio.get_running_loop().run_in_executor(None, self.proc.join, 5)
        if self.proc.is_alive():
            self.proc.kill()


async def pump_frames(sess: Session):
    """Forward engine output to the server."""
    loop = asyncio.get_running_loop()
    frames = 0
    while True:
        try:
            item = await loop.run_in_executor(None, sess.out_q.get, True, 0.5)
        except queue.Empty:
            if sess.proc is not None and not sess.proc.is_alive():
                return
            continue
        kind = item[0]
        if kind == "ready":
            sess.on_ready()
            log.info("engine ready in %.1fs: %dx%d, first frame %d", time.time() - sess.started, item[1], item[2], sess.first_frame)
            sess.writer.write(P.pack_json(P.READY, {"width": item[1], "height": item[2], "fps": P.FPS, "first_frame": sess.first_frame}))
        elif kind == "frame":
            sess.writer.write(P.pack_video(sess.first_frame + item[1], item[2]))
            frames += 1
        elif kind == "error":
            log.error("engine failed:\n%s", item[1])
            sess.writer.write(P.pack_json(P.ERROR, {"error": item[1].strip().splitlines()[-1]}))
        elif kind == "done":
            log.info("engine done after %d frames", frames)
            return
        await sess.writer.drain()


async def handle(reader: asyncio.StreamReader, writer: asyncio.StreamWriter, characters: dict, pool: Pool):
    peer = writer.get_extra_info("peername")
    sess, pump, cid = None, None, None
    try:
        while True:
            kind, payload = await P.read_message(reader)
            if kind == P.START and sess is None:
                cid = json.loads(payload).get("character", "")
                if cid not in characters:
                    writer.write(P.pack_json(P.ERROR, {"error": f"unknown character {cid!r}"}))
                    await writer.drain()
                    return
                log.info("%s: start %s", peer, cid)
                # The engine numbers frames from 0; READY tells the server which
                # audio frame that is (rendering starts where the audio stood then).
                eng = pool.take(cid)
                if eng is None:
                    writer.write(P.pack_json(P.ERROR, {"error": "worker busy: no GPU capacity for another call"}))
                    await writer.drain()
                    cid = None
                    return
                sess = Session(eng, writer)
                pump = asyncio.create_task(pump_frames(sess))
            elif kind == P.AUDIO and sess is not None:
                sess.push_audio(payload)
            elif kind == P.STOP:
                return
    except (asyncio.IncompleteReadError, ConnectionError):
        pass
    finally:
        log.info("%s: closed", peer)
        if pump is not None:
            pump.cancel()
        if sess is not None:
            await sess.stop()
            pool.release(cid)
        writer.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--listen", default="127.0.0.1:9800")
    ap.add_argument("--characters", default="characters.json")
    args = ap.parse_args()
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s %(message)s")
    with open(args.characters) as f:
        characters = json.load(f)
    host, port = args.listen.rsplit(":", 1)

    async def serve():
        pool = Pool(characters)
        for cid in characters:
            pool.warm_up(cid)
        server = await asyncio.start_server(lambda r, w: handle(r, w, characters, pool), host, int(port))
        log.info("listening on %s, characters: %s", args.listen, ", ".join(characters))
        async with server:
            await server.serve_forever()

    asyncio.run(serve())


if __name__ == "__main__":
    main()
