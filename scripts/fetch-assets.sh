#!/usr/bin/env bash
# Downloads the avatar assets into ./assets (git-ignored) and verifies them.
#
#   models/wav2arkit_cpu.onnx(.data)   lip-sync model, ~404 MB, Apache-2.0
#                                      (huggingface.co/myned-ai/wav2arkit_cpu)
#   vendor/gaussian-splat-renderer-for-lam.module.js
#                                      LAM WebGL renderer, MIT (npm)
#   avatars/p2-1.zip                   sample avatar from aigc3d/LAM_WebRender
#
# Overseas hosts: keep HTTPS_PROXY set if you need one. HF_ENDPOINT can
# point at a Hugging Face mirror, e.g. https://hf-mirror.com.
set -euo pipefail

DEST="${TALKTALK_ASSETS_DIR:-assets}"
HF="${HF_ENDPOINT:-https://huggingface.co}"
RENDERER_VERSION="0.0.9-alpha.2"

sha() { shasum -a 256 "$1" | cut -d' ' -f1; }

fetch() { # url dest sha256
  local url=$1 out=$2 want=$3
  if [[ -f "$out" && "$(sha "$out")" == "$want" ]]; then
    echo "ok       $out"
    return
  fi
  echo "fetching $out"
  mkdir -p "$(dirname "$out")"
  curl -fL --retry 3 --progress-bar -o "$out.part" "$url"
  local got; got=$(sha "$out.part")
  if [[ "$got" != "$want" ]]; then
    rm -f "$out.part"
    echo "checksum mismatch for $out: got $got want $want" >&2
    exit 1
  fi
  mv "$out.part" "$out"
}

fetch "$HF/myned-ai/wav2arkit_cpu/resolve/main/wav2arkit_cpu.onnx" \
  "$DEST/models/wav2arkit_cpu.onnx" cdecbfad3915dd20b2f0718942d0b8894b2ee11edcc5a9a9da45d29a46af2ed9
fetch "$HF/myned-ai/wav2arkit_cpu/resolve/main/wav2arkit_cpu.onnx.data" \
  "$DEST/models/wav2arkit_cpu.onnx.data" c0f0364673c6e50be126b193e2b56809c16ac6bee4805aea9b8251ce53429bf8
fetch "https://github.com/aigc3d/LAM_WebRender/raw/main/asset/arkit/p2-1.zip" \
  "$DEST/avatars/p2-1.zip" 1f3fae0f9e3b137a065957f52e4761bda7f25f6531908b1bf1216e8a091097be

RENDERER="$DEST/vendor/gaussian-splat-renderer-for-lam.module.js"
RENDERER_SHA=4800485f5d588c876a427e4fc9a57727c90f8a68e6ea910fc5140a73803ae983
if [[ -f "$RENDERER" && "$(sha "$RENDERER")" == "$RENDERER_SHA" ]]; then
  echo "ok       $RENDERER"
else
  echo "fetching $RENDERER"
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  curl -fL --retry 3 -s -o "$tmp/r.tgz" \
    "https://registry.npmjs.org/gaussian-splat-renderer-for-lam/-/gaussian-splat-renderer-for-lam-$RENDERER_VERSION.tgz"
  tar -xzf "$tmp/r.tgz" -C "$tmp" package/build/gaussian-splat-renderer-for-lam.module.js package/LICENSE 2>/dev/null \
    || tar -xzf "$tmp/r.tgz" -C "$tmp" package/build/gaussian-splat-renderer-for-lam.module.js
  if [[ "$(sha "$tmp/package/build/gaussian-splat-renderer-for-lam.module.js")" != "$RENDERER_SHA" ]]; then
    echo "checksum mismatch for renderer" >&2
    exit 1
  fi
  mkdir -p "$DEST/vendor"
  mv "$tmp/package/build/gaussian-splat-renderer-for-lam.module.js" "$RENDERER"
fi

echo
echo "Add to .env:"
echo "  TALKTALK_LIPSYNC_MODEL=$DEST/models/wav2arkit_cpu.onnx"
