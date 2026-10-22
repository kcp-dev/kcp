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

package apiexport

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpcorev1informers "github.com/kcp-dev/client-go/informers/core/v1"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	apisv1alpha2client "github.com/kcp-dev/sdk/client/clientset/versioned/typed/apis/v1alpha2"
	apisv1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha1"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	"github.com/kcp-dev/kcp/pkg/reconciler/events"
	kcpmetrics "github.com/kcp-dev/kcp/pkg/server/metrics"
	"github.com/kcp-dev/kcp/pkg/tombstone"
	builtinapiexport "github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas/builtin"
)

const (
	ControllerName = "kcp-apiexport"

	DefaultIdentitySecretNamespace = "kcp-system"
)

// NewController returns a new controller for APIExports.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	apiExportInformer apisv1alpha2informers.APIExportClusterInformer,
	globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
	deepSARClient kcpkubernetesclientset.ClusterInterface,
	apiExportEndpointSliceInformer apisv1alpha1informers.APIExportEndpointSliceClusterInformer,
	globalShardInformer corev1alpha1informers.ShardClusterInformer,
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	namespaceInformer kcpcorev1informers.NamespaceClusterInformer,
	secretInformer kcpcorev1informers.SecretClusterInformer,
	shardName string,
) (*controller, error) {
	c := &controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),

		shardName:         shardName,
		kcpClusterClient:  kcpClusterClient,
		kubeClusterClient: kubeClusterClient,

		listAPIExports: func() ([]*apisv1alpha2.APIExport, error) {
			return apiExportInformer.Lister().List(labels.Everything())
		},
		listAPIExportsForSecret: func(secret *corev1.Secret) ([]*apisv1alpha2.APIExport, error) {
			secretKey, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(secret)
			if err != nil {
				return nil, err
			}

			return indexers.ByIndex[*apisv1alpha2.APIExport](apiExportInformer.Informer().GetIndexer(), indexers.APIExportBySecret, secretKey)
		},
		getAPIExport: func(clusterName logicalcluster.Name, name string) (*apisv1alpha2.APIExport, error) {
			return apiExportInformer.Lister().Cluster(clusterName).Get(name)
		},
		getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](apisv1alpha2.Resource("apiexports"), apiExportInformer.Informer().GetIndexer(), globalAPIExportInformer.Informer().GetIndexer(), path, name)
		},
		getAPIExportsByIdentity: func(identityHash string) ([]*apisv1alpha2.APIExport, error) {
			return indexers.ByIndexWithFallback[*apisv1alpha2.APIExport](apiExportInformer.Informer().GetIndexer(), globalAPIExportInformer.Informer().GetIndexer(), indexers.APIExportByIdentity, identityHash)
		},
		listAPIExportsByClaimGrant: func(key string) ([]*apisv1alpha2.APIExport, error) {
			// local and global views are merged: an export is in both when
			// it is hosted here and replicated.
			local, err := indexers.ByIndex[*apisv1alpha2.APIExport](apiExportInformer.Informer().GetIndexer(), indexers.APIExportByClaimGrant, key)
			if err != nil {
				return nil, err
			}
			global, err := indexers.ByIndex[*apisv1alpha2.APIExport](globalAPIExportInformer.Informer().GetIndexer(), indexers.APIExportByClaimGrant, key)
			if err != nil {
				return nil, err
			}
			seen := map[string]bool{}
			merged := make([]*apisv1alpha2.APIExport, 0, len(local)+len(global))
			for _, export := range append(local, global...) {
				k := logicalcluster.From(export).String() + "|" + export.Name
				if seen[k] {
					continue
				}
				seen[k] = true
				merged = append(merged, export)
			}
			return merged, nil
		},
		canonicalIdentityHash: func(hash, group, resource string) string {
			return indexers.CanonicalIdentityHashForGR(globalAPIExportInformer.Informer().GetIndexer(), hash, group, resource)
		},
		isBuiltIn: builtinapiexport.IsBuiltInAPI,
		getAPIExportEndpointSlice: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIExportEndpointSlice, error) {
			return apiExportEndpointSliceInformer.Lister().Cluster(clusterName).Get(name)
		},
		createAPIExportEndpointSlice: func(ctx context.Context, clusterName logicalcluster.Path, apiExportEndpointSlice *apisv1alpha1.APIExportEndpointSlice) error {
			_, err := kcpClusterClient.Cluster(clusterName).ApisV1alpha1().APIExportEndpointSlices().Create(ctx, apiExportEndpointSlice, metav1.CreateOptions{})
			return err
		},

		getNamespace: func(clusterName logicalcluster.Name, name string) (*corev1.Namespace, error) {
			return namespaceInformer.Lister().Cluster(clusterName).Get(name)
		},
		createNamespace: func(ctx context.Context, clusterName logicalcluster.Path, ns *corev1.Namespace) error {
			_, err := kubeClusterClient.Cluster(clusterName).CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
			return err
		},
		secretNamespace: DefaultIdentitySecretNamespace,

		getSecret: func(ctx context.Context, clusterName logicalcluster.Name, ns, name string) (*corev1.Secret, error) {
			secret, err := secretInformer.Lister().Cluster(clusterName).Secrets(ns).Get(name)
			if err == nil {
				return secret, nil
			}

			// In case the lister is slow to catch up, try a live read
			secret, err = kubeClusterClient.Cluster(clusterName.Path()).CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}

			return secret, nil
		},
		createSecret: func(ctx context.Context, clusterName logicalcluster.Path, secret *corev1.Secret) error {
			_, err := kubeClusterClient.Cluster(clusterName).CoreV1().Secrets(secret.Namespace).Create(ctx, secret, metav1.CreateOptions{})
			return err
		},

		listShards: func() ([]*corev1alpha1.Shard, error) {
			return globalShardInformer.Lister().List(labels.Everything())
		},

		commit: committer.NewCommitter[*APIExport, Patcher, *APIExportSpec, *APIExportStatus](kcpClusterClient.ApisV1alpha2().APIExports()),

		countedAPIExportConditions: make(map[string]map[string]string),
		readyAPIExports:            make(map[string]struct{}),
	}
	if deepSARClient != nil {
		c.newDeepSARAuthorizer = func(clusterName logicalcluster.Name) (authorizer.Authorizer, error) {
			return delegated.NewDelegatedAuthorizer(clusterName, deepSARClient, delegated.Options{})
		}
	}

	_, _ = apiExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			export := obj.(*apisv1alpha2.APIExport)
			c.enqueueAPIExport(export)
			c.handleConditionMetricsOnAdd(export)
			c.handleReadyDurationMetricOnAdd(export)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			export := newObj.(*apisv1alpha2.APIExport)
			c.enqueueAPIExport(export)
			c.handleConditionMetricsOnUpdate(oldObj.(*apisv1alpha2.APIExport), export)
			c.handleReadyDurationMetricOnUpdate(export)
		},
		DeleteFunc: func(obj interface{}) {
			export := tombstone.Obj[*apisv1alpha2.APIExport](obj)
			c.enqueueAPIExport(export)
			c.handleConditionMetricsOnDelete(export)
			c.handleReadyDurationMetricOnDelete(export)
		},
	})

	// grants, sources of adopted storage and claimed exports may live on other
	// shards: re-evaluate every local export when the global view changes.
	_, _ = globalAPIExportInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAllAPIExportsFor(obj) },
		UpdateFunc: func(_, newObj interface{}) { c.enqueueAllAPIExportsFor(newObj) },
		DeleteFunc: func(obj interface{}) { c.enqueueAllAPIExportsFor(obj) },
	}))

	_, _ = secretInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueSecret(obj.(*corev1.Secret))
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueSecret(newObj.(*corev1.Secret))
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueSecret(tombstone.Obj[*corev1.Secret](obj))
		},
	}))

	_, _ = globalShardInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAllAPIExports(obj.(*corev1alpha1.Shard))
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueAllAPIExports(newObj.(*corev1alpha1.Shard))
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAllAPIExports(tombstone.Obj[*corev1alpha1.Shard](obj))
		},
	},
	))

	_, _ = apiExportEndpointSliceInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) {
			c.enqueueAllApiExportEndpointSlices(tombstone.Obj[*apisv1alpha1.APIExportEndpointSlice](obj))
		},
	},
	))

	return c, nil
}

