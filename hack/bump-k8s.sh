#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail
set -o xtrace

# The normal flow for updating a replaced dependency would look like:
# $ go mod edit -replace old=new@branch
# $ go mod tidy
# However, we don't know all of the specific packages that we pull in from the staging repos,
# nor do we want to update this script when that set changes. Therefore, we just look up the
# version of the k8s.io/kubernetes replacement and replace all modules at that version, since
# we know we will always want to use a self-consistent set of modules from our fork.
# Note: setting GOPROXY=direct allows us to bump very quickly after the fork has been committed to.
current_version="$( GOPROXY=direct go mod edit -json | jq '.Replace[] | select(.Old.Path=="k8s.io/kubernetes") | .New.Version' --raw-output )"
sed -i "s/${current_version}/feature-logical-clusters-1.22/g" go.mod # equivalent to go mod edit -replace
GOPROXY=direct go mod tidy