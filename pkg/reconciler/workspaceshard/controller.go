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

package workspaceshard

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformer "k8s.io/client-go/informers/core/v1"
	corelister "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	tenancyinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancylister "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	conditionsapi "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

const (
	secretIndex    = "secret"
	controllerName = "workspaceshard"
)

func NewController(
	rootKcpClient kcpclient.Interface,
	rootSecretInformer coreinformer.SecretInformer,
	rootWorkspaceShardInformer tenancyinformer.WorkspaceShardInformer,
) (*Controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "kcp-workspaceshard")

	c := &Controller{
		queue:                     queue,
		kcpClient:                 rootKcpClient,
		rootSecretIndexer:         rootSecretInformer.Informer().GetIndexer(),
		rootSecretLister:          rootSecretInformer.Lister(),
		rootWorkspaceShardIndexer: rootWorkspaceShardInformer.Informer().GetIndexer(),
		rootWorkspaceShardLister:  rootWorkspaceShardInformer.Lister(),
		syncChecks: []cache.InformerSynced{
			rootSecretInformer.Informer().HasSynced,
			rootWorkspaceShardInformer.Informer().HasSynced,
		},
	}

	rootSecretInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueForSecret(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueForSecret(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueForSecret(obj) },
	})

	rootWorkspaceShardInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})
	if err := c.rootWorkspaceShardIndexer.AddIndexers(map[string]cache.IndexFunc{
		secretIndex: func(obj interface{}) ([]string, error) {
			if shard, ok := obj.(*tenancyv1alpha1.WorkspaceShard); ok {
				key, err := cache.MetaNamespaceKeyFunc(&metav1.ObjectMeta{
					ClusterName: shard.ObjectMeta.ClusterName,
					Namespace:   shard.Spec.Credentials.Namespace,
					Name:        shard.Spec.Credentials.Name,
				})
				if err != nil {
					return nil, err
				}
				return []string{key}, nil
			}
			return []string{}, nil
		},
	}); err != nil {
		return nil, fmt.Errorf("failed to add indexer for WorkspaceShards: %w", err)
	}

	return c, nil
}

// Controller watches WorkspaceShards and Secrets in order to make sure every WorkspaceShard
// has its URL exposed when a valid kubeconfig is connected to it.
type Controller struct {
	queue workqueue.RateLimitingInterface

	kcpClient         kcpclient.Interface
	rootSecretIndexer cache.Indexer
	rootSecretLister  corelister.SecretLister

	rootWorkspaceShardIndexer cache.Indexer
	rootWorkspaceShardLister  tenancylister.WorkspaceShardLister

	syncChecks []cache.InformerSynced
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	klog.Infof("queueing workspace shard %q", key)
	c.queue.Add(key)
}

func (c *Controller) enqueueForSecret(obj interface{}) {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			klog.V(2).Infof("Couldn't get object from tombstone %#v", obj)
			return
		}
		secret, ok = tombstone.Obj.(*corev1.Secret)
		if !ok {
			klog.V(2).Infof("Tombstone contained object that is not a Secret: %#v", obj)
			return
		}
	}
	klog.Infof("handling secret %q", secret.Name)
	key, err := cache.MetaNamespaceKeyFunc(secret)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	workspaceShards, err := c.rootWorkspaceShardIndexer.ByIndex(secretIndex, key)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	for _, shard := range workspaceShards {
		key, err := cache.MetaNamespaceKeyFunc(shard)
		if err != nil {
			runtime.HandleError(err)
			return
		}
		klog.Infof("queuing associated workspace %q", key)
		c.queue.Add(key)
	}
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.Info("Starting WorkspaceShard controller")
	defer klog.Info("Shutting down WorkspaceShard controller")

	if !cache.WaitForNamedCacheSync(controllerName, ctx.Done(), c.syncChecks...) {
		klog.Warning("Failed to wait for caches to sync")
		return
	}

	for i := 0; i < numThreads; i++ {
		go wait.Until(func() { c.startWorker(ctx) }, time.Second, ctx.Done())
	}

	<-ctx.Done()
}

