#!/usr/bin/env bash
# 在远端 Docker daemon 上构建镜像，导出为压缩包并上传到目标服务器。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 可用环境变量覆盖默认的远端 daemon 与上传目标
DOCKER_HOST="${DOCKER_HOST:-ssh://192.168.137.2}"
TARGET="${TARGET:-root@10.18.12.63}"

IMAGE="weishaw/sub2api:latest"
TARBALL="${SCRIPT_DIR}/images.tgz"

VERSION="$("${SCRIPT_DIR}/backend/scripts/resolve-version.sh" 2>/dev/null || echo dev)"
COMMIT="$(git -C "${SCRIPT_DIR}" rev-parse --short HEAD)"

echo "==> 构建 ${IMAGE} (VERSION=${VERSION}, COMMIT=${COMMIT}, DOCKER_HOST=${DOCKER_HOST})"
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
