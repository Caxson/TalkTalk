#!/bin/bash
# Sets up the photoreal worker on an AutoDL GPU instance (Ubuntu 22.04,
# miniconda, CUDA 12.x driver), from nothing:
#
#   ROOT=/root/autodl-tmp bash setup_autodl.sh
#
# ENV is a conda env name, or an absolute path to put the env there (e.g. on
# the data disk when the system disk is small).
#
# Each step works around something that broke on the first install:
#   - PyPI through the Tsinghua mirror (download.pytorch.org crawled via the
#     academic proxy); torch 2.5.1 on PyPI is the CUDA 12.4 build.
#   - TensorRT 8.6.1 (what Ditto targets): the `tensorrt` meta-package fails
#     to build, and PyPI's tensorrt_bindings is an empty placeholder, so the
#     real wheels come from NVIDIA's China mirror and a small `tensorrt`
#     alias module points at them.
#   - TensorRT 8.6 needs cuDNN 8 while torch 2.5 ships cuDNN 9 under the same
#     pip name: cuDNN 8's libraries are unpacked apart and preloaded by the
#     alias.
#   - mediapipe needs EGL/GLES system libraries.
#   - Engines are built here from Ditto's ONNX models (hf-mirror throttles
#     the prebuilt engine downloads, and local builds suit this GPU).
#   - `hf download --include` missed files; plain curl from hf-mirror works.
set -euo pipefail

ROOT=${ROOT:-/root/autodl-tmp}
ENV=${ENV:-ditto}
HERE=$(cd "$(dirname "$0")" && pwd)
MIRROR="-i https://pypi.tuna.tsinghua.edu.cn/simple --progress-bar off"
HF=https://hf-mirror.com/digital-avatar/ditto-talkinghead/resolve/main
mkdir -p "$ROOT"
cd "$ROOT"

echo "== system libraries"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null
apt-get install -y -qq libegl1 libgles2 libgl1 ffmpeg >/dev/null

echo "== python environment ($ENV)"
set +u  # conda's activation scripts read unset variables
source /root/miniconda3/bin/activate
export PIP_CACHE_DIR="$ROOT/.pipcache"
if [[ "$ENV" == /* ]]; then
  [ -x "$ENV/bin/python" ] || conda create -y -q -p "$ENV" python=3.10 >/dev/null
else
  conda env list | grep -q "^$ENV " || conda create -y -q -n "$ENV" python=3.10 >/dev/null
fi
conda activate "$ENV"
set -u
pip install $MIRROR torch==2.5.1 torchvision==0.20.1 torchaudio==2.5.1
pip install $MIRROR librosa tqdm filetype imageio opencv_python_headless scikit-image cython \
  imageio-ffmpeg colored numpy==2.0.1 einops mediapipe av polygraphy cuda-python==12.4.0

echo "== TensorRT 8.6.1 and cuDNN 8"
for pkg in tensorrt-bindings:tensorrt_bindings-8.6.1-cp310-none-manylinux_2_17_x86_64.whl \
           tensorrt-libs:tensorrt_libs-8.6.1-py2.py3-none-manylinux_2_17_x86_64.whl; do
  pip install --progress-bar off --no-deps "https://pypi.nvidia.cn/${pkg%%:*}/${pkg##*:}"
done
mkdir -p "$ROOT/cudnn8"
if ! ls "$ROOT"/cudnn8/libcudnn.so.8 >/dev/null 2>&1; then
  tmp=$(mktemp -d)
  pip download $MIRROR --no-deps -d "$tmp" nvidia-cudnn-cu12==8.9.7.29
  python - "$tmp" "$ROOT/cudnn8" <<'PY'
import glob, os, sys, zipfile
z = zipfile.ZipFile(glob.glob(os.path.join(sys.argv[1], "nvidia_cudnn*.whl"))[0])
for n in z.namelist():
    if "/lib/" in n and ".so.8" in n:
        open(os.path.join(sys.argv[2], os.path.basename(n)), "wb").write(z.read(n))
PY
  rm -rf "$tmp"
fi
SITE=$(python -c "import site; print(site.getsitepackages()[0])")
mkdir -p "$SITE/tensorrt"
cat > "$SITE/tensorrt/__init__.py" <<PY
# Alias for the TensorRT 8.6.1 standalone wheels (see setup_autodl.sh):
# the code is in tensorrt_bindings; cuDNN 8 lives apart from torch's cuDNN 9
# and is preloaded here.
import ctypes as _ct, glob as _g
for _lib in sorted(_g.glob("$ROOT/cudnn8/libcudnn*.so.8"), key=lambda p: "ops" not in p):
    _ct.CDLL(_lib, mode=_ct.RTLD_GLOBAL)
from tensorrt_bindings import *
from tensorrt_bindings import __version__
PY
python -c "import torch, tensorrt; from cuda import cudart; print('torch', torch.__version__, 'cuda', torch.cuda.is_available(), 'trt', tensorrt.__version__)"

echo "== Ditto code and models"
if [ ! -d ditto/.git ]; then
  (set +u; source /etc/network_turbo >/dev/null 2>&1 || true; git clone -q --depth 1 https://github.com/antgroup/ditto-talkinghead ditto)
fi
D=ditto/checkpoints
FILES=$(curl -s https://hf-mirror.com/api/models/digital-avatar/ditto-talkinghead | python -c \
  "import json,sys; print(' '.join(s['rfilename'] for s in json.load(sys.stdin)['siblings'] if s['rfilename'].startswith(('ditto_cfg/', 'ditto_onnx/'))))")
for f in $FILES; do
  mkdir -p "$D/$(dirname "$f")"
  for try in 1 2 3 4 5; do
    [ -s "$D/$f" ] && break
    curl -sfL -o "$D/$f.part" "$HF/$f" && mv "$D/$f.part" "$D/$f" || sleep $((try * 5))
  done
  [ -s "$D/$f" ] || { echo "download failed: $f" >&2; exit 1; }
done

echo "== TensorRT engines (built for this GPU)"
if [ "$(ls "$D/ditto_trt_custom" 2>/dev/null | wc -l)" -lt 12 ]; then
  (cd ditto && python scripts/cvt_onnx_to_trt.py --onnx_dir ./checkpoints/ditto_onnx --trt_dir ./checkpoints/ditto_trt_custom)
fi
ls "$D/ditto_trt_custom" | wc -l

echo "== worker"
mkdir -p "$ROOT/worker" "$ROOT/avatars"
cp "$HERE"/{protocol.py,engine.py,server.py,probe_client.py} "$ROOT/worker/"
[ -f "$ROOT/worker/characters.json" ] || sed "s#/root/autodl-tmp#$ROOT#g" "$HERE/characters.example.json" > "$ROOT/worker/characters.json"
echo "SETUP_DONE: put idle clips in $ROOT/avatars/, then:"
echo "  cd $ROOT/worker && DITTO_DIR=$ROOT/ditto python server.py --listen 127.0.0.1:9800 --characters characters.json"
