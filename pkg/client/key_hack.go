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

package client

import (
	"fmt"
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"
)

// ToClusterAwareKey is a legacy adapter to allow formatting keys for indexers that still use the forked
// k8s MetaNamespaceKeyFunc.
func ToClusterAwareKey(cluster logicalcluster.Name, name string) string {
	return cluster.String() + "|" + name
}

func SplitClusterAwareKey(key string) (logicalcluster.Name, string) {
	parts := strings.Split(key, "|")
	if len(parts) != 2 {
		panic(fmt.Sprintf("bad key: %v", key))
	}
	return logicalcluster.New(parts[0]), parts[1]
}
