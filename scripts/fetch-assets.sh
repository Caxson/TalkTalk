#!/usr/bin/env bash
# Downloads the avatar assets into ./assets (git-ignored) and verifies them.
#
#   models/wav2arkit_cpu.onnx(.data)   lip-sync model, ~404 MB, Apache-2.0
#                                      (huggingface.co/myned-ai/wav2arkit_cpu)
#   vendor/gaussian-splat-renderer-for-lam.module.js
#                                      LAM WebGL renderer, MIT (npm)
#   avatars/AvatarSample_B.vrm         pixiv VRoid sample avatar (VRM 1.0); its
#                                      license allows use as an avatar, edits
#                                      and free redistribution, no credit needed
#   avatars/idle_loop.vrma             idle motion from pixiv/ChatVRM (MIT)
#   avatars/status.zip                 LAM sample from OpenAvatarChat
#                                      (Apache-2.0; LAM weights CC BY-NC 4.0)
#   avatars/hiyori/...                 Live2D sample "Hiyori", Free Material
#                                      License: small personal/indie use only,
#                                      no redistribution, no edits to design
#   vendor/live2d/live2dcubismcore.min.js
#                                      Live2D Cubism Core, Live2D Proprietary
#                                      Software License (fetched, not committed)
#   vendor/live2d/cubism.es.js, vendor/pixi/pixi.min.mjs
#                                      untitled-pixi-live2d-engine 1.4.0 and
#                                      PixiJS 8.20.1 (both MIT)
#   vendor/three/..., vendor/three-vrm*.module.min.js
#                                      three.js 0.186.1 and @pixiv/three-vrm
#                                      3.5.5 (both MIT) for the VRM renderer
#   vendor/mediapipe/...               MediaPipe tasks-vision 1.1.0 and the
#                                      face landmarker model (Apache-2.0); the
#                                      camera is analysed in the browser only
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

CHATVRM="https://raw.githubusercontent.com/pixiv/ChatVRM/b542aa00e19dccf9fc48ba340cf7eee011d2329a/public"
fetch "$CHATVRM/AvatarSample_B.vrm" \
  "$DEST/avatars/AvatarSample_B.vrm" ffbd8c92a9e67c0a948f69c7a2eec91e5c282c9ae70e9184309fc164d74cbc27
fetch "$CHATVRM/idle_loop.vrma" \
  "$DEST/avatars/idle_loop.vrma" ace95ba6dcc0bdf2ed1081c002332b4184441117c8d543b6f642b3d2c5cf99be

# Cast: more characters (see internal/cast/builtin.go).
fetch "https://raw.githubusercontent.com/HumanAIGC-Engineering/OpenAvatarChat/8b7b3b45bca28ae9ab5fa72ee31bc03c5a99b08b/src/handlers/client/ws_lam_client/lam_samples/status.zip" \
  "$DEST/avatars/status.zip" 59a9ff8e7dc11eacdb321726148442a31ba18ee919a8559c9a0011c169c59e65

HIYORI="https://raw.githubusercontent.com/Live2D/CubismWebSamples/b1de66b0b1f1cb881d95fb6158622aeb6a2827bd/Samples/Resources/Hiyori"
while read -r want f; do
  fetch "$HIYORI/$f" "$DEST/avatars/hiyori/$f" "$want"
