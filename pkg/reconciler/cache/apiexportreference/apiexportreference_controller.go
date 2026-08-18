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

// Package apiexportreference keeps a ClusterCachedResource for every object an
// APIExport points at.
//
// An APIExport can name an object through spec.resources[].storage.virtual.
// reference, and the shard resolves that reference through the cache server --
// so an object that is not in the cache cannot be referenced at all.
//
// Getting it there is what ClusterCachedResources already do: they replicate,
// they make the cache server serve the kind, they clean up after themselves,
// and they keep each provider in its own part of the cache. So this controller
// does not replicate anything itself. It writes down what the APIExport asked
// for, as a ClusterCachedResource that the APIExport owns, and lets that
// machinery do the rest.
package apiexportreference

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	cachev1alpha1apply "github.com/kcp-dev/sdk/client/applyconfiguration/cache/v1alpha1"
	metav1apply "github.com/kcp-dev/sdk/client/applyconfiguration/meta/v1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"
	cachev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/cache/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	// ControllerName holds this controller's name.
	ControllerName = "kcp-apiexport-reference-controller"
)

// NewController returns a controller that keeps a ClusterCachedResource for
// every object an APIExport references.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	apiExportInformer apisv1alpha2informers.APIExportClusterInformer,
	clusterCachedResourceInformer cachev1alpha1informers.ClusterCachedResourceClusterInformer,
	restMappingFor func(cluster logicalcluster.Name, gk schema.GroupKind) (*meta.RESTMapping, error),
) (*controller, error) {
	c := &controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: ControllerName},
		),
		restMappingFor: restMappingFor,
		applyResource: func(ctx context.Context, cluster logicalcluster.Name, ccr *cachev1alpha1apply.ClusterCachedResourceApplyConfiguration) error {
			_, err := kcpClusterClient.Cluster(cluster.Path()).CacheV1alpha1().ClusterCachedResources().Apply(ctx, ccr, metav1.ApplyOptions{
				FieldManager: ControllerName,
				Force:        true,
			})
			return err
		},
		deleteResource: func(ctx context.Context, cluster logicalcluster.Name, name string) error {
			return kcpClusterClient.Cluster(cluster.Path()).CacheV1alpha1().ClusterCachedResources().Delete(ctx, name, metav1.DeleteOptions{})
		},
		getAPIExport: func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIExport, error) {
			return apiExportInformer.Lister().Cluster(cluster).Get(name)
		},
		listOwnedResources: func(cluster logicalcluster.Name, export string) ([]*cachev1alpha1.ClusterCachedResource, error) {
			all, err := clusterCachedResourceInformer.Lister().Cluster(cluster).List(labels.Everything())
			if err != nil {
				return nil, err
			}
			var out []*cachev1alpha1.ClusterCachedResource
			for _, ccr := range all {
				if ownedBy(ccr, export) {
					out = append(out, ccr)
				}
			}
			return out, nil
		},
	}

	_, _ = apiExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIExport(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIExport(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueAPIExport(obj) },
	})

	// A ClusterCachedResource that somebody deleted or edited by hand has to
	// come back while its APIExport still wants it. Reconciling applies only
	// the fields this controller owns, so reacting to every update is safe:
	// a write by someone else (the clustercachedresources controller
	// defaulting spec.identity, say) resolves to a no-op apply, not a fight.
	_, _ = clusterCachedResourceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) { c.enqueueOwningAPIExport(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueOwningAPIExport(obj) },
	})

	return c, nil
}

type controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	restMappingFor     func(cluster logicalcluster.Name, gk schema.GroupKind) (*meta.RESTMapping, error)
	getAPIExport       func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIExport, error)
	listOwnedResources func(cluster logicalcluster.Name, export string) ([]*cachev1alpha1.ClusterCachedResource, error)
	applyResource      func(ctx context.Context, cluster logicalcluster.Name, ccr *cachev1alpha1apply.ClusterCachedResourceApplyConfiguration) error
	deleteResource     func(ctx context.Context, cluster logicalcluster.Name, name string) error
}

// reference is one object an APIExport points at. It names a kind, because
// that is what the APIExport carries; resolving it to a resource needs a
// RESTMapper.
type reference struct {
	group string
	kind  string
	name  string
}

