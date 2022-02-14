/*
Copyright 2021 The KCP Authors.

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

package workspaceindex

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpexternalversions "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	tenancyinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancylister "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

const (
	rootShard         = "root"
	currentShardIndex = "shard"
	controllerName    = "workspaceindex-controller"
)

const resyncPeriod = 10 * time.Hour

func NewController(
	kubeClient kubernetes.ClusterInterface,
	workspaceInformer tenancyinformer.ClusterWorkspaceInformer,
	workspaceShardInformer tenancyinformer.WorkspaceShardInformer,
	index Index,
) (*Controller, error) {
	orgQueue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "kcp-workspaceindex-org")
	workspaceQueue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "kcp-workspaceindex-workspace")

	c := &Controller{
		orgQueue:              orgQueue,
		workspaceQueue:        workspaceQueue,
		kubeClient:            kubeClient,
		workspaceIndexer:      workspaceInformer.Informer().GetIndexer(),
		workspaceLister:       workspaceInformer.Lister(),
		workspaceShardIndexer: workspaceShardInformer.Informer().GetIndexer(),
		workspaceShardLister:  workspaceShardInformer.Lister(),
		syncChecks: []cache.InformerSynced{
			workspaceInformer.Informer().HasSynced,
			workspaceShardInformer.Informer().HasSynced,
		},
		shardInformers: map[string]map[string]organizationInformer{},
		index:          index,
	}

	workspaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueOrg(obj)
			c.enqueueWorkspace(rootShard, rootShard, obj)
		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueOrg(obj)
			c.enqueueWorkspace(rootShard, rootShard, obj)
		},
		// TODO: handle deletes
	})
	if err := c.workspaceIndexer.AddIndexers(map[string]cache.IndexFunc{
		currentShardIndex: func(obj interface{}) ([]string, error) {
			if workspace, ok := obj.(*tenancyv1alpha1.ClusterWorkspace); ok {
				return []string{workspace.Status.Location.Current}, nil
			}
			return []string{}, nil
		},
	}); err != nil {
		return nil, fmt.Errorf("failed to add indexer for ClusterWorkspace: %w", err)
	}

	workspaceShardInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueOrgsForShard(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueOrgsForShard(obj) },
	})

	return c, nil
}

// Controller is meant to watch Workspaces and WorkspaceShards in the root logical
// cluster which correspond to the Organizations present in the kcp fleet. Events in
// the root logical cluster are used to create an in-memory record of the set of
// Organizations that need to be watched as well as the credentials with which that
// should be done. For each Organization, Workspaces are watched in order to build a
// mapping of logical cluster name and resource version to the kcp shard on which the
// data will be found.
type Controller struct {
	orgQueue       workqueue.RateLimitingInterface
	workspaceQueue workqueue.RateLimitingInterface

	// activeWorkersLock guards the counts of workers above - we need one lock for both
	// instead of using sync.atomic as we need to read both in one atomic transaction
	activeWorkersLock           sync.RWMutex
	orgQueueActiveWorkers       uint64
	workspaceQueueActiveWorkers uint64

	// kubeClient is used to fetch kubeconfig secrets for shards in the root ClusterWorkspace
	kubeClient kubernetes.ClusterInterface

	workspaceIndexer cache.Indexer
	workspaceLister  tenancylister.ClusterWorkspaceLister

	workspaceShardIndexer cache.Indexer
	workspaceShardLister  tenancylister.WorkspaceShardLister

	syncChecks []cache.InformerSynced

	shardInformersLock sync.RWMutex
	// shardInformers are the set of informers we manage for each shard that orgs are on
	// mapped from shard to logical cluster to informer
	shardInformers map[string]map[string]organizationInformer

	// index holds resourceVersion-bound locations for workspaces through history.
	index Index
}

type organizationInformer struct {
	ctx    context.Context
	cancel func()

	workspaceIndexer cache.Indexer
	workspaceLister  tenancylister.ClusterWorkspaceLister

	credentialsHash string
}

func (c *Controller) enqueueOrg(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	klog.Infof("queueing org workspace %q", key)
	c.orgQueue.Add(key)
}

func (c *Controller) enqueueOrgsForShard(obj interface{}) {
	shard, ok := obj.(*tenancyv1alpha1.WorkspaceShard)
	if !ok {
		klog.V(2).Infof("Enqueued object that is not a WorkspaceShard: %#v", obj)
		return
	}
	klog.Infof("handling shard %q", shard.Name)
	workspaces, err := c.workspaceIndexer.ByIndex(currentShardIndex, shard.Name)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	for _, workspace := range workspaces {
		key, err := cache.MetaNamespaceKeyFunc(workspace)
		if err != nil {
			runtime.HandleError(err)
			return
		}
		klog.Infof("queuing associated org workspace %q", key)
		c.orgQueue.Add(key)
	}
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.orgQueue.ShutDown()

	klog.Info("Starting WorkspaceIndex orchestrator")
	defer klog.Info("Shutting down WorkspaceIndex orchestrator")

	if !cache.WaitForNamedCacheSync(controllerName, ctx.Done(), c.syncChecks...) {
		klog.Warning("Failed to wait for caches to sync")
		return
	}

	for i := 0; i < numThreads; i++ {
		go wait.Until(func() { c.startOrgWorker(ctx) }, time.Second, ctx.Done())
		go wait.Until(func() { c.startWorkspaceWorker(ctx) }, time.Second, ctx.Done())
	}

	<-ctx.Done()
}

func (c *Controller) startOrgWorker(ctx context.Context) {
	for c.processNextOrgWorkItem(ctx) {
	}
}

func (c *Controller) processNextOrgWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.orgQueue.Get()
	if quit {
		return false
	}
	c.activeWorkersLock.Lock()
	c.orgQueueActiveWorkers += 1
	c.activeWorkersLock.Unlock()
	defer func() {
		c.activeWorkersLock.Lock()
		c.orgQueueActiveWorkers -= 1
		c.activeWorkersLock.Unlock()
	}()
	key := k.(string)

	klog.Infof("processing org %q", key)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.orgQueue.Done(key)

	if err := c.processOrg(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		c.orgQueue.AddRateLimited(key)
		return true
	}
	c.orgQueue.Forget(key)
	return true
}

func (c *Controller) processOrg(ctx context.Context, key string) error {
	obj, err := c.workspaceLister.Get(key) // TODO: clients need a way to scope down the lister per-cluster
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}
	obj = obj.DeepCopy()

	return c.handleOrg(ctx, obj)
}

// handleOrg ensures that an informer is running for Workspaces in every Organization, using the
// latest credentials available for the Shard on which the Organization is held
func (c *Controller) handleOrg(ctx context.Context, workspace *tenancyv1alpha1.ClusterWorkspace) error {
	if !conditions.IsTrue(workspace, tenancyv1alpha1.WorkspaceScheduled) ||
		!conditions.IsTrue(workspace, tenancyv1alpha1.WorkspaceURLValid) {
		klog.Infof("org workspace %s/%s not ready, skipping...", workspace.ClusterName, workspace.Name)
		return nil // not ready yet
	}
	logicalCluster, err := helper.EncodeLogicalClusterName(workspace)
	if err != nil {
		klog.Errorf("error creating client with workspace shard credentials: %v", err)
		return nil
	}

	shardKey, err := cache.MetaNamespaceKeyFunc(&metav1.ObjectMeta{
		ClusterName: workspace.ObjectMeta.ClusterName,
		Name:        workspace.Status.Location.Current,
	})
	if err != nil {
		klog.Errorf("failed to get key for workspace shard: %v", err)
		return nil
	}
	workspaceShard, err := c.workspaceShardLister.Get(shardKey)
	if err != nil {
		klog.Errorf("failed to get workspace shard %q: %v", shardKey, err)
		return nil
	}

	var previousCredentialsHash, previousResourceVersion string
	c.shardInformersLock.Lock()
	defer c.shardInformersLock.Unlock()
	if informersByLogicalCluster, ok := c.shardInformers[workspace.Status.Location.Current]; ok {
		if informer, ok := informersByLogicalCluster[logicalCluster]; ok {
			previousCredentialsHash = informer.credentialsHash
			items, err := informer.workspaceLister.List(labels.Everything())
			if err != nil {
				klog.Errorf("failed to list workspaces from previous lister on %s/%s: %v", workspace.Status.Location.Current, logicalCluster, err)
				return nil
			}
			var largestResourceVersion int64
			for _, item := range items {
				rv, err := strconv.ParseInt(item.ResourceVersion, 10, 64)
				if err != nil {
					klog.Errorf("failed to parse resourceVersion for workspace %s from %s/%s: %v", item.Name, workspace.Status.Location.Current, logicalCluster, err)
				}
				if rv > largestResourceVersion {
					largestResourceVersion = rv
				}
			}
			previousResourceVersion = strconv.FormatInt(largestResourceVersion, 10)
		}
	}

	if previousCredentialsHash == workspaceShard.Status.CredentialsHash {
		// nothing to be done
		return nil
	}

	secret, err := c.kubeClient.Cluster(workspaceShard.ClusterName).CoreV1().Secrets(workspaceShard.Spec.Credentials.Namespace).Get(ctx, workspaceShard.Spec.Credentials.Name, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("failed to get workspace shard credentials: %v", shardKey, err)
		return err // retry since this could flake in an otherwise OK situation
	}

	data, ok := secret.Data[tenancyv1alpha1.WorkspaceShardCredentialsKey]
	if !ok {
		klog.Error("workspace shard credentials missing data")
		return nil
	}

	cfg, err := clientcmd.RESTConfigFromKubeConfig(data)
	if err != nil {
		klog.Errorf("workspace shard credentials invalid: %v", err)
		return nil
	}

	var options []kcpexternalversions.SharedInformerOption
	if previousResourceVersion != "" {
		options = append(options, kcpexternalversions.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.ResourceVersion = previousResourceVersion
		}))
	}

	kcpClient, err := kcpclient.NewClusterForConfig(cfg)
	if err != nil {
		klog.Errorf("error creating client with workspace shard credentials: %v", err)
		return nil
	}

	octx, cancel := context.WithCancel(ctx)
	crossClusterKcpClient := kcpClient.Cluster(logicalCluster)
	kcpSharedInformerFactory := kcpexternalversions.NewSharedInformerFactoryWithOptions(crossClusterKcpClient, resyncPeriod, options...)
	workspaceInformer := kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces()

	workspaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueWorkspace(workspaceShard.Name, logicalCluster, obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueWorkspace(workspaceShard.Name, logicalCluster, obj) },
		// TODO: handle deletes
	})

	if previousCredentialsHash != "" {
		c.shardInformers[workspace.Status.Location.Current][logicalCluster].cancel()
		delete(c.shardInformers[workspace.Status.Location.Current], logicalCluster)
	}
	if _, recorded := c.shardInformers[workspace.Status.Location.Current]; !recorded {
		c.shardInformers[workspace.Status.Location.Current] = map[string]organizationInformer{}
	}
	c.shardInformers[workspace.Status.Location.Current][logicalCluster] = organizationInformer{
		ctx:              octx,
		cancel:           cancel,
		workspaceIndexer: workspaceInformer.Informer().GetIndexer(),
		workspaceLister:  workspaceInformer.Lister(),
		credentialsHash:  workspaceShard.Status.CredentialsHash,
	}
	klog.Infof("starting informer for shard %s", workspace.Status.Location.Current)
	// TODO: think through the concurrency here to make sure we're never getting extra events on credential reload
	kcpSharedInformerFactory.Start(octx.Done())
	kcpSharedInformerFactory.WaitForCacheSync(octx.Done())
	klog.Infof("started informer for shard %s", workspace.Status.Location.Current)
	return nil
}

func (c *Controller) enqueueWorkspace(shard, logicalCluster string, obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	key = fmt.Sprintf("%s|%s|%s", shard, logicalCluster, key)
	klog.Infof("queueing workspace %q", key)
	c.workspaceQueue.Add(key)
}

func (c *Controller) startWorkspaceWorker(ctx context.Context) {
	for c.processNextWorkspaceWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkspaceWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.workspaceQueue.Get()
	if quit {
		return false
	}
	c.activeWorkersLock.Lock()
	c.workspaceQueueActiveWorkers += 1
	c.activeWorkersLock.Unlock()
	defer func() {
		c.activeWorkersLock.Lock()
		c.workspaceQueueActiveWorkers -= 1
		c.activeWorkersLock.Unlock()
	}()
	key := k.(string)

	klog.Infof("processing workspace %q", key)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.workspaceQueue.Done(key)

	if err := c.processWorkspace(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		c.workspaceQueue.AddRateLimited(key)
		return true
	}
	c.workspaceQueue.Forget(key)
	return true
}

func (c *Controller) processWorkspace(ctx context.Context, key string) error {
	parts := strings.SplitN(key, "|", 3)
	if len(parts) != 3 {
		// programmer error
		klog.Errorf("invalid key in workspace queue: %s", key)
		return nil
	}
	var shard, logicalCluster string
	shard, logicalCluster, key = parts[0], parts[1], parts[2]
	var lister tenancylister.ClusterWorkspaceLister
	if shard == rootShard {
		lister = c.workspaceLister
	} else {
		informersByLogicalCluster, ok := c.shardInformers[shard]
		if !ok {
			klog.Infof("shard %s in workspace queue has no associated listers", shard)
			return nil
		}
		informer, ok := informersByLogicalCluster[logicalCluster]
		if !ok {
			klog.Infof("shard %s in workspace queue has no associated lister for logical cluster %s", shard, logicalCluster)
			return nil
		}
		lister = informer.workspaceLister
	}
	obj, err := lister.Get(key) // TODO: clients need a way to scope down the lister per-cluster
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}
	obj = obj.DeepCopy()

	return c.index.Record(obj)
}

// Stable exposes whether - to the best of our knowledge - the controller has stabilized.
func (c *Controller) Stable() bool {
	c.activeWorkersLock.RLock()
	defer c.activeWorkersLock.RUnlock()
	orgLen, orgWorkers, workspaceLen, workspaceWorkers := c.orgQueue.Len(), c.orgQueueActiveWorkers, c.workspaceQueue.Len(), c.workspaceQueueActiveWorkers
	klog.Infof("workspaceQueue.Len()=%d, workspaceQueueActiveWorkers=%d, orgQueue.Len()=%d, orgQueueActiveWorkers=%d", workspaceLen, workspaceWorkers, orgLen, orgWorkers)
	return workspaceWorkers == 0 && orgWorkers == 0 && orgLen == 0 && workspaceLen == 0
}