type APIExport = apisv1alpha2.APIExport
type APIExportSpec = apisv1alpha2.APIExportSpec
type APIExportStatus = apisv1alpha2.APIExportStatus
type Patcher = apisv1alpha2client.APIExportInterface
type Resource = committer.Resource[*APIExportSpec, *APIExportStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

// controller reconciles APIExports. It ensures an export's identity secret exists and is valid.
type controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	shardName         string
	kcpClusterClient  kcpclientset.ClusterInterface
	kubeClusterClient kcpkubernetesclientset.ClusterInterface

	listAPIExports          func() ([]*apisv1alpha2.APIExport, error)
	listAPIExportsForSecret func(secret *corev1.Secret) ([]*apisv1alpha2.APIExport, error)
	getAPIExport            func(clusterName logicalcluster.Name, name string) (*apisv1alpha2.APIExport, error)

	// decoupling (enhancements KEP 0008): adopted storage, claim grants,
	// identity pins and alias detection need the installation-wide view.
	getAPIExportByPath         func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	getAPIExportsByIdentity    func(identityHash string) ([]*apisv1alpha2.APIExport, error)
	listAPIExportsByClaimGrant func(key string) ([]*apisv1alpha2.APIExport, error)
	canonicalIdentityHash      func(hash, group, resource string) string
	isBuiltIn                  func(apis.GroupResource) bool
	newDeepSARAuthorizer       func(clusterName logicalcluster.Name) (authorizer.Authorizer, error)

	getAPIExportEndpointSlice    func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIExportEndpointSlice, error)
	createAPIExportEndpointSlice func(ctx context.Context, clusterName logicalcluster.Path, apiExportEndpointSlice *apisv1alpha1.APIExportEndpointSlice) error

	getNamespace    func(clusterName logicalcluster.Name, name string) (*corev1.Namespace, error)
	createNamespace func(ctx context.Context, clusterName logicalcluster.Path, ns *corev1.Namespace) error

	secretNamespace string

	getSecret    func(ctx context.Context, clusterName logicalcluster.Name, ns, name string) (*corev1.Secret, error)
	createSecret func(ctx context.Context, clusterName logicalcluster.Path, secret *corev1.Secret) error

	listShards func() ([]*corev1alpha1.Shard, error)

	commit CommitFunc

	// metricsMu protects countedAPIExportConditions and readyAPIExports.
	metricsMu                  sync.Mutex
	countedAPIExportConditions map[string]map[string]string
	readyAPIExports            map[string]struct{}
}

