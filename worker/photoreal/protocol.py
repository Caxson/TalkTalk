"""Wire protocol between the TalkTalk server and the photoreal worker.

One TCP connection per conversation. Every message is a 1-byte type, a
4-byte big-endian payload length, and the payload:

  server -> worker
    S  start   JSON {"character": "<id>"}
    A  audio   PCM16 little-endian, mono, 16 kHz: the audio the listener
               will hear, sent ahead of playback, silence included
    X  stop    empty

  worker -> server
    R  ready   JSON {"width", "height", "fps", "first_frame"}: frames now
               follow; first_frame is the index of the first one
    V  video   uint32 frame index (big-endian) + one H.264 access unit
               (Annex B); frame k shows the audio at samples [k*640, (k+1)*640)
               of the stream the server sent
    E  error   JSON {"error": "..."}
"""

import asyncio
import json
import struct

HEADER = struct.Struct(">cI")
MAX_PAYLOAD = 8 << 20  # an access unit is well under this

START, AUDIO, STOP = b"S", b"A", b"X"
READY, VIDEO, ERROR = b"R", b"V", b"E"

SAMPLE_RATE = 16000
FPS = 25
SAMPLES_PER_FRAME = SAMPLE_RATE // FPS  # 640


async def read_message(reader: asyncio.StreamReader) -> tuple[bytes, bytes]:
    kind, size = HEADER.unpack(await reader.readexactly(HEADER.size))
    if size > MAX_PAYLOAD:
        raise ValueError(f"message too large: {size} bytes")
    return kind, await reader.readexactly(size)


def pack(kind: bytes, payload: bytes = b"") -> bytes:
    return HEADER.pack(kind, len(payload)) + payload


def pack_json(kind: bytes, obj: dict) -> bytes:
    return pack(kind, json.dumps(obj).encode())


def pack_video(index: int, access_unit: bytes) -> bytes:
    return pack(VIDEO, struct.pack(">I", index) + access_unit)
