#!/usr/bin/env bash
# Downloads the avatar assets into ./assets (git-ignored) and verifies them.
#
#   models/wav2arkit_cpu.onnx(.data)   lip-sync model, ~404 MB, Apache-2.0
#                                      (huggingface.co/myned-ai/wav2arkit_cpu)
#   vendor/gaussian-splat-renderer-for-lam.module.js
#                                      LAM WebGL renderer, MIT (npm)
#   avatars/p2-1.zip                   sample avatar from aigc3d/LAM_WebRender
#   avatars/AvatarSample_B.vrm         pixiv VRoid sample avatar (VRM 1.0); its
#                                      license allows use as an avatar, edits
#                                      and free redistribution, no credit needed
#   avatars/idle_loop.vrma             idle motion from pixiv/ChatVRM (MIT)
#   vendor/three/..., vendor/three-vrm*.module.min.js
#                                      three.js 0.186.1 and @pixiv/three-vrm
#                                      3.5.5 (both MIT) for the VRM renderer
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

CHATVRM="https://raw.githubusercontent.com/pixiv/ChatVRM/b542aa00e19dccf9fc48ba340cf7eee011d2329a/public"
fetch "$CHATVRM/AvatarSample_B.vrm" \
  "$DEST/avatars/AvatarSample_B.vrm" ffbd8c92a9e67c0a948f69c7a2eec91e5c282c9ae70e9184309fc164d74cbc27
fetch "$CHATVRM/idle_loop.vrma" \
  "$DEST/avatars/idle_loop.vrma" ace95ba6dcc0bdf2ed1081c002332b4184441117c8d543b6f642b3d2c5cf99be

# npm_files tarball-url "path-in-package dest sha256" ...
# Extracts single files from an npm tarball, verifying each one.
npm_files() {
  local url=$1; shift
  local need=()
  for spec in "$@"; do
    read -r _ dest want <<<"$spec"
    if [[ ! -f "$dest" || "$(sha "$dest")" != "$want" ]]; then need+=("$spec"); else echo "ok       $dest"; fi
  done
  [[ ${#need[@]} -eq 0 ]] && return
  local tmp; tmp=$(mktemp -d)
  echo "fetching $url"
  curl -fL --retry 3 -s -o "$tmp/p.tgz" "$url"
  tar -xzf "$tmp/p.tgz" -C "$tmp"
  for spec in "${need[@]}"; do
    read -r src dest want <<<"$spec"
    if [[ "$(sha "$tmp/package/$src")" != "$want" ]]; then
      echo "checksum mismatch for $src in $url" >&2; rm -rf "$tmp"; exit 1
    fi
    mkdir -p "$(dirname "$dest")"
    cp "$tmp/package/$src" "$dest"
    echo "ok       $dest"
  done
  rm -rf "$tmp"
}

V="$DEST/vendor"
npm_files https://registry.npmjs.org/three/-/three-0.186.1.tgz \
  "build/three.module.js $V/three/three.module.js 9052042d676cb0fdc1ddfefe193053f34b7ac0513a616fdac4535d49987812ea" \
  "build/three.core.js $V/three/three.core.js 9edde002b066a9a05676a6127f67735b62baf399bdea529f2f7e31657da769e6" \
  "examples/jsm/loaders/GLTFLoader.js $V/three/addons/loaders/GLTFLoader.js 131c0f78c01d19368ae495caa65b3adaa10487810a36a05bb5901b769a35ac16" \
  "examples/jsm/utils/BufferGeometryUtils.js $V/three/addons/utils/BufferGeometryUtils.js 9fb63427ce6641fa14fd0baff9cc4d1b5f9c3d85fd084bf2e90e803c44ec1797" \
  "examples/jsm/utils/SkeletonUtils.js $V/three/addons/utils/SkeletonUtils.js b1632a703206c3d830de9fcbe515696770d04b71a15ee6b50afa6d2c3298c86f"
npm_files https://registry.npmjs.org/@pixiv/three-vrm/-/three-vrm-3.5.5.tgz \
  "lib/three-vrm.module.min.js $V/three-vrm.module.min.js 99f943e956e52dbf52bbe418899e8a8ae5e35f5c79a94ab8e404926afc620bf2"
npm_files https://registry.npmjs.org/@pixiv/three-vrm-animation/-/three-vrm-animation-3.5.5.tgz \
  "lib/three-vrm-animation.module.min.js $V/three-vrm-animation.module.min.js 31de9586a36a8ad806303b84358f11d74ce70ac1d2656b04e8a5da7a913aa907"

RENDERER="$DEST/vendor/gaussian-splat-renderer-for-lam.module.js"
RENDERER_SHA=4800485f5d588c876a427e4fc9a57727c90f8a68e6ea910fc5140a73803ae983
if [[ -f "$RENDERER" && "$(sha "$RENDERER")" == "$RENDERER_SHA" ]]; then
  echo "ok       $RENDERER"
else
  echo "fetching $RENDERER"
  tmp=$(mktemp -d)
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
  rm -rf "$tmp"
fi

echo
echo "Add to .env:"
echo "  TALKTALK_LIPSYNC_MODEL=$DEST/models/wav2arkit_cpu.onnx"
