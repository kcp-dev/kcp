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

package apiexport

import (
	"context"
	"fmt"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apisinformers "github.com/kcp-dev/kcp/pkg/client/informers/apis/v1alpha1"
	tenancyinformers "github.com/kcp-dev/kcp/pkg/client/informers/tenancy/v1alpha1"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
)

const (
	controllerName = "kcp-apiexport"

	DefaultIdentitySecretNamespace = "kcp-system"
)

// NewController returns a new controller for APIExports.
func NewController(
	kcpClusterClient kcpclient.Interface,
	apiExportInformer apisinformers.APIExportInformer,
	clusterWorkspaceShardInformer tenancyinformers.ClusterWorkspaceShardInformer,
	kubeClusterClient kubernetes.Interface,
	namespaceInformer coreinformers.NamespaceInformer,
	secretInformer coreinformers.SecretInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		queue:             queue,
		kcpClusterClient:  kcpClusterClient,
		apiExportLister:   apiExportInformer.Lister(),
		apiExportIndexer:  apiExportInformer.Informer().GetIndexer(),
		kubeClusterClient: kubeClusterClient,
		getNamespace: func(clusterName logicalcluster.Name, name string) (*corev1.Namespace, error) {
			return namespaceInformer.Lister().Get(clusters.ToClusterAwareKey(clusterName, name))
		},
		createNamespace: func(ctx context.Context, clusterName logicalcluster.Name, ns *corev1.Namespace) error {
			_, err := kubeClusterClient.CoreV1().Namespaces().Create(logicalcluster.WithCluster(ctx, clusterName), ns, metav1.CreateOptions{})
			return err
		},
		secretLister:    secretInformer.Lister(),
		secretNamespace: DefaultIdentitySecretNamespace,
		createSecret: func(ctx context.Context, clusterName logicalcluster.Name, secret *corev1.Secret) error {
			_, err := kubeClusterClient.CoreV1().Secrets(secret.Namespace).Create(logicalcluster.WithCluster(ctx, clusterName), secret, metav1.CreateOptions{})
			return err
		},
		listClusterWorkspaceShards: func() ([]*tenancyv1alpha1.ClusterWorkspaceShard, error) {
			return clusterWorkspaceShardInformer.Lister().List(labels.Everything())
		},
		commit: committer.NewCommitter[*APIExport, *APIExportSpec, *APIExportStatus](kcpClusterClient.ApisV1alpha1().APIExports()),
	}

	c.getSecret = c.readThroughGetSecret

	indexers.AddIfNotPresentOrDie(
		apiExportInformer.Informer().GetIndexer(),
		cache.Indexers{
			indexers.APIExportByIdentity: indexers.IndexAPIExportByIdentity,
			indexers.APIExportBySecret:   indexers.IndexAPIExportBySecret,
		},
	)

	apiExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIExport(obj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueAPIExport(newObj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIExport(obj)
		},
	})

	secretInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueSecret(obj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueSecret(newObj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueSecret(obj)
		},
	})

	clusterWorkspaceShardInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				c.enqueueAllAPIExports(obj)
			},
			UpdateFunc: func(_, newObj interface{}) {
				c.enqueueAllAPIExports(newObj)
			},
			DeleteFunc: func(obj interface{}) {
				c.enqueueAllAPIExports(obj)
			},
		},
	)

	return c, nil
}

type APIExport = apisv1alpha1.APIExport
type APIExportSpec = apisv1alpha1.APIExportSpec
type APIExportStatus = apisv1alpha1.APIExportStatus
type Resource = committer.Resource[*APIExportSpec, *APIExportStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

// controller reconciles APIExports. It ensures an export's identity secret exists and is valid.
type controller struct {
	queue workqueue.RateLimitingInterface

	kcpClusterClient kcpclient.Interface
	apiExportLister  apislisters.APIExportLister
	apiExportIndexer cache.Indexer

	kubeClusterClient kubernetes.Interface

	getNamespace    func(clusterName logicalcluster.Name, name string) (*corev1.Namespace, error)
	createNamespace func(ctx context.Context, clusterName logicalcluster.Name, ns *corev1.Namespace) error

	secretLister    corelisters.SecretLister
	secretNamespace string

	getSecret    func(ctx context.Context, clusterName logicalcluster.Name, ns, name string) (*corev1.Secret, error)
	createSecret func(ctx context.Context, clusterName logicalcluster.Name, secret *corev1.Secret) error

	listClusterWorkspaceShards func() ([]*tenancyv1alpha1.ClusterWorkspaceShard, error)
	commit                     CommitFunc
}

// enqueueAPIBinding enqueues an APIExport .
func (c *controller) enqueueAPIExport(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), controllerName), key)
	logger.V(4).Info("queueing APIExport")
	c.queue.Add(key)
}

func (c *controller) enqueueAllAPIExports(clusterWorkspaceShard interface{}) {
	list, err := c.apiExportLister.List(labels.Everything())
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithObject(logging.WithReconciler(klog.Background(), controllerName), clusterWorkspaceShard.(*tenancyv1alpha1.ClusterWorkspaceShard))
	for i := range list {
		key, err := cache.MetaNamespaceKeyFunc(list[i])
		if err != nil {
			runtime.HandleError(err)
			continue
		}

		logging.WithQueueKey(logger, key).V(2).Info("queuing APIExport because ClusterWorkspaceShard changed")
		c.queue.Add(key)
	}
}

func (c *controller) enqueueSecret(obj interface{}) {
	secretKey, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	apiExportKeys, err := c.apiExportIndexer.IndexKeys(indexers.APIExportBySecret, secretKey)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithObject(logging.WithReconciler(klog.Background(), controllerName), obj.(*corev1.Secret))
	for _, key := range apiExportKeys {
		logging.WithQueueKey(logger, key).V(2).Info("queueing APIExport via identity Secret")
		c.queue.Add(key)
	}
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), controllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
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
	key := k.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

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

func (c *controller) process(ctx context.Context, key string) error {
	obj, err := c.apiExportLister.Get(key)
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

func (c *controller) readThroughGetSecret(ctx context.Context, clusterName logicalcluster.Name, ns, name string) (*corev1.Secret, error) {
	secret, err := c.secretLister.Secrets(ns).Get(clusters.ToClusterAwareKey(clusterName, name))
	if err == nil {
		return secret, nil
	}

	// In case the lister is slow to catch up, try a live read
	secret, err = c.kubeClusterClient.CoreV1().Secrets(ns).Get(logicalcluster.WithCluster(ctx, clusterName), name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	return secret, nil
}