// String renders a reference in human readable form, for logging and error messages. It is not a legal object name.
func (r reference) String() string {
	return fmt.Sprintf("%s.%s/%s", strings.ToLower(r.kind), r.group, r.name)
}

// resolved is a reference once its kind has been turned into a resource.
type resolved struct {
	gvr  schema.GroupVersionResource
	name string
}

// referencesFrom collects what an APIExport points at. A reference names a kind
// rather than a resource, and the object is always in the APIExport's own
// logical cluster: a provider can only publish endpoints for itself.
func referencesFrom(export *apisv1alpha2.APIExport) []reference {
	var refs []reference
	for _, resource := range export.Spec.Resources {
		if resource.Storage.Virtual == nil {
			continue
		}
		ref := resource.Storage.Virtual.Reference
		if ref.Kind == "" || ref.Name == "" {
			continue
		}
		refs = append(refs, reference{
			group: ptr.Deref(ref.APIGroup, ""),
			kind:  ref.Kind,
			name:  ref.Name,
		})
	}
	return refs
}

func ownedBy(ccr *cachev1alpha1.ClusterCachedResource, export string) bool {
	for _, ref := range ccr.OwnerReferences {
		if ref.Kind == "APIExport" && ref.Name == export &&
			ref.APIVersion == apisv1alpha2.SchemeGroupVersion.String() {
			return true
		}
	}
	return false
}

// nameFor builds a stable name for the ClusterCachedResource standing in for
// one reference. It has to be a legal object name whatever the reference looks
// like, and it has to be the same every time so that reconciling twice does not
// make two of them.
func nameFor(export string, ref resolved) string {
	h := sha256.Sum256([]byte(export + "|" + ref.gvr.String() + "|" + ref.name))
	suffix := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(h[:5]))

	// Keep something human-readable in front, but leave room for the hash.
	prefix := ref.gvr.Resource
	if len(prefix) > 40 {
		prefix = prefix[:40]
	}
	return fmt.Sprintf("apiexport-%s-%s", prefix, suffix)
}

// desired builds the apply configuration that stands for a reference. It
// carries only the fields this controller owns; applying it with a field
// manager leaves everything else -- spec.identity, most notably, which the
// clustercachedresources controller defaults in place -- to its owners.
func desired(export *apisv1alpha2.APIExport, ref resolved) *cachev1alpha1apply.ClusterCachedResourceApplyConfiguration {
	return cachev1alpha1apply.ClusterCachedResource(nameFor(export.Name, ref)).
		// Owned by the APIExport, so that the whole thing goes away with it
		// -- and its finalizer drains the cache on the way out.
		WithOwnerReferences(metav1apply.OwnerReference().
			WithAPIVersion(apisv1alpha2.SchemeGroupVersion.String()).
			WithKind("APIExport").
			WithName(export.Name).
			WithUID(export.UID)).
		WithAnnotations(map[string]string{
			"cache.kcp.io/referenced-by": export.Name,
		}).
		WithSpec(cachev1alpha1apply.ClusterCachedResourceSpec().
			WithGroup(ref.gvr.Group).
			WithVersion(ref.gvr.Version).
			WithResource(ref.gvr.Resource).
			// Only the object that was referenced. A ClusterCachedResource
			// usually stands for a whole kind; here it stands for one object.
			WithNames(ref.name))
}

func (c *controller) enqueueAPIExport(obj interface{}) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	export, ok := obj.(*apisv1alpha2.APIExport)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("object is %T, not an APIExport", obj))
		return
	}
	c.queue.Add(kcpcache.ToClusterAwareKey(logicalcluster.From(export).String(), "", export.Name))
}

func (c *controller) enqueueOwningAPIExport(obj interface{}) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	ccr, ok := obj.(*cachev1alpha1.ClusterCachedResource)
	if !ok {
		return
	}
	for _, ref := range ccr.OwnerReferences {
		if ref.Kind != "APIExport" {
			continue
		}
		c.queue.Add(kcpcache.ToClusterAwareKey(logicalcluster.From(ccr).String(), "", ref.Name))
	}
}

