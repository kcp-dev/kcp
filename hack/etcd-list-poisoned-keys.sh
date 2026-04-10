#!/usr/bin/env bash

# Copyright 2026 The kcp Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# List etcd keys that look like they were corrupted by the workspace-path
# poisoning bug: the local proxy used to stuff multi-segment workspace paths
# (e.g. "root:internal-cluster") verbatim into the cluster-name segment of
# the etcd key, producing rows that are invisible to the normal read path
# but still consume space and can leak via wildcard partial-metadata lists.
#
# This script is READ-ONLY. It never modifies or deletes keys.
#
# Usage:
#   hack/etcd-list-poisoned-keys.sh [--summary]
#
# Flags:
#   -s, --summary   Print counts grouped by poisoned cluster-name segment
#                   and by <group>/<resource> prefix instead of raw keys.
#   -h, --help      Show this help.
#
# Environment variables (passed through to etcdctl):
#   ETCDCTL_ENDPOINTS   default: http://localhost:2379
#   ETCDCTL_CACERT
#   ETCDCTL_CERT
#   ETCDCTL_KEY
#
# Requires etcdctl v3.4+ (v3 API is the default). Do NOT export
# ETCDCTL_API=3 on modern etcdctl — it just prints a "unrecognized
# environment variable" warning.
#
# Example against a TLS-protected etcd (generic):
#   ETCDCTL_ENDPOINTS=https://etcd-0.etcd:2379 \
#   ETCDCTL_CACERT=/etc/etcd/ca.crt \
#   ETCDCTL_CERT=/etc/etcd/client.crt \
#   ETCDCTL_KEY=/etc/etcd/client.key \
#   hack/etcd-list-poisoned-keys.sh --summary
#
# ---------------------------------------------------------------------------
# Running against a local `kcp start` (embedded etcd)
# ---------------------------------------------------------------------------
#
# `kcp start` runs an embedded etcd whose connection material lives under
# the kcp --root-directory (default: .kcp in the current working directory).
# The defaults are:
#
#   data dir       <root-dir>/etcd-server/
#   client URL     https://localhost:<--embedded-etcd-client-port>   (default 2379)
#   CA cert        <root-dir>/etcd-server/secrets/ca/cert.pem
#   client cert    <root-dir>/etcd-server/secrets/client/cert.pem
#   client key     <root-dir>/etcd-server/secrets/client/key.pem
#
# The embedded etcd is only reachable while the kcp process is running,
# and the TLS cert is issued for "localhost" (use https://localhost:PORT,
# not an IP).
#
# First verify the endpoint is alive:
#
#   etcdctl \
#     --endpoints=https://localhost:2379 \
#     --cacert=.kcp/etcd-server/secrets/ca/cert.pem \
#     --cert=.kcp/etcd-server/secrets/client/cert.pem \
#     --key=.kcp/etcd-server/secrets/client/key.pem \
#     endpoint health
#
# Then scan for poisoned keys:
#
#   ETCDCTL_ENDPOINTS=https://localhost:2379 \
#   ETCDCTL_CACERT=.kcp/etcd-server/secrets/ca/cert.pem \
#   ETCDCTL_CERT=.kcp/etcd-server/secrets/client/cert.pem \
#   ETCDCTL_KEY=.kcp/etcd-server/secrets/client/key.pem \
#   hack/etcd-list-poisoned-keys.sh --summary
#
# Multi-shard: each shard started by `kcp start --shard=...` runs its own
# embedded etcd on its own --embedded-etcd-client-port. Run the script
# once per shard, varying ETCDCTL_ENDPOINTS and the three cert paths to
# point at that shard's <root-dir>/etcd-server/secrets/.
#
# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------
#
# This script never writes. Only after (1) taking an etcd snapshot and
# (2) reviewing the list, delete per affected resource:
#
#     etcdctl del --prefix /registry/<group>/<resource>/customresources/<seg>/
#
# where <seg> is a poisoned cluster-name segment reported by --summary.
# Do NOT paste the raw output of this script into a delete command.

set -euo pipefail

SUMMARY=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    -s|--summary)
      SUMMARY=1
      shift
      ;;
    -h|--help)
      sed -n '17,100p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      echo "unknown flag: $1" >&2
      echo "try: $0 --help" >&2
      exit 2
      ;;
  esac
done

if ! command -v etcdctl >/dev/null 2>&1; then
  echo "etcdctl not found in PATH" >&2
  exit 1
fi

# Walk every key under /registry/ and keep only those with at least one
# path segment that contains a colon and does not start with "system:".
#
# kcp's storage key layout is:
#     /registry/<group>/<resource>/customresources/<cluster>/...
# None of <group>, <resource>, namespace, or object name can legitimately
# contain a colon. The only segment that can legitimately contain a colon
# is a "system:*" cluster name. Any other colon is a poisoned cluster-name
# segment written by the localproxy bug.
poisoned=$(
  etcdctl get --prefix --keys-only /registry/ \
    | awk -F/ '
        NF == 0 { next }          # etcdctl prints blank lines between keys
        {
          for (i = 1; i <= NF; i++) {
            seg = $i
            if (seg == "")                continue
            if (index(seg, ":") == 0)     continue
            if (seg ~ /^system:/)         continue
            print
            next
          }
        }
      '
)

if [[ -z "${poisoned}" ]]; then
  echo "No poisoned keys found."
  exit 0
fi

if [[ "${SUMMARY}" -eq 0 ]]; then
  printf '%s\n' "${poisoned}"
  exit 0
fi

total=$(printf '%s\n' "${poisoned}" | wc -l | tr -d ' ')
echo "Total poisoned keys: ${total}"
echo
echo "By poisoned cluster-name segment (count  segment):"
printf '%s\n' "${poisoned}" \
  | awk -F/ '
      {
        for (i = 1; i <= NF; i++) {
          if (index($i, ":") > 0 && $i !~ /^system:/) {
            print $i
            next
          }
        }
      }
    ' \
  | sort | uniq -c | sort -rn

echo
echo "By resource prefix (count  /registry/<group>/<resource>):"
printf '%s\n' "${poisoned}" \
  | awk -F/ '
      NF >= 4 { print "/" $2 "/" $3 "/" $4 }
    ' \
  | sort | uniq -c | sort -rn
