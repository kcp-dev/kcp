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

package testing

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"

	"github.com/kcp-dev/kcp/test/load/pkg/measurement"
)

// NewKCPReport creates a new Report and adds metadata about kcp its running against.
func NewKCPReport(ctx context.Context, t *testing.T, title string, cfg *rest.Config) *measurement.Report {
	t.Helper()

	report := measurement.NewReport(title)

	c, err := kcpclientset.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("Could not build kcp client: %v", err)
	}
	rootclient := c.Cluster(core.RootCluster.Path())

	sv, err := rootclient.Discovery().ServerVersion()
	if err != nil {
		t.Fatalf("Could not fetch kcp version: %v", err)
	}

	report.Metadata = append(report.Metadata, measurement.Parameter{Key: "kcp Version", Value: sv.GitVersion})

	shards, err := rootclient.CoreV1alpha1().Shards().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Could not list kcp shards: %v", err)
	}

	report.Metadata = append(report.Metadata, measurement.Parameter{Key: "kcp Shards", Value: fmt.Sprintf("%d", len(shards.Items))})

	// running on first shard name is fine, we just want to detect if cache server is used and not verify that it is correct for all shards
	firstShard := shards.Items[0].Name
	report.Metadata = append(report.Metadata, measurement.Parameter{Key: "kcp Cache Server", Value: detectCacheServer(ctx, t, cfg, firstShard)})

	return report
}

// In our two setups, we can use a little trick to determine if the cache server is embedded or external:
// 200 -> we reached embedded cache handler
// 404 -> can't reach cache via external VW, also embedded
// 403 + "not resolved to valid virtual workspace" -> external cache server
// anything else, can't tell.
// This might become a bit brittle, but since its just to report metadata, we should be fine.
// Any alternative would involve sharing setup data or giving access to the underlying Kubernetes cluster.
func detectCacheServer(ctx context.Context, t *testing.T, cfg *rest.Config, shardName string) string {
	// strip out any clusters
	host := cfg.Host
	if i := strings.Index(host, "/clusters/"); i != -1 {
		host = host[:i]
	}
	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		t.Logf("Could not build HTTP client for cache-server probe: %v", err)
		return "unknown"
	}
	url := host + "/services/cache/shards/" + shardName + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Logf("Could not build request for cache-server probe: %v", err)
		return "unknown"
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Logf("Could not probe cache-server endpoint: %v", err)
		return "unknown"
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return "embedded"
	}

	if resp.StatusCode == http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		if strings.Contains(string(body), "Path not resolved to a valid virtual workspace") {
			return "external"
		}
	}
	return "unknown"
}