done <<'FILES'
973cb3392f7b01f5f73e9d3fedc495a81a0f3ee8014e5df72ef943311c56ee12 Hiyori.model3.json
0323f5f377b9afef54318be09e9b4eea2a8cb266190ff9d2f843f95ecb77716c Hiyori.moc3
c7a6d641893519c0bbc615545887f03f6c5b8031b4096873740b618370452fc1 Hiyori.physics3.json
ff1cc242790f196c87a7379dc1340b608723db60af1b2825e00da8f351ffc1bf Hiyori.pose3.json
75fe50a89ea695b46c98dce8991c2f3d67e3d7a479786e6310460289b552d29b Hiyori.userdata3.json
0fc3fc76b7a68136056c8832c930c8af9f93899f359eded7349b481ea2342d03 Hiyori.cdi3.json
560aea0d910dc04950d669640b2ef17c0d817969e9909f477f166bb5cbd8ffbe Hiyori.2048/texture_00.png
87fe9ab7db81ab3025e0407229e449233581e00fa92c8e911923bc6c7d98ce84 Hiyori.2048/texture_01.png
c4f0d982c685b81c1af0ced96c8f6bd5118b8a3fa972c3a12a46652b8d78461d motions/Hiyori_m01.motion3.json
858c582874a0c4badfec38d4caf19e232fea5ba714f5a2ac6a282c617910c154 motions/Hiyori_m02.motion3.json
de33a8ce78226a5ff80cfa941fb5a4c50b6c1c4b346031ed44b384f772b62cf9 motions/Hiyori_m03.motion3.json
54ddb94d0494772e10355429c547bb7e29601f38d663087804b0c793b29cc51f motions/Hiyori_m04.motion3.json
7f71061c9207ac1da6e06098ede9318307a7e75715a3ef97c82646acd9ee8620 motions/Hiyori_m05.motion3.json
55c007646febe4cb76d20cbb0623086986dbb55c7ef82212693a5e26636a32ef motions/Hiyori_m06.motion3.json
3f06cb70f5654eed3389978885e4dff151b6cf9e8d59248680addcd267ff793a motions/Hiyori_m07.motion3.json
66022fff5cff3c30b7fc1295bf92e4975e3ab9980d5cb296b5e24a2a8acc6daf motions/Hiyori_m08.motion3.json
9edbd6a88e977221fa90625b5f94c9fd27212e1183288105584be8272543ade7 motions/Hiyori_m09.motion3.json
ba0ba6764a80459432ffd48d6bd5ba261fd25f196208ade3016be74efb9d3dfc motions/Hiyori_m10.motion3.json
FILES
# Cubism 5.2 Core: the engine reads drawables.renderOrders, which the 5.3
# Core (the unversioned "latest" URL) moved, so the 5.2 line is pinned.
fetch https://cubism.live2d.com/sdk-web/core/05/live2dcubismcore.min.js \
  "$DEST/vendor/live2d/live2dcubismcore.min.js" 25ae938cb4fe282ce189b357bcc97e603d1e1f7ec78bf04150d401c23cdc792f

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

npm_files https://registry.npmjs.org/untitled-pixi-live2d-engine/-/untitled-pixi-live2d-engine-1.4.0.tgz \
  "dist/cubism.es.js $V/live2d/cubism.es.js 4afe8c561d06b6f5c3bd5b4eeb67362cf54e3c12b5e84433e16429dde3159091"
npm_files https://registry.npmjs.org/pixi.js/-/pixi.js-8.20.1.tgz \
  "dist/pixi.min.mjs $V/pixi/pixi.min.mjs efc469a3839f80d6b55f8041c498e84fa271991166d45f37c9d8511f914a65f4"

MP="$V/mediapipe"
npm_files https://registry.npmjs.org/@mediapipe/tasks-vision/-/tasks-vision-1.1.0.tgz \
  "vision_bundle.mjs $MP/vision_bundle.mjs 9d5fc9ef74b22329cf9aa88dc543956d192b8c254210b187271614713eb5b10c" \
  "wasm/vision_wasm_internal.js $MP/wasm/vision_wasm_internal.js 7c652617b5bef832f42878ce8c5f752b0724bca6054e4bc1013a37d4b1949723" \
  "wasm/vision_wasm_internal.wasm $MP/wasm/vision_wasm_internal.wasm 782eda3ec1f414fdb4f1d66a8b59093eb4d5ab437e06117796abf05962d68eef"
fetch https://storage.googleapis.com/mediapipe-models/face_landmarker/face_landmarker/float16/1/face_landmarker.task \
  "$MP/face_landmarker.task" 64184e229b263107bc2b804c6625db1341ff2bb731874b0bcc2fe6544e0bc9ff

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

if ! grep -qs '^TALKTALK_LIPSYNC_MODEL=' .env; then
  echo
  echo "Add to .env:"
  echo "  TALKTALK_LIPSYNC_MODEL=$DEST/models/wav2arkit_cpu.onnx"
fi
