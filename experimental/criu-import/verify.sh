#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

if [[ $(docker version --format '{{.Server.Os}}/{{.Server.Arch}}') != linux/arm64 ]]; then
  echo 'This experiment requires a native Linux ARM64 Docker server.' >&2
  exit 1
fi

out=$(mktemp -d /tmp/llar-criu-verify.XXXXXX)
chmod 777 "$out"
echo "Artifacts: $out"
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -mod=readonly -o "$out/importer" ./cmd/importer
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -mod=readonly -o "$out/probe" ./cmd/probe
docker build --platform linux/arm64 -t llar-criu-import:arm64 .
docker run --rm --pull=never --platform linux/arm64 \
  --cap-add SYS_PTRACE --cap-add CHECKPOINT_RESTORE --cap-add SYS_ADMIN --cap-add NET_ADMIN --cap-add SYS_RESOURCE \
  --security-opt seccomp=unconfined --network none \
  --mount "type=bind,source=$out,target=/out" \
  --mount "type=bind,source=$PWD,target=/experiment,readonly" \
  llar-criu-import:arm64 bash /experiment/verify-linux.sh
