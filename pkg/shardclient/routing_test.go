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

package shardclient

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
)

func TestWithDirectRouting(t *testing.T) {
	t.Parallel()

	// two shards, which answer with the path they were asked for
	newShard := func(name string) *httptest.Server {
		shard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"kind":"ConfigMapList","apiVersion":"v1","metadata":{"resourceVersion":%q},"items":[]}`, name+r.URL.Path)
		}))
		t.Cleanup(shard.Close)
		return shard
	}
	shards := map[logicalcluster.Name]*httptest.Server{
		"cluster-on-root":    newShard("root"),
		"cluster-on-shard-1": newShard("shard-1"),
	}

	config := WithDirectRouting(&rest.Config{Host: "https://not-a-shard.invalid"}, func(clusterName logicalcluster.Name) (*url.URL, error) {
		shard, found := shards[clusterName]
		if !found {
			return nil, fmt.Errorf("logical cluster %s not found", clusterName)
		}
		return url.Parse(shard.URL)
	})
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	require.NoError(t, err)

	list := func(clusterName logicalcluster.Name) (string, error) {
		configMaps, err := kubeClusterClient.Cluster(clusterName.Path()).CoreV1().ConfigMaps("default").List(t.Context(), metav1.ListOptions{})
		if err != nil {
			return "", err
		}
		return configMaps.ResourceVersion, nil
	}

	answer, err := list("cluster-on-root")
	require.NoError(t, err)
	require.Equal(t, "root/clusters/cluster-on-root/api/v1/namespaces/default/configmaps", answer)

	answer, err = list("cluster-on-shard-1")
	require.NoError(t, err)
	require.Equal(t, "shard-1/clusters/cluster-on-shard-1/api/v1/namespaces/default/configmaps", answer)

	_, err = list("unknown-cluster")
	require.ErrorContains(t, err, "cannot route request to the shard of logical cluster unknown-cluster")
}

func TestClusterNameFromPath(t *testing.T) {
	t.Parallel()

	for path, want := range map[string]logicalcluster.Name{
		"/clusters/2ysoakpbgjw03nkk/api/v1/configmaps": "2ysoakpbgjw03nkk",
		"/clusters/2ysoakpbgjw03nkk":                   "2ysoakpbgjw03nkk",
		"/clusters/*/api/v1/configmaps":                "",
		"/clusters/":                                   "",
		"/api/v1/configmaps":                           "",
		"/services/admin/clusters/x":                   "",
	} {
		got, ok := clusterNameFromPath(path)
		require.Equal(t, want, got, path)
		require.Equal(t, want != "", ok, path)
	}
}