func (c *controller) Start(ctx context.Context, workers int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for range workers {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *controller) processNextWorkItem(ctx context.Context) bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	logger := klog.FromContext(ctx).WithValues("apiExport", key)
	ctx = klog.NewContext(ctx, logger)

	if err := c.reconcile(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) reconcile(ctx context.Context, rawKey string) error {
	cluster, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(rawKey)
	if err != nil {
		utilruntime.HandleError(err)
		return nil
	}

	export, err := c.getAPIExport(cluster, name)
	if apierrors.IsNotFound(err) {
		// Gone. Its ClusterCachedResources are owned by it, so the garbage
		// collector takes them, and each one drains the cache on its way out.
		return nil
	} else if err != nil {
		return err
	}

	wanted, unresolved, err := c.wantedFor(export)
	if err != nil {
		return err
	}

	existing, err := c.listOwnedResources(cluster, export.Name)
	if err != nil {
		return err
	}

	// Anything this APIExport owns but no longer asks for goes. Deleting the
	// ClusterCachedResource is what removes the copies: its finalizer drains
	// them while the kind is still served.
	//
	// Only when every reference resolved, though. With one still unresolved the
	// wanted set is a partial answer, and pruning against it would delete a
	// ClusterCachedResource the APIExport does in fact still ask for -- then
	// recreate it on the next pass, draining and refilling the cache for a kind
	// that never stopped being referenced.
	if len(unresolved) == 0 {
		for _, ccr := range existing {
			if _, keep := wanted[ccr.Name]; keep {
				continue
			}
			if !ccr.DeletionTimestamp.IsZero() {
				continue
			}
			klog.FromContext(ctx).V(2).Info("dropping a ClusterCachedResource nothing references anymore",
				"clusterCachedResource", ccr.Name)
			if err := c.deleteResource(ctx, cluster, ccr.Name); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}

	// Server-side apply what is wanted. The apply configuration names only the
	// fields this controller owns, so an existing ClusterCachedResource keeps
	// whatever others put on it -- the clustercachedresources controller
	// defaults spec.identity in place, and that must survive this write.
	//
	// References that did resolve are applied even while others have not: an
	// APIExport that mixes a settled reference with a brand-new one should not
	// have the settled one held hostage.
	for _, want := range wanted {
		if err := c.applyResource(ctx, cluster, want); err != nil {
			return err
		}
	}

	if len(unresolved) > 0 {
		// Requeue with backoff. The rate limiter starts at milliseconds, so the
		// ordinary case -- a CRD applied moments before the APIExport that names
		// it -- converges about as fast as an event would have delivered it. A
		// reference to a kind that genuinely does not exist settles into a slow
		// retry, which is the right price for a misconfiguration that would
		// otherwise leave no trace anywhere.
		return fmt.Errorf("APIExport %s|%s references %s: not resolvable (yet)", cluster, export.Name, unresolved)
	}

	return nil
}

// wantedFor resolves an APIExport's references to the ClusterCachedResources
// that should exist for them, as apply configurations keyed by name. It also
// reports the references that could not be resolved.
//
// An unresolved reference is neither an error nor an absence. The RESTMapper
// behind restMappingFor is per-replica, in-memory state fed asynchronously by
// its own controller -- and one that deliberately withholds a CRD until it is
// Established. A no-match from it means "this replica does not know that kind
// yet", which is true of every kind for a short while after it is created, and
// stays true forever for a kind that was never created. The two are
// indistinguishable here, so this returns them and lets the caller retry.
func (c *controller) wantedFor(export *apisv1alpha2.APIExport) (map[string]*cachev1alpha1apply.ClusterCachedResourceApplyConfiguration, []reference, error) {
	if !export.DeletionTimestamp.IsZero() {
		// On its way out. The garbage collector handles what it owns.
		return nil, nil, nil
	}

	cluster := logicalcluster.From(export)
	wanted := map[string]*cachev1alpha1apply.ClusterCachedResourceApplyConfiguration{}
	seen := sets.New[resolved]()
	var unresolved []reference

	for _, ref := range referencesFrom(export) {
		mapping, err := c.restMappingFor(cluster, schema.GroupKind{Group: ref.group, Kind: ref.kind})
		if err != nil {
			if meta.IsNoMatchError(err) {
				unresolved = append(unresolved, ref)
				continue
			}
			return nil, nil, err
		}

		r := resolved{gvr: mapping.Resource, name: ref.name}
		if seen.Has(r) {
			continue
		}
		seen.Insert(r)

		wanted[nameFor(export.Name, r)] = desired(export, r)
	}

	return wanted, unresolved, nil
}
