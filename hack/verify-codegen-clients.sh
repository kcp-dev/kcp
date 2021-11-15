#!/usr/bin/env bash

# This script ensures that the generated client code checked into git is up-to-date
# with the generator. If it is not, re-generate the configuration to update it.

set -o errexit
set -o nounset
set -o pipefail

"$( dirname "${BASH_SOURCE[0]}")/update-codegen-clients.sh"
if ! git diff --quiet --exit-code -- pkg/client; then
	cat << EOF
ERROR: This check enforces that the client code is generated correctly.
ERROR: The client code is out of date. Run the following command to re-
ERROR: generate the clients:
ERROR: $ hack/update-generated-clients.sh
ERROR: The following differences were found:
EOF
	git diff
	exit 1
fi