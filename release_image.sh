#!/usr/bin/env bash
# 在本地 Docker daemon 上构建镜像，导出为压缩包并上传到目标服务器。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 可用环境变量覆盖默认的上传目标
TARGET="${TARGET:-xueshiji@raspberrypi}"

# 忽略外部环境的 DOCKER_HOST，确保构建和导出都发生在本地 daemon
unset DOCKER_HOST

IMAGE="weishaw/sub2api:latest"
TARBALL="${SCRIPT_DIR}/images.tgz"

VERSION="$("${SCRIPT_DIR}/backend/scripts/resolve-version.sh" 2>/dev/null || echo dev)"
COMMIT="$(git -C "${SCRIPT_DIR}" rev-parse --short HEAD)"

echo "==> 构建 ${IMAGE} (VERSION=${VERSION}, COMMIT=${COMMIT})"
docker buildx build \
    --build-arg VERSION="${VERSION}" \
    --build-arg COMMIT="${COMMIT}" \
    -t "${IMAGE}" \
    --load \
    "${SCRIPT_DIR}"

echo "==> 导出镜像到 ${TARBALL}"
docker save "${IMAGE}" | pigz >"${TARBALL}"

echo "==> 上传到 ${TARGET}:~/"
scp "${TARBALL}" "${TARGET}:~/"

echo "==> 完成"