// enqueueAPIExport enqueues an APIExport.
func (c *controller) enqueueAPIExport(apiExport *apisv1alpha2.APIExport) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(apiExport)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing APIExport")
	c.queue.Add(key)
}

func (c *controller) enqueueAllAPIExports(shard *corev1alpha1.Shard) {
	list, err := c.listAPIExports()
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), shard)
	for i := range list {
		key, err := kcpcache.MetaClusterNamespaceKeyFunc(list[i])
		if err != nil {
			utilruntime.HandleError(err)
			continue
		}

		logging.WithQueueKey(logger, key).V(3).Info("queuing APIExport because Shard changed")
		c.queue.Add(key)
	}
}

func (c *controller) enqueueSecret(secret *corev1.Secret) {
	apiExports, err := c.listAPIExportsForSecret(secret)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), secret)
	for _, apiExport := range apiExports {
		key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(apiExport)
		if err != nil {
			utilruntime.HandleError(err)
			return
		}
		logging.WithQueueKey(logger, key).V(3).Info("queueing APIExport via identity Secret")
		c.queue.Add(key)
	}
}

func (c *controller) enqueueAllApiExportEndpointSlices(apiExportEndpointSlice *apisv1alpha1.APIExportEndpointSlice) {
	clusterName := logicalcluster.From(apiExportEndpointSlice)

	apiExportName := apiExportEndpointSlice.Spec.APIExport.Name

	apiExport, err := c.getAPIExport(clusterName, apiExportName)
	if err != nil {
		if errors.IsNotFound(err) {
			return
		}
		utilruntime.HandleError(err)
		return
	}

	c.enqueueAPIExport(apiExport)
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
		utilruntime.HandleError(err)
		return nil
	}

	obj, err := c.getAPIExport(cluster, name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}

	old := obj
	obj = obj.DeepCopy()

	logger := logging.WithObject(klog.FromContext(ctx), obj)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	if err := c.reconcile(ctx, obj); err != nil {
		errs = append(errs, err)
	}

	// Regardless of whether reconcile returned an error or not, always try to patch status if needed. Return the
	// reconciliation error at the end.

	// If the object being reconciled changed as a result, update it.
	oldResource := &Resource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
	newResource := &Resource{ObjectMeta: obj.ObjectMeta, Spec: &obj.Spec, Status: &obj.Status}
	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return utilerrors.NewAggregate(errs)
}

// InstallIndexers adds the additional indexers that this controller requires to the informers.
func InstallIndexers(apiExportInformer, globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer) {
	indexers.AddIfNotPresentOrDie(
		apiExportInformer.Informer().GetIndexer(),
		cache.Indexers{
			indexers.APIExportByIdentity:   indexers.IndexAPIExportByIdentity,
			indexers.APIExportBySecret:     indexers.IndexAPIExportBySecret,
			indexers.APIExportByClaimGrant: indexers.IndexAPIExportByClaimGrant,
		},
	)
	indexers.AddIfNotPresentOrDie(
		globalAPIExportInformer.Informer().GetIndexer(),
		cache.Indexers{
			indexers.APIExportByIdentity:   indexers.IndexAPIExportByIdentity,
			indexers.APIExportByClaimGrant: indexers.IndexAPIExportByClaimGrant,
		},
	)
}

