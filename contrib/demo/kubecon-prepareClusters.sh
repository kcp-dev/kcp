#!/bin/bash

DEMO_ROOT="$(dirname "${BASH_SOURCE}")"

${DEMO_ROOT}/clusters/kind/createKindClusters.sh
