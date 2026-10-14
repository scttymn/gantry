#!/usr/bin/env bash
# Runs once as root on a clean Ubuntu 24.04: Go, from its official tarball, and
# a C compiler: gantry compiles SQLite from C.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update && apt-get install -y --no-install-recommends curl ca-certificates gcc libc6-dev
arch=$(dpkg --print-architecture) # amd64 on the benchmark box
curl -fsSL "https://go.dev/dl/go1.27.1.linux-$arch.tar.gz" | tar -C /usr/local -xz
