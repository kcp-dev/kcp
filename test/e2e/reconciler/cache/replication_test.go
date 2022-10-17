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

package cache

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/rest"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	cacheopitons "github.com/kcp-dev/kcp/pkg/cache/server/options"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestInProcessReplicateAPIExport tests if an APIExport is propagated to the cache server.
// The test exercises creation, modification and removal of the APIExport object.
func TestInProcessReplicateAPIExport(t *testing.T) {
	t.Parallel()
	// TODO(p0lyn0mial): switch to framework.SharedKcpServer when caching is turned on by default
	tokenAuthFile := framework.WriteTokenAuthFile(t)
	server := framework.PrivateKcpServer(t,
		framework.WithCustomArguments(append(framework.TestServerArgsWithTokenAuthFile(tokenAuthFile),
			"--run-cache-server=true",
		)...,
		))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	org := framework.NewOrganizationFixture(t, server)
	cluster := framework.NewWorkspaceFixture(t, server, org, framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))
	kcpRootShardConfig := server.RootShardSystemMasterBaseConfig(t)
	kcpRootShardClient, err := clientset.NewClusterForConfig(kcpRootShardConfig)
	require.NoError(t, err)
	cacheClientRT := cacheclient.WithCacheServiceRoundTripper(rest.CopyConfig(kcpRootShardConfig))
	cacheClientRT = cacheclient.WithShardNameFromContextRoundTripper(cacheClientRT)
	cacheClientRT = cacheclient.WithDefaultShardRoundTripper(cacheClientRT, shard.Wildcard)
	kcpclienthelper.SetMultiClusterRoundTripper(cacheClientRT)
	cacheKcpClusterClient, err := clientset.NewClusterForConfig(cacheClientRT)
	require.NoError(t, err)

	t.Logf("Create an APIExport in %s workspace on the root shard", cluster)
	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, cluster, kcpRootShardClient.Cluster(cluster), "wild.wild.west", "testing replication to the cache server")
	var cachedWildAPIExport *apisv1alpha1.APIExport
	var wildAPIExport *apisv1alpha1.APIExport
	t.Logf("Get %s/%s APIExport from the root shard and the cache server for comparison", cluster, "wild.wild.west")
	framework.Eventually(t, func() (bool, string) {
		wildAPIExport, err = kcpRootShardClient.Cluster(cluster).ApisV1alpha1().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		cachedWildAPIExport, err = cacheKcpClusterClient.Cluster(cluster).ApisV1alpha1().APIExports().Get(cacheclient.WithShardInContext(ctx, shard.New("root")), wildAPIExport.Name, metav1.GetOptions{})
		if err != nil {
			if !errors.IsNotFound(err) {
				return true, err.Error()
			}
			return false, err.Error()
		}
		t.Logf("Verify if both the orignal APIExport and replicated are the same except %s annotation and ResourceVersion after creation", genericapirequest.AnnotationKey)
		if _, found := cachedWildAPIExport.Annotations[genericapirequest.AnnotationKey]; !found {
			t.Fatalf("replicated APIExport root/%s/%s, doesn't have %s annotation", cluster, cachedWildAPIExport.Name, genericapirequest.AnnotationKey)
		}
		delete(cachedWildAPIExport.Annotations, genericapirequest.AnnotationKey)
		if diff := cmp.Diff(cachedWildAPIExport, wildAPIExport, cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion")); len(diff) > 0 {
			return false, fmt.Sprintf("replicated APIExport root/%s/%s is different that the original", cluster, wildAPIExport.Name)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 400*time.Millisecond)

	t.Logf("Verify that a spec update on %s/%s APIExport is propagated to the cached object", cluster, "wild.wild.west")
	verifyAPIExportUpdate(ctx, t, cluster, kcpRootShardClient, cacheKcpClusterClient, func(apiExport *apisv1alpha1.APIExport) {
		apiExport.Spec.PermissionClaims = append(wildAPIExport.Spec.PermissionClaims, apisv1alpha1.PermissionClaim{GroupResource: apisv1alpha1.GroupResource{Group: "foo", Resource: "bar"}})
	})
	t.Logf("Verify that a metadata update on %s/%s APIExport is propagated ot the cached object", cluster, "wild.wild.west")
	verifyAPIExportUpdate(ctx, t, cluster, kcpRootShardClient, cacheKcpClusterClient, func(apiExport *apisv1alpha1.APIExport) {
		if apiExport.Annotations == nil {
			apiExport.Annotations = map[string]string{}
		}
		apiExport.Annotations["testAnnotation"] = "testAnnotationValue"
	})

	t.Logf("Verify that deleting %s/%s APIExport leads to removal of the cached object", cluster, "wild.wild.west")
	err = kcpRootShardClient.Cluster(cluster).ApisV1alpha1().APIExports().Delete(ctx, "wild.wild.west", metav1.DeleteOptions{})
	require.NoError(t, err)
	framework.Eventually(t, func() (bool, string) {
		_, err := cacheKcpClusterClient.Cluster(cluster).ApisV1alpha1().APIExports().Get(cacheclient.WithShardInContext(ctx, shard.New("root")), wildAPIExport.Name, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			return true, ""
		}
		if err != nil {
			return false, err.Error()
		}
		return false, fmt.Sprintf("replicated APIExport root/%s/%s wasn't removed", cluster, cachedWildAPIExport.Name)
	}, wait.ForeverTestTimeout, 400*time.Millisecond)
}

func TestStandaloneReplicateAPIExport(t *testing.T) {
	tokenAuthFile := framework.WriteTokenAuthFile(t)
	server := framework.PrivateKcpServer(t,
		framework.WithCustomArguments(append(framework.TestServerArgsWithTokenAuthFile(tokenAuthFile),
			"--run-cache-server=true",
		)...,
		))
	cacheServerOptions := cacheopitons.NewOptions(".kcp-cache")
}

func verifyAPIExportUpdate(ctx context.Context, t *testing.T, cluster logicalcluster.Name, kcpRootShardClient clientset.ClusterInterface, cacheKcpClusterClient clientset.ClusterInterface, changeApiExportFn func(*apisv1alpha1.APIExport)) {
	var wildAPIExport *apisv1alpha1.APIExport
	var updatedWildAPIExport *apisv1alpha1.APIExport
	var err error
	framework.Eventually(t, func() (bool, string) {
		wildAPIExport, err = kcpRootShardClient.Cluster(cluster).ApisV1alpha1().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
		if err != nil {
			return true, err.Error()
		}
		changeApiExportFn(wildAPIExport)
		updatedWildAPIExport, err = kcpRootShardClient.Cluster(cluster).ApisV1alpha1().APIExports().Update(ctx, wildAPIExport, metav1.UpdateOptions{})
		if err != nil {
			if !errors.IsConflict(err) {
				return true, fmt.Sprintf("unknow error while updating the cached %s/%s/%s APIExport, err: %s", "root", cluster, "wild.wild.west", err.Error())
			}
			return false, err.Error() // try again
		}
		return true, ""
	}, wait.ForeverTestTimeout, 400*time.Millisecond)
	t.Logf("Get root/%s/%s APIExport from the cache server", cluster, "wild.wild.west")
	framework.Eventually(t, func() (bool, string) {
		cachedWildAPIExport, err := cacheKcpClusterClient.Cluster(cluster).ApisV1alpha1().APIExports().Get(cacheclient.WithShardInContext(ctx, shard.New("root")), wildAPIExport.Name, metav1.GetOptions{})
		if err != nil {
			return true, err.Error()
		}
		t.Logf("Verify if both the orignal APIExport and replicated are the same except %s annotation and ResourceVersion after an update to the spec", genericapirequest.AnnotationKey)
		if _, found := cachedWildAPIExport.Annotations[genericapirequest.AnnotationKey]; !found {
			return true, fmt.Sprintf("replicated APIExport root/%s/%s, doesn't have %s annotation", cluster, cachedWildAPIExport.Name, genericapirequest.AnnotationKey)
		}
		delete(cachedWildAPIExport.Annotations, genericapirequest.AnnotationKey)
		if diff := cmp.Diff(cachedWildAPIExport, updatedWildAPIExport, cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion")); len(diff) > 0 {
			return false, fmt.Sprintf("replicated APIExport root/%s/%s is different that the original, diff: %s", cluster, wildAPIExport.Name, diff)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 400*time.Millisecond)
}
