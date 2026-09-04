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

package syncer

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	clientshard "github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/pkg/cache/server/bootstrap"
)

// rootCluster is the logical cluster where root-phase0 resources live on the
// root cache server.
var rootCluster = logicalcluster.Name("root")

// systemGVR groups a GVR with the shard it lives under on the root cache.
type systemGVR struct {
	gvr   schema.GroupVersionResource
	shard string
}

// systemGVRs is the fixed set of GVRs that the root cache bootstraps during
// root-phase0 and that a non-root cache must receive before serving kcp shards.
// Versions are the storage versions from the corresponding CRD specs.
var systemGVRs = []systemGVR{
	{schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}, "root"},
	{schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiresourceschemas"}, "root"},
	{schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha1", Resource: "apiexportendpointslices"}, "root"},
	{schema.GroupVersionResource{Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters"}, "root"},
	{schema.GroupVersionResource{Group: "core.kcp.io", Version: "v1alpha1", Resource: "shards"}, "root"},
	{schema.GroupVersionResource{Group: "core.kcp.io", Version: "v1alpha1", Resource: "shards"}, "system:shard"},
	{schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}, "root"},
	{schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}, "root"},
	{schema.GroupVersionResource{Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspacetypes"}, "root"},
}

// systemCRDNames are the CRD names (resource.group) that must be Established
// before the bootstrap can safely list or create objects of those types.
var systemCRDNames = func() []string {
	seen := make(map[string]bool)
	var names []string
	for _, sg := range systemGVRs {
		name := sg.gvr.Resource + "." + sg.gvr.Group
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}()

// BootstrapFromPeer ensures the local cache holds the system resources that
// root-phase0 creates on the root cache. The sequence is:
//
//  1. Wait for all system CRDs to reach the Established condition so their API
//     endpoints are ready.
//  2. Check whether the local cache is already bootstrapped (proxy: any
//     apis.kcp.io/apiexports in cluster=root, shard=root). Skip if it is.
//  3. Pick a random URL from peerURLs, connect to that peer, and copy each GVR
//     in systemGVRs into the local cache.
//
// Must be called after the CRD bootstrap and local Cache object creation have
// completed (i.e. after the "cache-server-start-informers" post-start hook).
func BootstrapFromPeer(
	ctx context.Context,
	localCRDClient kcpapiextensionsclientset.ClusterInterface,
	localConfig *rest.Config,
	peerTLSConfig rest.TLSClientConfig,
	peerURLs []string,
) error {
	logger := klog.FromContext(ctx).WithName("cache-peer-bootstrap")

	// Phase 1: wait for the system CRDs to be Established so their API
	// endpoints are available for the check and the object creates below.
	logger.Info("waiting for system CRDs to be Established")
	if err := waitForSystemCRDsEstablished(ctx, localCRDClient, logger); err != nil {
		return fmt.Errorf("waiting for system CRDs: %w", err)
	}

	localClient, err := kcpdynamic.NewForConfig(localConfig)
	if err != nil {
		return fmt.Errorf("build local dynamic client: %w", err)
	}

	// Phase 2: proxy check — if any APIExports exist locally the cache is
	// already bootstrapped. A NotFound error means the root shard/cluster
	// doesn't exist yet, which also means we need to bootstrap.
	checkCtx := cacheclient.WithShardInContext(ctx, clientshard.New("root"))
	existing, err := localClient.Cluster(rootCluster.Path()).
		Resource(systemGVRs[0].gvr). // apiexports
		List(checkCtx, metav1.ListOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("check local APIExports: %w", err)
	}
	if err == nil && len(existing.Items) > 0 {
		logger.V(4).Info("local cache already bootstrapped, skipping peer pull")
		return nil
	}

	logger.Info("local cache missing system APIExports; waiting for a peer to be ready")

	// Phase 3: poll until a peer has the root-phase0 objects and the pull
	// succeeds. The peer itself may still be bootstrapping (root kcp shard
	// running root-phase0), so we must wait rather than fail immediately.
	urls := make([]string, len(peerURLs))
	copy(urls, peerURLs)
	rand.Shuffle(len(urls), func(i, j int) { urls[i], urls[j] = urls[j], urls[i] })

	return wait.PollUntilContextCancel(ctx, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		for _, url := range urls {
			// Probe: does the peer have APIExports yet? If not it is still
			// running root-phase0 on its side; skip and try next iteration.
			if !peerHasSystemAPIExports(ctx, url, peerTLSConfig) {
				logger.V(4).Info("peer not ready yet, will retry", "url", url)
				continue
			}
			if err := pullSystemGVRsFromPeer(ctx, localClient, url, peerTLSConfig, logger); err != nil {
				logger.V(4).Info("pull from peer failed, will retry", "url", url, "err", err)
				continue
			}
			logger.Info("peer bootstrap succeeded", "url", url)
			return true, nil
		}
		return false, nil // no peer ready yet; sleep and retry
	})
}

// peerHasSystemAPIExports returns true when the peer cache at url has at least
// one APIExport in the root cluster / root shard, indicating that the root
// kcp shard has finished running root-phase0 and the objects have been
// replicated to that peer.
func peerHasSystemAPIExports(ctx context.Context, url string, tlsCfg rest.TLSClientConfig) bool {
	peerCfg := buildBootstrapPeerConfig(url, tlsCfg)
	peerClient, err := kcpdynamic.NewForConfig(peerCfg)
	if err != nil {
		return false
	}
	listCtx := cacheclient.WithShardInContext(ctx, clientshard.New("root"))
	list, err := peerClient.Cluster(rootCluster.Path()).Resource(systemGVRs[0].gvr).List(listCtx, metav1.ListOptions{})
	return err == nil && len(list.Items) > 0
}

// waitForSystemCRDsEstablished polls until every CRD in systemCRDNames has
// the Established condition set to True. This ensures the dynamic API
// endpoints are ready before we try to list or create objects via them.
func waitForSystemCRDsEstablished(ctx context.Context, client kcpapiextensionsclientset.ClusterInterface, logger klog.Logger) error {
	crdCtx := cacheclient.WithShardInContext(ctx, clientshard.New(bootstrap.SystemCacheServerShard))
	crdClient := client.Cluster(bootstrap.SystemCRDLogicalCluster.Path()).ApiextensionsV1().CustomResourceDefinitions()

	return wait.PollUntilContextCancel(ctx, 500*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		for _, name := range systemCRDNames {
			crd, err := crdClient.Get(crdCtx, name, metav1.GetOptions{})
			if err != nil {
				logger.V(4).Info("CRD not yet available, retrying", "crd", name, "err", err)
				return false, nil
			}
			if !isCRDEstablished(crd) {
				logger.V(4).Info("CRD not yet Established, retrying", "crd", name)
				return false, nil
			}
		}
		return true, nil
	})
}

func isCRDEstablished(crd *apiextensionsv1.CustomResourceDefinition) bool {
	for _, cond := range crd.Status.Conditions {
		if cond.Type == apiextensionsv1.Established {
			return cond.Status == apiextensionsv1.ConditionTrue
		}
	}
	return false
}

func pullSystemGVRsFromPeer(
	ctx context.Context,
	localClient kcpdynamic.ClusterInterface,
	peerURL string,
	tlsCfg rest.TLSClientConfig,
	logger klog.Logger,
) error {
	peerCfg := buildBootstrapPeerConfig(peerURL, tlsCfg)
	peerClient, err := kcpdynamic.NewForConfig(peerCfg)
	if err != nil {
		return fmt.Errorf("build peer client for %s: %w", peerURL, err)
	}

	for _, sg := range systemGVRs {
		listCtx := cacheclient.WithShardInContext(ctx, clientshard.New(sg.shard))
		list, err := peerClient.Cluster(rootCluster.Path()).Resource(sg.gvr).List(listCtx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("list %s (shard=%s) from %s: %w", sg.gvr.Resource, sg.shard, peerURL, err)
		}
		logger.V(4).Info("pulled from peer", "gvr", sg.gvr.Resource, "shard", sg.shard, "count", len(list.Items))

		for i := range list.Items {
			obj := list.Items[i].DeepCopy()
			obj.SetResourceVersion("")
			obj.SetUID("")
			obj.SetManagedFields(nil)

			shardName := obj.GetAnnotations()[clientshard.AnnotationKey]
			cluster := logicalcluster.From(obj)

			createCtx := cacheclient.WithShardInContext(ctx, clientshard.New(shardName))
			_, err := localClient.Cluster(cluster.Path()).Resource(sg.gvr).
				Namespace(obj.GetNamespace()).Create(createCtx, obj, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			if err != nil {
				return fmt.Errorf("create %s/%s locally: %w", sg.gvr.Resource, obj.GetName(), err)
			}
		}
	}
	return nil
}

// buildBootstrapPeerConfig returns a REST config for a peer cache-server with
// the round-trippers needed for shard-in-URL routing. The wildcard-shard
// default is omitted because bootstrap always specifies shards explicitly via
// cacheclient.WithShardInContext.
func buildBootstrapPeerConfig(host string, tlsCfg rest.TLSClientConfig) *rest.Config {
	cfg := &rest.Config{Host: host, TLSClientConfig: tlsCfg}
	cfg = cacheclient.WithCacheServiceRoundTripper(cfg)
	cfg = cacheclient.WithShardNameFromContextRoundTripper(cfg)
	return cfg
}