// enqueueAllAPIExportsFor re-enqueues every local export because an export
// elsewhere changed. Cheap enough: exports are few and the reconcile is
// index-driven.
func (c *controller) enqueueAllAPIExportsFor(obj interface{}) {
	if _, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = obj.(cache.DeletedFinalStateUnknown).Obj
	}
	if _, ok := obj.(*apisv1alpha2.APIExport); !ok {
		return
	}
	list, err := c.listAPIExports()
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	for _, export := range list {
		c.enqueueAPIExport(export)
	}
}

func (c *controller) handleConditionMetricsOnAdd(export *apisv1alpha2.APIExport) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(export)
	if err != nil {
		return
	}

	snapshot := make(map[string]string, len(export.Status.Conditions))
	for _, cond := range export.Status.Conditions {
		snapshot[string(cond.Type)] = string(cond.Status)
	}

	c.metricsMu.Lock()
	defer c.metricsMu.Unlock()

	if _, exists := c.countedAPIExportConditions[key]; exists {
		return
	}
	c.countedAPIExportConditions[key] = snapshot
	for condType, status := range snapshot {
		kcpmetrics.IncrementAPIExportConditionStatus(c.shardName, condType, status)
	}
}

func (c *controller) handleConditionMetricsOnUpdate(oldExport, newExport *apisv1alpha2.APIExport) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(newExport)
	if err != nil {
		return
	}

	newSnapshot := make(map[string]string, len(newExport.Status.Conditions))
	for _, cond := range newExport.Status.Conditions {
		newSnapshot[string(cond.Type)] = string(cond.Status)
	}

	c.metricsMu.Lock()
	defer c.metricsMu.Unlock()

	oldSnapshot := c.countedAPIExportConditions[key]

	for condType, oldStatus := range oldSnapshot {
		newStatus, exists := newSnapshot[condType]
		if !exists || newStatus != oldStatus {
			kcpmetrics.DecrementAPIExportConditionStatus(c.shardName, condType, oldStatus)
		}
	}
	for condType, newStatus := range newSnapshot {
		oldStatus, exists := oldSnapshot[condType]
		if !exists || oldStatus != newStatus {
			kcpmetrics.IncrementAPIExportConditionStatus(c.shardName, condType, newStatus)
		}
	}

	c.countedAPIExportConditions[key] = newSnapshot
}

func (c *controller) handleConditionMetricsOnDelete(export *apisv1alpha2.APIExport) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(export)
	if err != nil {
		return
	}

	c.metricsMu.Lock()
	defer c.metricsMu.Unlock()

	snapshot, exists := c.countedAPIExportConditions[key]
	if !exists {
		return
	}
	for condType, status := range snapshot {
		kcpmetrics.DecrementAPIExportConditionStatus(c.shardName, condType, status)
	}
	delete(c.countedAPIExportConditions, key)
}

func isAPIExportReady(export *apisv1alpha2.APIExport) bool {
	identityValid := false
	urlsReady := false
	for _, cond := range export.Status.Conditions {
		switch cond.Type {
		case apisv1alpha2.APIExportIdentityValid:
			identityValid = cond.Status == corev1.ConditionTrue
		case apisv1alpha2.APIExportVirtualWorkspaceURLsReady:
			urlsReady = cond.Status == corev1.ConditionTrue
		}
	}
	return identityValid && urlsReady
}

func (c *controller) handleReadyDurationMetricOnAdd(export *apisv1alpha2.APIExport) {
	if !isAPIExportReady(export) {
		return
	}
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(export)
	if err != nil {
		return
	}

	c.metricsMu.Lock()
	defer c.metricsMu.Unlock()

	// On add (including restarts), just mark the export as already seen so that
	// a subsequent update does not re-record the ready duration.
	c.readyAPIExports[key] = struct{}{}
}

func (c *controller) handleReadyDurationMetricOnUpdate(export *apisv1alpha2.APIExport) {
	if !isAPIExportReady(export) {
		return
	}
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(export)
	if err != nil {
		return
	}

	c.metricsMu.Lock()
	defer c.metricsMu.Unlock()

	if _, alreadyObserved := c.readyAPIExports[key]; alreadyObserved {
		return
	}
	c.readyAPIExports[key] = struct{}{}
	kcpmetrics.ObserveAPIExportReadyDuration(c.shardName, export.CreationTimestamp.Time)
}

func (c *controller) handleReadyDurationMetricOnDelete(export *apisv1alpha2.APIExport) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(export)
	if err != nil {
		return
	}

	c.metricsMu.Lock()
	defer c.metricsMu.Unlock()

	delete(c.readyAPIExports, key)
}
