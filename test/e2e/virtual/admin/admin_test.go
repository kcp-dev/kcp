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

package admin

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
	kcptesting "github.com/kcp-dev/sdk/testing"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestAdminWorkspaceShards exercises the Admin workspace (/services/admin):
// the aggregated shards view.
func TestAdminWorkspaceShards(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)

	vwCfg := rest.CopyConfig(cfg)
	vwURL, err := url.Parse(cfg.Host)
	require.NoError(t, err)
	vwURL.Path = "/services/admin"
	vwCfg.Host = vwURL.String()
	adminClient, err := kcpclientset.NewForConfig(vwCfg)
	require.NoError(t, err)

	t.Logf("List shards through the Admin workspace at %s", vwCfg.Host)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		shards, err := adminClient.CoreV1alpha1().Shards().List(ctx, metav1.ListOptions{})
		require.NoError(c, err)
		require.NotEmpty(c, shards.Items, "expected at least the root shard in the admin view")
		for _, shard := range shards.Items {
			require.NotContains(c, shard.Annotations, "kcp.io/shard", "cache bookkeeping annotations must be stripped")
		}
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}
