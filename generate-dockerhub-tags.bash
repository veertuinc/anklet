#!/bin/bash
set -exo pipefail
echo "This script is EOL. We don't publish to dockerhub anymore."
exit 1
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
pushd "${SCRIPT_DIR}"
DOCKERFILE_PATH="${SCRIPT_DIR}/docker"
IMAGE_NAME="veertu/anklet"
BIN_NAME="anklet"
TAG=latest
cleanup() {
  rm -f "${DOCKERFILE_PATH}/${BIN_NAME}"*
  docker buildx rm mybuilder || true
}
pushd dist
for zipfile in *.zip; do
    if [ -f "$zipfile" ]; then
        unzip -f "$zipfile"
    fi
done
popd
cp -f dist/anklet_v$(cat "${SCRIPT_DIR}"/VERSION)_linux_amd64 "${DOCKERFILE_PATH}/${BIN_NAME}_linux_amd64"
cp -f dist/anklet_v$(cat "${SCRIPT_DIR}"/VERSION)_linux_arm64 "${DOCKERFILE_PATH}/${BIN_NAME}_linux_arm64"
ls -alht "${DOCKERFILE_PATH}/"
trap cleanup EXIT
pushd "${DOCKERFILE_PATH}"
docker buildx create --name mybuilder --use desktop-linux || true
docker buildx install || true
# make sure docker login is handled
docker build --no-cache --platform linux/amd64,linux/arm64 \
  -t ${IMAGE_NAME}:${TAG} -t ${IMAGE_NAME}:v$(cat "${SCRIPT_DIR}"/VERSION) \
  --push .
popd
