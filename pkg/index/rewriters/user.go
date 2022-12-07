/*
Copyright 2022 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package rewriters

import (
	"crypto/sha256"
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/martinlindhe/base36"
)

// UserRewriter translates a user home cluster path: "user:paul:abc" to "<cluster>:abc".
func UserRewriter(segments []string) []string {
	if segments[0] == "user" && len(segments) > 1 {
		return append(strings.Split(HomeClusterName(segments[1]).String(), ":"), segments[2:]...)
	}

	return segments
}

func HomeClusterName(userName string) logicalcluster.Name {
	hash := sha256.Sum224([]byte(userName))
	return logicalcluster.Name(strings.ToLower(base36.EncodeBytes(hash[:8])))
}
