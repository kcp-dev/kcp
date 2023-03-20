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

package synctarget

import (
	"context"
	"fmt"
	"sync"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	workloadv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/informer"
)

var _ informer.GVRSource = (*syncTargetGVRSource)(nil)

// NewSyncTargetGVRSource returns an [informer.GVRSource] that can update its list of GVRs based on
// a SyncTarget resource passed to the updateGVRs() method.
//
// It will be used to feed the various [informer.DiscoveringDynamicSharedInformerFactory] instances
// for downstream and upstream.
func NewSyncTargetGVRSource(
	syncTargetInformer workloadv1alpha1informers.SyncTargetInformer,
	downstreamKubeClient *kubernetes.Clientset,
) *syncTargetGVRSource {
	return &syncTargetGVRSource{
		synctargetInformerHasSynced: syncTargetInformer.Informer().HasSynced,
		gvrsToWatch:                 map[schema.GroupVersionResource]informer.GVRPartialMetadata{},
		isGVRAllowed: func(ctx context.Context, gvr schema.GroupVersionResource) (bool, error) {
			if sar, err := downstreamKubeClient.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
				Spec: authorizationv1.SelfSubjectAccessReviewSpec{
					ResourceAttributes: &authorizationv1.ResourceAttributes{
						Group:    gvr.Group,
						Resource: gvr.Resource,
						Version:  gvr.Version,
						Verb:     "*",
					},
				},
			}, metav1.CreateOptions{}); err != nil {
				return false, err
			} else {
				return sar.Status.Allowed, nil
			}
		},
		downstreamDiscoveryClient: memory.NewMemCacheClient(discovery.NewDiscoveryClient(downstreamKubeClient.RESTClient())),
	}
}

type syncTargetGVRSource struct {
	gvrsToWatchLock sync.RWMutex
	gvrsToWatch     map[schema.GroupVersionResource]informer.GVRPartialMetadata

	// Support subscribers that want to know when Synced GVRs have changed.
	subscribersLock sync.Mutex
	subscribers     []chan<- struct{}

	synctargetInformerHasSynced cache.InformerSynced
	isGVRAllowed                func(ctx context.Context, gvr schema.GroupVersionResource) (bool, error)

	downstreamDiscoveryClient discovery.CachedDiscoveryInterface
}

// GVRs returns the required metadata (scope, kind, singular name) about all GVRs that should be synced.
// It implements [informer.GVRSource.GVRs].
func (c *syncTargetGVRSource) GVRs() map[schema.GroupVersionResource]informer.GVRPartialMetadata {
	c.gvrsToWatchLock.RLock()
	defer c.gvrsToWatchLock.RUnlock()

	gvrs := make(map[schema.GroupVersionResource]informer.GVRPartialMetadata, len(c.gvrsToWatch)+len(builtinGVRs)+2)
	gvrs[corev1.SchemeGroupVersion.WithResource("namespaces")] = informer.GVRPartialMetadata{
		Scope: apiextensionsv1.ClusterScoped,
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Singular: "namespace",
			Kind:     "Namespace",
		},
	}
	for key, value := range builtinGVRs {
		gvrs[key] = value
	}
	for key, value := range c.gvrsToWatch {
		gvrs[key] = value
	}
	return gvrs
}

// Ready returns true if the controller is ready to return the GVRs to sync.
// It implements [informer.GVRSource.Ready].
func (c *syncTargetGVRSource) Ready() bool {
	return c.synctargetInformerHasSynced()
}

// Subscribe returns a new channel to which the controller writes whenever
// its list of GVRs has changed.
// It implements [informer.GVRSource.Subscribe].
func (c *syncTargetGVRSource) Subscribe() <-chan struct{} {
	c.subscribersLock.Lock()
	defer c.subscribersLock.Unlock()

	// Use a buffered channel so we can always send at least 1, regardless of consumer status.
	changes := make(chan struct{}, 1)
	c.subscribers = append(c.subscribers, changes)

	return changes
}

// removeUnusedGVRs removes the GVRs which are not required anymore, and return `true` if GVRs were updated.
func (c *syncTargetGVRSource) removeUnusedGVRs(ctx context.Context, requiredGVRs map[schema.GroupVersionResource]bool) bool {
	logger := klog.FromContext(ctx)

	c.gvrsToWatchLock.Lock()
	defer c.gvrsToWatchLock.Unlock()

	updated := false
	for gvr := range c.gvrsToWatch {
		if _, ok := requiredGVRs[gvr]; !ok {
			logger.WithValues("gvr", gvr.String()).V(2).Info("Stop syncer for gvr")
			delete(c.gvrsToWatch, gvr)
			updated = true
		}
	}
	return updated
}