func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k.(string)

	klog.Infof("processing key %q", key)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("invalid key: %q: %v", key, err)
		return nil
	}
	if namespace != "" {
		klog.Errorf("namespace %q found in key for cluster-wide WorkspaceShard object", namespace)
		return nil
	}

	obj, err := c.rootWorkspaceShardLister.Get(key) // TODO: clients need a way to scope down the lister per-cluster
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}
	previous := obj
	obj = obj.DeepCopy()

	if err := c.reconcile(ctx, obj); err != nil {
		return err
	}

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(previous.Status, obj.Status) {
		oldData, err := json.Marshal(tenancyv1alpha1.WorkspaceShard{
			Status: previous.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal old data for workspace shard %q|%q/%q: %w", helper.RootCluster, namespace, name, err)
		}

		newData, err := json.Marshal(tenancyv1alpha1.WorkspaceShard{
			ObjectMeta: metav1.ObjectMeta{
				UID:             previous.UID,
				ResourceVersion: previous.ResourceVersion,
			}, // to ensure they appear in the patch as preconditions
			Status: obj.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal new data for workspace shard %q|%q/%q: %w", helper.RootCluster, namespace, name, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return fmt.Errorf("failed to create patch for workspace shard %q|%q/%q: %w", helper.RootCluster, namespace, name, err)
		}
		_, uerr := c.kcpClient.TenancyV1alpha1().WorkspaceShards().Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
		return uerr
	}

	return nil
}

func (c *Controller) reconcile(ctx context.Context, workspaceShard *tenancyv1alpha1.WorkspaceShard) error {
	secret, err := c.rootSecretLister.Secrets(workspaceShard.Spec.Credentials.Namespace).Get(clusters.ToClusterAwareKey(workspaceShard.ClusterName, workspaceShard.Spec.Credentials.Name))
	if errors.IsNotFound(err) {
		conditions.MarkFalse(workspaceShard, tenancyv1alpha1.WorkspaceShardCredentialsValid, tenancyv1alpha1.WorkspaceShardCredentialsReasonMissing, conditionsapi.ConditionSeverityWarning, "Referenced secret %s/%s could not be found.", workspaceShard.Spec.Credentials.Namespace, workspaceShard.Spec.Credentials.Name)
		return nil
	} else if err != nil {
		return err
	}

	data, ok := secret.Data[tenancyv1alpha1.WorkspaceShardCredentialsKey]
	if !ok {
		conditions.MarkFalse(workspaceShard, tenancyv1alpha1.WorkspaceShardCredentialsValid, tenancyv1alpha1.WorkspaceShardCredentialsReasonInvalid, conditionsapi.ConditionSeverityError, "Referenced secret %s/%s did not contain key %s.", workspaceShard.Spec.Credentials.Namespace, workspaceShard.Spec.Credentials.Name, tenancyv1alpha1.WorkspaceShardCredentialsKey)
		return nil
	}

	cfg, err := clientcmd.RESTConfigFromKubeConfig(data)
	if err != nil {
		conditions.MarkFalse(workspaceShard, tenancyv1alpha1.WorkspaceShardCredentialsValid, tenancyv1alpha1.WorkspaceShardCredentialsReasonInvalid, conditionsapi.ConditionSeverityError, "Referenced credentials invalid: %v.", err.Error())
		return nil
	}

	hash := sha256.New()
	if _, err := hash.Write(data); err != nil {
		// TODO: our hash cannot ever return an error on Write, but the interface makes it possible, do we care to do something better?
		conditions.MarkFalse(workspaceShard, tenancyv1alpha1.WorkspaceShardCredentialsValid, tenancyv1alpha1.WorkspaceShardCredentialsReasonInvalid, conditionsapi.ConditionSeverityError, "Referenced credentials could not be hashed: %v.", err.Error())
		return nil
	}

	workspaceShard.Status.CredentialsHash = base64.StdEncoding.WithPadding(base64.NoPadding).EncodeToString(hash.Sum(nil))
	workspaceShard.Status.ConnectionInfo = &tenancyv1alpha1.ConnectionInfo{
		Host:    cfg.Host,
		APIPath: cfg.APIPath,
	}
	conditions.MarkTrue(workspaceShard, tenancyv1alpha1.WorkspaceShardCredentialsValid)
	return nil
}
