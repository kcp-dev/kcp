/*
Copyright 2026 The kcp Authors.

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

package kubernetes

import (
	"testing"

	"k8s.io/client-go/rest"

	"github.com/kcp-dev/logicalcluster/v3"
)

func TestClusterDelegatesToGroupClients(t *testing.T) {
	t.Parallel()

	cs, err := NewForConfig(&rest.Config{Host: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	path := logicalcluster.NewPath("root:org")

	if cs.Cluster(path).RbacV1() != cs.RbacV1().Cluster(path) {
		t.Fatalf("expected Cluster().RbacV1() to return the cached RbacV1 client of the cluster")
	}
	if cs.Cluster(path).CoreV1() != cs.CoreV1().Cluster(path) {
		t.Fatalf("expected Cluster().CoreV1() to return the cached CoreV1 client of the cluster")
	}
	if cs.Cluster(path).RbacV1() == cs.Cluster(logicalcluster.NewPath("root:other")).RbacV1() {
		t.Fatalf("expected different clients for different clusters")
	}
	first, second := cs.Cluster(path).Discovery(), cs.Cluster(path).Discovery()
	if first != second {
		t.Fatalf("expected the discovery client of a cluster to be cached")
	}
}
