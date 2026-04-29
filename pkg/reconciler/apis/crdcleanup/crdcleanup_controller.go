/*
Copyright 2022 The kcp Authors.

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

package crdcleanup

import (
	"context"
	"fmt"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpapiextensionsv1informers "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	sdkclient "github.com/kcp-dev/sdk/client"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	"github.com/kcp-dev/kcp/pkg/reconciler/events"
)

const (
	ControllerName = "kcp-crdcleanup"

	AgeThreshold time.Duration = time.Minute * 30
)

// NewController returns a new controller for CRD cleanup.
func NewController(
	crdInformer kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer,
	crdClusterClient kcpapiextensionsclientset.ClusterInterface,
	apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer,
	apiExportInformer apisv1alpha2informers.APIExportClusterInformer,
	globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
) (*controller, error) {
	c := &controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		getCRD: func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
			return crdInformer.Lister().Cluster(clusterName).Get(name)
		},
		listBoundCRDsByGroupResource: func(gr schema.GroupResource) ([]*apiextensionsv1.CustomResourceDefinition, error) {
			crds, err := crdInformer.Lister().Cluster(apibinding.SystemBoundCRDsClusterName).List(labels.Everything())
			if err != nil {
				return nil, err
			}
			var matching []*apiextensionsv1.CustomResourceDefinition
			for _, crd := range crds {
				if crd.Spec.Group == gr.Group && crd.Spec.Names.Plural == gr.Resource {
					matching = append(matching, crd)
				}
			}
			return matching, nil
		},
		getAPIBindingsByBoundResourceUID: func(name string) ([]*apisv1alpha2.APIBinding, error) {
			return indexers.ByIndex[*apisv1alpha2.APIBinding](apiBindingInformer.Informer().GetIndexer(), indexers.APIBindingByBoundResourceUID, name)
		},
		// getAPIBindingsClaimingCRD walks back from a bound CRD to APIBindings that
		// reference it via accepted permission claims. The CRD's schema-cluster +
		// schema-name annotations identify the source APIResourceSchema; the APIExport
		// indexer locates its owning export(s); a binding with an accepted claim whose
		// (identityHash, group, resource) matches counts as a referrer. See issue #4087.
		getAPIBindingsClaimingCRD: func(crd *apiextensionsv1.CustomResourceDefinition) ([]*apisv1alpha2.APIBinding, error) {
			schemaCluster := crd.Annotations[apisv1alpha1.AnnotationSchemaClusterKey]
			schemaName := crd.Annotations[apisv1alpha1.AnnotationSchemaNameKey]
			if schemaCluster == "" || schemaName == "" {
				return nil, nil
			}
			schemaKey := sdkclient.ToClusterAwareKey(logicalcluster.NewPath(schemaCluster), schemaName)
			exports, err := indexers.ByIndexWithFallback[*apisv1alpha2.APIExport](
				apiExportInformer.Informer().GetIndexer(),
				globalAPIExportInformer.Informer().GetIndexer(),
				indexers.APIExportByAPIResourceSchema, schemaKey,
			)
			if err != nil {
				return nil, err
			}
			gr := schema.GroupResource{Group: crd.Spec.Group, Resource: crd.Spec.Names.Plural}
			var refs []*apisv1alpha2.APIBinding
			seen := sets.New[string]()
			for _, exp := range exports {
				if exp.Status.IdentityHash == "" {
					continue
				}
				bindings, err := indexers.ByIndex[*apisv1alpha2.APIBinding](
					apiBindingInformer.Informer().GetIndexer(),
					indexers.APIBindingByAcceptedClaimIdentityAndGR,
					indexers.IdentityGroupResourceKeyFunc(exp.Status.IdentityHash, gr.Group, gr.Resource),
				)
				if err != nil {
					return nil, err
				}
				for _, b := range bindings {
					k, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(b)
					if err != nil {
						continue
					}
					if seen.Has(k) {
						continue
					}
					seen.Insert(k)
					refs = append(refs, b)
				}
			}
			return refs, nil
		},
		deleteCRD: func(ctx context.Context, name string) error {
			return crdClusterClient.ApiextensionsV1().CustomResourceDefinitions().Cluster(apibinding.SystemBoundCRDsClusterName.Path()).Delete(ctx, name, metav1.DeleteOptions{})
		},
	}

	_, _ = crdInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			crd := obj.(*apiextensionsv1.CustomResourceDefinition)
			return logicalcluster.From(crd) == apibinding.SystemBoundCRDsClusterName
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				c.enqueueCRD(obj.(*apiextensionsv1.CustomResourceDefinition))
			},
			UpdateFunc: func(_, obj interface{}) {
				c.enqueueCRD(obj.(*apiextensionsv1.CustomResourceDefinition))
			},
			DeleteFunc: func(obj interface{}) {
				c.enqueueCRD(obj.(*apiextensionsv1.CustomResourceDefinition))
			},
		},
	})

	_, _ = apiBindingInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.enqueueFromAPIBinding(oldObj.(*apisv1alpha2.APIBinding), newObj.(*apisv1alpha2.APIBinding))
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueFromAPIBinding(nil, obj.(*apisv1alpha2.APIBinding))
		},
	}))

	return c, nil
}

// controller deletes bound CRDs when they are no longer in use by any APIBindings.
type controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	getCRD                           func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error)
	listBoundCRDsByGroupResource     func(gr schema.GroupResource) ([]*apiextensionsv1.CustomResourceDefinition, error)
	getAPIBindingsByBoundResourceUID func(name string) ([]*apisv1alpha2.APIBinding, error)
	getAPIBindingsClaimingCRD        func(crd *apiextensionsv1.CustomResourceDefinition) ([]*apisv1alpha2.APIBinding, error)
	deleteCRD                        func(ctx context.Context, name string) error
}

// enqueueCRD enqueues a CRD.
func (c *controller) enqueueCRD(crd *apiextensionsv1.CustomResourceDefinition) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(crd)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing CRD")
	c.queue.Add(key)
}

func (c *controller) enqueueFromAPIBinding(oldBinding, newBinding *apisv1alpha2.APIBinding) {
	logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), newBinding)

	// Looking at old and new versions in case a schema gets removed from an APIExport.
	// In that case, the last APIBinding to have the schema removed will trigger the CRD delete,
	// but only the old version will have the reference to the schema.

	uidSet := sets.New[string]()

	if oldBinding != nil {
		for _, boundResource := range oldBinding.Status.BoundResources {
			uidSet.Insert(boundResource.Schema.UID)
		}
	}

	for _, boundResource := range newBinding.Status.BoundResources {
		uidSet.Insert(boundResource.Schema.UID)
	}

	for uid := range uidSet {
		key := kcpcache.ToClusterAwareKey(apibinding.SystemBoundCRDsClusterName.String(), "", uid)
		logging.WithQueueKey(logger, key).V(3).Info("queueing CRD via APIBinding")
		c.queue.Add(key)
	}

	// Permission-claim referrers are tracked indirectly: a binding's accepted
	// permission claims keep matching CRDs alive even though the CRD's UID never
	// appears in BoundResources. When a binding's claims change (or it is deleted),
	// re-evaluate every bound CRD whose group/resource matches an old or new claim.
	// process() will then resolve back via APIExport identity hash and decide.
	claimGRs := sets.New[schema.GroupResource]()
	collect := func(b *apisv1alpha2.APIBinding) {
		if b == nil {
			return
		}
		for _, pc := range b.Spec.PermissionClaims {
			if pc.State != apisv1alpha2.ClaimAccepted {
				continue
			}
			if pc.IdentityHash == "" {
				continue
			}
			claimGRs.Insert(schema.GroupResource{Group: pc.Group, Resource: pc.Resource})
		}
	}
	collect(oldBinding)
	collect(newBinding)
	for gr := range claimGRs {
		crds, err := c.listBoundCRDsByGroupResource(gr)
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("listing bound CRDs for %s: %w", gr, err))
			continue
		}
		for _, crd := range crds {
			key := kcpcache.ToClusterAwareKey(apibinding.SystemBoundCRDsClusterName.String(), "", crd.Name)
			logging.WithQueueKey(logger, key).V(3).Info("queueing CRD via APIBinding permission claim")
			c.queue.Add(key)
		}
	}
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for range numThreads {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	cluster, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		return err
	}
	clusterName := logicalcluster.Name(cluster.String()) // TODO: remove this when SplitMetaClusterNamespaceKey returns a tenancy.Name

	obj, err := c.getCRD(clusterName, name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}

	logger := logging.WithObject(klog.FromContext(ctx), obj)
	ctx = klog.NewContext(ctx, logger)

	result, err := c.getAPIBindingsByBoundResourceUID(obj.Name)
	if err != nil {
		return err
	}

	if len(result) > 0 {
		// An APIBinding that uses this bound CRD was found. Thus don't delete.
		return nil
	}

	// Also consult the permission-claim referrer path: a binding may keep this CRD
	// alive without listing its UID in BoundResources.
	claimRefs, err := c.getAPIBindingsClaimingCRD(obj)
	if err != nil {
		return err
	}
	if len(claimRefs) > 0 {
		return nil
	}

	age := time.Since(obj.CreationTimestamp.Time)

	if age < AgeThreshold {
		duration := AgeThreshold - age
		logger.V(4).Info("Requeuing until CRD is older to give some time for the bindings to complete initialization", "duration", duration)
		c.queue.AddAfter(key, duration)
		return nil
	}

	logger.V(2).Info("Deleting CRD")
	if err := c.deleteCRD(ctx, obj.Name); err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}

	return nil
}

// InstallIndexers adds the additional indexers that this controller requires to the informers.
func InstallIndexers(
	apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer,
	apiExportInformer apisv1alpha2informers.APIExportClusterInformer,
	globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
) {
	indexers.AddIfNotPresentOrDie(
		apiBindingInformer.Informer().GetIndexer(),
		cache.Indexers{
			indexers.APIBindingByBoundResourceUID:           indexers.IndexAPIBindingByBoundResourceUID,
			indexers.APIBindingByAcceptedClaimIdentityAndGR: indexers.IndexAPIBindingByAcceptedClaimIdentityAndGR,
		},
	)
	// AddIfNotPresentOrDie is a no-op if the index name is already registered (the
	// apibinding controller registers the same indexer under the same name).
	for _, inf := range []apisv1alpha2informers.APIExportClusterInformer{apiExportInformer, globalAPIExportInformer} {
		indexers.AddIfNotPresentOrDie(inf.Informer().GetIndexer(), cache.Indexers{
			indexers.APIExportByAPIResourceSchema: indexers.IndexAPIExportByAPIResourceSchema,
		})
	}
}