// addGVR adds the given GVR if it isn't already in the list, and returns `true` if the GVR was added,
// `false` if it was already there.
func (c *syncTargetGVRSource) addGVR(ctx context.Context, gvr schema.GroupVersionResource) (bool, error) {
	logger := klog.FromContext(ctx)

	c.gvrsToWatchLock.Lock()
	defer c.gvrsToWatchLock.Unlock()

	if _, ok := c.gvrsToWatch[gvr]; ok {
		logger.V(2).Info("Informer is started already")
		return false, nil
	}

	partialMetadata, err := c.getGVRPartialMetadata(gvr)
	if err != nil {
		return false, err
	}

	c.gvrsToWatch[gvr] = *partialMetadata

	return true, nil
}

func (c *syncTargetGVRSource) getGVRPartialMetadata(gvr schema.GroupVersionResource) (*informer.GVRPartialMetadata, error) {
	apiResourceList, err := c.downstreamDiscoveryClient.ServerResourcesForGroupVersion(gvr.GroupVersion().String())
	if err != nil {
		return nil, err
	}
	for _, apiResource := range apiResourceList.APIResources {
		if apiResource.Name == gvr.Resource {
			var resourceScope apiextensionsv1.ResourceScope
			if apiResource.Namespaced {
				resourceScope = apiextensionsv1.NamespaceScoped
			} else {
				resourceScope = apiextensionsv1.ClusterScoped
			}

			return &informer.GVRPartialMetadata{
					Names: apiextensionsv1.CustomResourceDefinitionNames{
						Kind:     apiResource.Kind,
						Singular: apiResource.SingularName,
					},
					Scope: resourceScope,
				},
				nil
		}
	}
	return nil, fmt.Errorf("unable to retrieve discovery for GVR: %s", gvr)
}

func (c *syncTargetGVRSource) notifySubscribers(ctx context.Context) {
	logger := klog.FromContext(ctx)

	c.subscribersLock.Lock()
	defer c.subscribersLock.Unlock()

	for index, ch := range c.subscribers {
		logger.V(4).Info("Attempting to notify subscribers", "index", index)
		select {
		case ch <- struct{}{}:
			logger.V(4).Info("Successfully notified subscriber", "index", index)
		default:
			logger.V(4).Info("Unable to notify subscriber - channel full", "index", index)
		}
	}
}

var builtinGVRs = map[schema.GroupVersionResource]informer.GVRPartialMetadata{
	{
		Version:  "v1",
		Resource: "configmaps",
	}: {
		Scope: apiextensionsv1.NamespaceScoped,
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Singular: "configMap",
			Kind:     "configmap",
		},
	},
	{
		Version:  "v1",
		Resource: "secrets",
	}: {
		Scope: apiextensionsv1.NamespaceScoped,
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Singular: "secret",
			Kind:     "Secret",
		},
	},
}

func getAllGVRs(synctarget *workloadv1alpha1.SyncTarget) map[schema.GroupVersionResource]bool {
	// TODO(jmprusi): Added Configmaps and Secrets to the default syncing, but we should figure out
	//                a way to avoid doing that: https://github.com/kcp-dev/kcp/issues/727
	gvrs := map[schema.GroupVersionResource]bool{}

	for gvr := range builtinGVRs {
		gvrs[gvr] = true
	}

	// TODO(qiujian16) We currently checks the API compatibility on the server side. When we change to check the
	// compatibility on the syncer side, this part needs to be changed.
	for _, r := range synctarget.Status.SyncedResources {
		if r.State != workloadv1alpha1.ResourceSchemaAcceptedState {
			continue
		}
		for _, version := range r.Versions {
			gvrs[schema.GroupVersionResource{
				Group:    r.Group,
				Version:  version,
				Resource: r.Resource,
			}] = true
		}
	}

	return gvrs
}

func (c *syncTargetGVRSource) updateGVRs(ctx context.Context, syncTarget *workloadv1alpha1.SyncTarget) (unauthorizedGVRs []string, errs []error) {
	logger := klog.FromContext(ctx)

	requiredGVRs := getAllGVRs(syncTarget)

	c.downstreamDiscoveryClient.Invalidate()

	notify := false
	for gvr := range requiredGVRs {
		logger := logger.WithValues("gvr", gvr.String())
		ctx := klog.NewContext(ctx, logger)
		allowed, err := c.isGVRAllowed(ctx, gvr)
		if err != nil {
			logger.Error(err, "Failed to check ssar")
			errs = append(errs, err)
			unauthorizedGVRs = append(unauthorizedGVRs, gvr.String())
			continue
		}

		if !allowed {
			logger.V(2).Info("Stop informer since the syncer is not authorized to sync")
			// remove this from requiredGVRs so its informer will be stopped later.
			delete(requiredGVRs, gvr)
			unauthorizedGVRs = append(unauthorizedGVRs, gvr.String())
			continue
		}

		if updated, err := c.addGVR(ctx, gvr); err != nil {
			errs = append(errs, err)
			continue
		} else if updated {
			notify = true
		}
	}

	if updated := c.removeUnusedGVRs(ctx, requiredGVRs); updated {
		notify = true
	}

	if notify {
		c.notifySubscribers(ctx)
	}

	return unauthorizedGVRs, nil
}
