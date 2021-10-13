#!/bin/bash

set -o nounset
set -o pipefail
set -o errexit
set -o xtrace

rm -rf "${WORKDIR}"
mkdir -p "${WORKDIR}"
time make build
./contrib/demo/sharding/run.sh