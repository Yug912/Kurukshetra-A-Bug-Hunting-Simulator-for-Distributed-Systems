#!/usr/bin/env bash
# Build every image the brain side needs: kuru/toykv, kuru/etcd:buggy, kuru/etcd:fixed.
set -euo pipefail
cd "$(dirname "$0")/.."
docker build -t kuru/toykv -f targets/toykv/Dockerfile .
docker build -t kuru/etcd:buggy -f targets/etcd/Dockerfile.buggy .
docker build -t kuru/etcd:fixed -f targets/etcd/Dockerfile.fixed .
docker images --format '{{.Repository}}:{{.Tag}}  {{.Size}}' | grep -E '^kuru/(toykv|etcd)'
