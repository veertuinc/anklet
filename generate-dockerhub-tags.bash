#!/bin/bash
set -exo pipefail
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
pushd "${SCRIPT_DIR}"
DOCKERFILE_PATH="${SCRIPT_DIR}/docker"
NAME="anklet"
cleanup() {
  rm -f "${DOCKERFILE_PATH}/${NAME}"*
  rm -rf dist
  docker buildx rm mybuilder || true
}
rm -rf dist
ARCH=amd64 make build-linux
ARCH=arm64 make build-linux
cp -f dist/anklet_v$(cat "${SCRIPT_DIR}"/VERSION)_linux_amd64 "${DOCKERFILE_PATH}/anklet_linux_amd64"
cp -f dist/anklet_v$(cat "${SCRIPT_DIR}"/VERSION)_linux_arm64 "${DOCKERFILE_PATH}/anklet_linux_arm64"
ls -alht "${DOCKERFILE_PATH}/"
trap cleanup EXIT
pushd "${DOCKERFILE_PATH}"
docker buildx create --name mybuilder --use desktop-linux || true
docker buildx install || true
# make sure docker login is handled
docker build --no-cache --platform linux/amd64,linux/arm64 \
  -t veertu/"${NAME}":latest -t veertu/"$NAME:v$(cat "${SCRIPT_DIR}"/VERSION)" \
  --push .
popd