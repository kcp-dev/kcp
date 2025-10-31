/*
Copyright 2025 The KCP Authors.

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

package cachedresources

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/pkg/crypto"
	"github.com/kcp-dev/kcp/pkg/logging"
	replicationcontroller "github.com/kcp-dev/kcp/pkg/reconciler/cache/cachedresources/replication"
)

type reconcileStatus int

const (
	reconcileStatusStopAndRequeue reconcileStatus = iota
	reconcileStatusContinue
	reconcileStatusStop
)

type reconciler interface {
	reconcile(ctx context.Context, workspace *cachev1alpha1.CachedResource) (reconcileStatus, error)
}

// reconcile reconciles the workspace objects. It is intended to be single reconciler for all the
// workspace replated operations. For now it has single reconciler that updates the status of the
// workspace based on the mount status.
func (c *Controller) reconcile(ctx context.Context, cluster logicalcluster.Name, cachedResource *cachev1alpha1.CachedResource) (bool, error) {
	reconcilers := []reconciler{
		&finalizer{},
		&identity{
			ensureSecretNamespaceExists: c.ensureSecretNamespaceExists,
			getSecret:                   c.getSecret,
			createIdentitySecret:        c.createIdentitySecret,
			secretNamespace:             c.secretNamespace,
		},
		&endpointSlice{
			getEndpointSlice:    c.getEndpointSlice,
			createEndpointSlice: c.createEndpointSlice,
		},
		&purge{
			deleteSelectedCacheResources: func(ctx context.Context, cachedResource *cachev1alpha1.CachedResource) error {
				return c.deleteSelectedCacheResources(ctx, cluster, cachedResource)
			},
		},
		&counter{
			listSelectedLocalResources: func(ctx context.Context, cachedResource *cachev1alpha1.CachedResource) (*unstructured.UnstructuredList, error) {
				return c.listSelectedLocalResources(ctx, cluster, cachedResource)
			},
			listSelectedCachedResources: func(ctx context.Context, cachedResource *cachev1alpha1.CachedResource) (*cachev1alpha1.CachedObjectList, error) {
				return c.listSelectedCacheResources(ctx, cluster, cachedResource)
			},
		},
		&replication{
			shardName:                      c.shardName,
			dynamicClusterClient:           c.dynamicClient,
			kcpCacheClient:                 c.kcpCacheClient,
			dynRESTMapper:                  c.dynRESTMapper,
			cacheKcpInformers:              c.cacheKcpInformers,
			discoveringDynamicKcpInformers: c.discoveringDynamicKcpInformers,
			callback:                       c.enqueue,
			controllerRegistry:             c.controllerRegistry,
		},
	}

	var errs []error

	requeue := false
	for _, r := range reconcilers {
		var err error
		var status reconcileStatus
		status, err = r.reconcile(ctx, cachedResource)
		if err != nil {
			errs = append(errs, err)
		}
		if status == reconcileStatusStopAndRequeue {
			requeue = true
			break
		}
		if status == reconcileStatusStop {
			break
		}
	}

	return requeue, utilerrors.NewAggregate(errs)
}

func (c *Controller) listSelectedLocalResources(ctx context.Context, cluster logicalcluster.Name, cachedResource *cachev1alpha1.CachedResource) (*unstructured.UnstructuredList, error) {
	gvr := schema.GroupVersionResource{
		Group:    cachedResource.Spec.Group,
		Version:  cachedResource.Spec.Version,
		Resource: cachedResource.Spec.Resource,
	}

	listOpts := metav1.ListOptions{}
	if cachedResource.Spec.LabelSelector != nil {
		listOpts.LabelSelector = labels.SelectorFromSet(cachedResource.Spec.LabelSelector.MatchLabels).String()
	}

	resources, err := c.dynamicClient.Cluster(cluster.Path()).Resource(gvr).List(ctx, listOpts)
	if err != nil {
		return nil, err
	}

	return resources, nil
}

func (c *Controller) deleteSelectedCacheResources(ctx context.Context, cluster logicalcluster.Name, cachedResource *cachev1alpha1.CachedResource) error {
	gvr := schema.GroupVersionResource{
		Group:    cachedResource.Spec.Group,
		Version:  cachedResource.Spec.Version,
		Resource: cachedResource.Spec.Resource,
	}
	if gvr.Group == "" {
		gvr.Group = "core"
	}

	selector := labels.SelectorFromSet(labels.Set{
		replicationcontroller.LabelKeyObjectSchema: gvr.Version + "." + gvr.Resource + "." + gvr.Group,
	})
	if cachedResource.Spec.LabelSelector != nil && len(cachedResource.Spec.LabelSelector.MatchLabels) > 0 {
		l := labels.SelectorFromSet(cachedResource.Spec.LabelSelector.MatchLabels)
		r, _ := selector.Requirements()
		selector = l.Add(r...)
	}

	ctx = cacheclient.WithShardInContext(ctx, shard.New(c.shardName))
	return c.kcpCacheClient.Cluster(cluster.Path()).CacheV1alpha1().CachedObjects().DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
}

func (c *Controller) listSelectedCacheResources(ctx context.Context, cluster logicalcluster.Name, cachedResource *cachev1alpha1.CachedResource) (*cachev1alpha1.CachedObjectList, error) {
	gvr := schema.GroupVersionResource{
		Group:    cachedResource.Spec.Group,
		Version:  cachedResource.Spec.Version,
		Resource: cachedResource.Spec.Resource,
	}
	if gvr.Group == "" {
		gvr.Group = "core"
	}

	selector := labels.SelectorFromSet(labels.Set{
		replicationcontroller.LabelKeyObjectSchema: gvr.Version + "." + gvr.Resource + "." + gvr.Group,
	})
	if cachedResource.Spec.LabelSelector != nil && len(cachedResource.Spec.LabelSelector.MatchLabels) > 0 {
		l := labels.SelectorFromSet(cachedResource.Spec.LabelSelector.MatchLabels)
		r, _ := selector.Requirements()
		selector = l.Add(r...)
	}
	ctx = cacheclient.WithShardInContext(ctx, shard.New(c.shardName))
	resources, err := c.kcpCacheClient.Cluster(cluster.Path()).CacheV1alpha1().CachedObjects().List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return nil, err
	}

	return resources, nil
}

func (c *Controller) ensureSecretNamespaceExists(ctx context.Context, clusterName logicalcluster.Name, defaultSecretNamespace string) {
	logger := klog.FromContext(ctx)
	ctx = klog.NewContext(ctx, logger)
	if _, err := c.getNamespace(clusterName, defaultSecretNamespace); errors.IsNotFound(err) {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:        defaultSecretNamespace,
				Annotations: map[string]string{logicalcluster.AnnotationKey: clusterName.String()},
			},
		}
		logger = logging.WithObject(logger, ns)
		if err := c.createNamespace(ctx, clusterName.Path(), ns); err != nil && !errors.IsAlreadyExists(err) {
			logger.Error(err, "error creating namespace for CachedResource secret identities")
			// Keep going - maybe things will work. If the secret creation fails, we'll make sure to set a condition.
		}
	}
}

func (c *Controller) createIdentitySecret(ctx context.Context, clusterName logicalcluster.Path, defaultSecretNamespace, cachedResourceName string) error {
	secret, err := GenerateIdentitySecret(ctx, defaultSecretNamespace, cachedResourceName)
	if err != nil {
		return err
	}
	secret.Annotations[logicalcluster.AnnotationKey] = clusterName.String()

	logger := logging.WithObject(klog.FromContext(ctx), secret)
	ctx = klog.NewContext(ctx, logger)
	logger.V(2).Info("creating identity secret")
	return c.createSecret(ctx, clusterName, secret)
}

// TODO: This is copy from apiexport controller. We should move it to a shared location.
func GenerateIdentitySecret(ctx context.Context, ns string, name string) (*corev1.Secret, error) {
	logger := klog.FromContext(ctx)
	start := time.Now()
	key := crypto.Random256BitsString()
	if dur := time.Since(start); dur > time.Millisecond*100 {
		logger.Info("identity key generation took a long time", "duration", dur)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   ns,
			Name:        name,
			Annotations: map[string]string{},
		},
		StringData: map[string]string{
			apisv1alpha1.SecretKeyAPIExportIdentity: key,
		},
	}

	return secret, nil
}
