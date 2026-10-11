#!/usr/bin/env bash
# 下载 onnxruntime 动态库并生成 CGO 环境变量脚本，用于 `-tags onnx` 构建。
# 用法：bash scripts/setup-onnx.sh [目标目录]（默认 .cache/onnxruntime）
set -euo pipefail

TARGET_DIR="${1:-.cache/onnxruntime}"
ORT_VERSION="${ORT_VERSION:-1.19.2}"

case "$(uname -s)-$(uname -m)" in
  Darwin-arm64) ORT_PKG="onnxruntime-macos-arm64-${ORT_VERSION}" ;;
  Darwin-x86_64) ORT_PKG="onnxruntime-macos-x86_64-${ORT_VERSION}" ;;
  Linux-x86_64) ORT_PKG="onnxruntime-linux-x64-${ORT_VERSION}" ;;
  Linux-aarch64) ORT_PKG="onnxruntime-linux-aarch64-${ORT_VERSION}" ;;
  *)
    echo "不支持的平台：$(uname -s)-$(uname -m)，请手动安装 onnxruntime 并设置 CGO 变量" >&2
    exit 1
    ;;
esac

mkdir -p "${TARGET_DIR}"
if [ ! -d "${TARGET_DIR}/${ORT_PKG}" ]; then
  URL="https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VERSION}/${ORT_PKG}.tgz"
  echo "下载 ${URL}"
  curl -fL --retry 3 "${URL}" -o "${TARGET_DIR}/${ORT_PKG}.tgz"
  tar -xzf "${TARGET_DIR}/${ORT_PKG}.tgz" -C "${TARGET_DIR}"
fi

ORT_ROOT="${TARGET_DIR}/${ORT_PKG}"
cat > "${TARGET_DIR}/env.sh" <<EOF
export CGO_ENABLED=1
export CGO_CFLAGS="-I${ORT_ROOT}/include"
export CGO_LDFLAGS="-L${ORT_ROOT}/lib -lonnxruntime -Wl,-rpath,${ORT_ROOT}/lib"
EOF

cat <<EOF
onnxruntime 已就绪：${ORT_ROOT}
构建命令：
  source ${TARGET_DIR}/env.sh
  go build -tags onnx -o bin/fitpose-server-onnx ./cmd/server

姿态模型（如 MoveNet）需另行准备，放到 models/ 后启动：
  ./bin/fitpose-server-onnx --provider onnx --model models/pose-model.onnx \\
    --input-name input --output-name output --input-size 256
EOF
