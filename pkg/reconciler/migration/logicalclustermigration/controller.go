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
	"fmt"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpapiextensionsv1informers "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	migrationv1alpha1client "github.com/kcp-dev/sdk/client/clientset/versioned/typed/migration/v1alpha1"
	apisv1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha1"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"
	migrationv1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/migration/v1alpha1"
	apisv1alpha1listers "github.com/kcp-dev/sdk/client/listers/apis/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
)

const (
	ControllerName = "kcp-logicalcluster-migration"

	// MigratingAnnotationKey is the annotation set on the LogicalCluster object to
	// indicate it is currently being migrated. The value is the cluster path
	// of the LogicalClusterMigration object that triggered the migration.
	MigratingAnnotationKey = "internal.kcp.io/migrating"
)

func NewController(
	shardName string,
	kcpClusterClient kcpclientset.ClusterInterface,
	crdClusterClient kcpapiextensionsclientset.ClusterInterface,
	externalLogicalClusterAdminConfig *rest.Config,
	etcdClient *clientv3.Client,
	etcdStoragePrefix string,
	cachedLogicalClusterMigrationInformer migrationv1alpha1informers.LogicalClusterMigrationClusterInformer,
	localLogicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
	cachedShardInformer corev1alpha1informers.ShardClusterInformer,
	crdInformer kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer,
	apiExportInformer, globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
	apiResourceSchemaInformer, globalAPIResourceSchemaInformer apisv1alpha1informers.APIResourceSchemaClusterInformer,
	migratingLogicalClusters *MigratingLogicalClusters,
	cancelLogicalClusterConnections func(logicalcluster.Path, error),
	ddsif *informer.DiscoveringDynamicSharedInformerFactory,
) (*Controller, error) {
	c := &Controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		shardName:                         shardName,
		kcpClusterClient:                  kcpClusterClient,
		crdClusterClient:                  crdClusterClient,
		externalLogicalClusterAdminConfig: externalLogicalClusterAdminConfig,
		etcdClient:                        etcdClient,
		etcdStoragePrefix:                 etcdStoragePrefix,
		logicalClusterLister:              localLogicalClusterInformer.Lister(),
		shardLister:                       cachedShardInformer.Lister(),
		migratingLogicalClusters:          migratingLogicalClusters,
		cancelLogicalClusterConnections:   cancelLogicalClusterConnections,
		ddsif:                             ddsif,
		commit:                            committer.NewCommitter[*migrationv1alpha1.LogicalClusterMigration, migrationv1alpha1client.LogicalClusterMigrationInterface, *migrationv1alpha1.LogicalClusterMigrationSpec, *migrationv1alpha1.LogicalClusterMigrationStatus](kcpClusterClient.MigrationV1alpha1().LogicalClusterMigrations()),
		getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](apisv1alpha2.Resource("apiexports"), apiExportInformer.Informer().GetIndexer(), globalAPIExportInformer.Informer().GetIndexer(), path, name)
		},
		getAPIResourceSchema: informer.NewScopedGetterWithFallback[*apisv1alpha1.APIResourceSchema, apisv1alpha1listers.APIResourceSchemaLister](apiResourceSchemaInformer.Lister(), globalAPIResourceSchemaInformer.Lister()),
		getCRD: func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
			return crdInformer.Lister().Cluster(clusterName).Get(name)
		},
		originClients: make(map[string]*originClientEntry),
	}
	c.copyPageFromOrigin = c.copyPageFromOriginViaHTTP

	// Events are queued from the cache server as that is used to
	// coordinate the migration between the participating shards.
	// The updates are written through the front-proxy to the
	// LogicalClusterMigration, the owning shard then replicates these
	// changes to the cache server which then triggers the next phase in
	// the respective shard.
	_, _ = cachedLogicalClusterMigrationInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(obj) },
	})

	return c, nil
}

type logicalClusterMigrationResource = committer.Resource[*migrationv1alpha1.LogicalClusterMigrationSpec, *migrationv1alpha1.LogicalClusterMigrationStatus]

type Controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	shardName string

	kcpClusterClient                  kcpclientset.ClusterInterface
	crdClusterClient                  kcpapiextensionsclientset.ClusterInterface
	externalLogicalClusterAdminConfig *rest.Config
	etcdClient                        *clientv3.Client
	etcdStoragePrefix                 string

	logicalClusterLister corev1alpha1listers.LogicalClusterClusterLister
	shardLister          corev1alpha1listers.ShardClusterLister

	migratingLogicalClusters        *MigratingLogicalClusters
	cancelLogicalClusterConnections func(logicalcluster.Path, error)
	ddsif                           *informer.DiscoveringDynamicSharedInformerFactory

	commit func(ctx context.Context, old, new *logicalClusterMigrationResource) error

	getAPIExportByPath   func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	getAPIResourceSchema func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
	getCRD               func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error)

	// copyPageFromOrigin fetches and writes a single page of a
	// LogicalClusterDump. Extracted as a field (defaulting to
	// copyPageFromOriginViaHTTP) so reconcileMigrating's pagination and
	// requeue logic can be unit-tested without a live origin shard.
	copyPageFromOrigin func(ctx context.Context, lcName logicalcluster.Name, originShardName, continueToken string) (int64, string, error)

	// originClientsMu guards originClients.
	originClientsMu sync.Mutex
	// originClients caches one client per origin shard, shared by every
	// concurrent migration copying data from that shard, so they reuse
	// the same underlying HTTP transport and connection pool instead of
	// each building their own. Entries are refcounted (see
	// originClientEntry) and only torn down once no migration is using
	// them anymore.
	originClients map[string]*originClientEntry
}

// originClientEntry is a refcounted, shared client for one origin shard.
// refs tracks which logical clusters currently have a migration actively
// copying data from this shard; the entry is dropped once refs is empty.
type originClientEntry struct {
	client kcpclientset.ClusterInterface
	refs   map[logicalcluster.Name]struct{}
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing LogicalClusterMigration")
	c.queue.Add(key)
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for range numThreads {
		go wait.Until(func() { c.startWorker(ctx) }, time.Second, ctx.Done())
	}

	<-ctx.Done()
}

func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

	defer c.queue.Done(key)

	if requeue, err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	} else if requeue {
		c.queue.Add(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) (bool, error) {
	logger := klog.FromContext(ctx)

	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "unable to decode key")
		return false, nil
	}

	migration, err := c.kcpClusterClient.Cluster(clusterName.Path()).MigrationV1alpha1().LogicalClusterMigrations().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	old := migration
	migration = migration.DeepCopy()

	logger = logging.WithObject(logger, migration)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	requeue, err := c.reconcile(ctx, migration)
	if err != nil {
		errs = append(errs, err)
	}

	oldResource := &logicalClusterMigrationResource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
	newResource := &logicalClusterMigrationResource{ObjectMeta: migration.ObjectMeta, Spec: &migration.Spec, Status: &migration.Status}
	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return requeue, utilerrors.NewAggregate(errs)
}

// isOrigin returns true if this shard is the origin of the migration.
// Once the origin shard has set status.originShard, we use that as the source of truth.
// On first reconcile (before status is set), we check whether the LC exists locally.
func (c *Controller) isOrigin(migration *migrationv1alpha1.LogicalClusterMigration) bool {
	if migration.Status.OriginShard != "" {
		return migration.Status.OriginShard == c.shardName
	}
	lcName := logicalcluster.Name(migration.Spec.LogicalCluster)
	_, err := c.logicalClusterLister.Cluster(lcName).Get(corev1alpha1.LogicalClusterName)
	return err == nil
}

// isDestination returns true if this shard is the intended destination.
func (c *Controller) isDestination(migration *migrationv1alpha1.LogicalClusterMigration) bool {
	return migration.Spec.DestinationShard == c.shardName
}
