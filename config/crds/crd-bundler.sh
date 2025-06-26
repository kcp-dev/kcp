#!/usr/bin/env bash

# This script bundles CRDs to be used in relation to goreleaser.
CRDS_DIR=$(dirname "${0}")

CRDS_FILE="${CRDS_DIR}/crds.yaml"
rm -f ${CRDS_FILE}
for file in ${CRDS_DIR}/apis.kcp.*.yaml ${CRDS_DIR}/cache.kcp.*.yaml \
            ${CRDS_DIR}/core.kcp.*.yaml ${CRDS_DIR}/tenancy.kcp.*.yaml \
            ${CRDS_DIR}/topology.kcp.*.yaml; do
    cat ${file} >> "${CRDS_FILE}"
    echo "--" >> "${CRDS_FILE}"
    echo ${file} >> /tmp/debug
done
