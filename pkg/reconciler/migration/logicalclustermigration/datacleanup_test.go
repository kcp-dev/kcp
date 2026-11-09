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

package logicalclustermigration

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/etcd/etcdtest"
)

func TestScanEtcdKeys_singlePageEmitsAllPrefixFamilies(t *testing.T) {
	t.Parallel()

	prefix := "/registry/"
	target := logicalcluster.Name("root:ws")

	// All keys fit in one page. Three distinct (group, resource) families
	// for the target, plus an unrelated cluster's data that must NOT be
	// emitted.
	kv := etcdtest.NewFakeKV(map[string]string{
		"/registry/apps/deployments/root:ws/default/foo": "v",
		"/registry/apps/deployments/root:ws/default/bar": "v",
		"/registry/core/configmaps/root:ws/default/cm1":  "v",
		"/registry/core/secrets/root:ws/default/s1":      "v",
		// other cluster, must not be emitted
		"/registry/apps/deployments/root:other/default/x": "v",
		"/registry/core/configmaps/root:other/default/y":  "v",
	})

	got, err := collectScan(t, kv, prefix, target, 1000)
	require.NoError(t, err)

	want := []string{
		"/registry/apps/deployments/root:ws",
		"/registry/core/configmaps/root:ws",
		"/registry/core/secrets/root:ws",
	}
	sort.Strings(got)
	sort.Strings(want)
	require.Equal(t, want, got)
}

func TestScanEtcdKeys_multiPageEmitsAllPrefixFamilies(t *testing.T) {
	t.Parallel()

	prefix := "/registry/"
	target := logicalcluster.Name("root:ws")

	// 5 distinct prefix families for the target. With pageSize=2, the
	// scanner must paginate at least 3 times and still emit every family
	// exactly once.
	kv := etcdtest.NewFakeKV(map[string]string{
		"/registry/apps/deployments/root:ws/default/a":                "v",
		"/registry/apps/deployments/root:ws/default/b":                "v",
		"/registry/apps/replicasets/root:ws/default/a":                "v",
		"/registry/core/configmaps/root:ws/default/a":                 "v",
		"/registry/core/secrets/root:ws/default/a":                    "v",
		"/registry/rbac.authorization.k8s.io/roles/root:ws/default/a": "v",
		// noise from another cluster
		"/registry/apps/deployments/root:other/default/x": "v",
		"/registry/core/secrets/root:other/default/y":     "v",
	})

	got, err := collectScan(t, kv, prefix, target, 2)
	require.NoError(t, err)

	want := []string{
		"/registry/apps/deployments/root:ws",
		"/registry/apps/replicasets/root:ws",
		"/registry/core/configmaps/root:ws",
		"/registry/core/secrets/root:ws",
		"/registry/rbac.authorization.k8s.io/roles/root:ws",
	}
	sort.Strings(got)
	sort.Strings(want)
	require.Equal(t, want, got)
}

func TestScanEtcdKeys_noTargetKeysEmitsNothing(t *testing.T) {
	t.Parallel()

	prefix := "/registry/"
	target := logicalcluster.Name("root:ws")

	kv := etcdtest.NewFakeKV(map[string]string{
		"/registry/apps/deployments/root:other/default/a": "v",
		"/registry/core/configmaps/root:other/default/b":  "v",
	})

	got, err := collectScan(t, kv, prefix, target, 1000)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestScanEtcdKeys_crdAndIdentityResources(t *testing.T) {
	t.Parallel()

	prefix := "/registry/"
	target := logicalcluster.Name("root:ws")

	kv := etcdtest.NewFakeKV(map[string]string{
		"/registry/widgets.example.io/widgets/customresources/root:ws/default/w1": "v",
		"/registry/widgets.example.io/widgets/customresources/root:ws/default/w2": "v",
		"/registry/things.example.io/things/abc123def/root:ws/default/t1":         "v",
		"/registry/apps/deployments/root:ws/default/d1":                           "v",
	})

	got, err := collectScan(t, kv, prefix, target, 1000)
	require.NoError(t, err)

	want := []string{
		"/registry/apps/deployments/root:ws",
		"/registry/things.example.io/things/abc123def/root:ws",
		"/registry/widgets.example.io/widgets/customresources/root:ws",
	}
	sort.Strings(got)
	sort.Strings(want)
	require.Equal(t, want, got)
}

// collectScan invokes scanEtcdKeys with the given page size and collects
// every prefix it emits. It exists so each test reads as a single
// require.Equal.
func collectScan(t *testing.T, kv *etcdtest.FakeKV, prefix string, target logicalcluster.Name, pageSize int64) ([]string, error) {
	t.Helper()

	out := make(chan string)
	errCh := make(chan error, 1)

	go func() {
		errCh <- scanEtcdKeys(context.Background(), kv, prefix, target, pageSize, out)
	}()

	var got []string
	for p := range out {
		got = append(got, p)
	}
	return got, <-errCh
}
