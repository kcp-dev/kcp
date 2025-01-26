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

package options

import (
	"context"
	"time"

	"github.com/jellydator/ttlcache/v3"
	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	"github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientset "k8s.io/client-go/kubernetes"
	kubecorev1lister "k8s.io/client-go/listers/core/v1"
	"k8s.io/kubernetes/pkg/serviceaccount"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
	corev1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/core/v1alpha1"
)

const (
	// SuccessCacheTTL is the TTL to cache a successful lookup for remote clusters.
	SuccessCacheTTL = 1 * time.Minute
	// FailureCacheTTL is the TTL to cache a failed lookup for remote clusters.
	FailureCacheTTL = 10 * time.Second
)

type cacheKey struct {
	clusterName logicalcluster.Name
	types.NamespacedName
}

// newServiceAccountTokenCache creates a new service account token cache backed
// by ttl caches for all remote clusters, and local informers logic for local
// clusters.
func newServiceAccountTokenCache(delayedKubeClusterClient *kubernetes.ClusterInterface, delayedKcpInformers *kcpinformers.SharedInformerFactory) func(kubeInformers kcpkubernetesinformers.SharedInformerFactory) serviceaccount.ServiceAccountTokenClusterGetter {
	return func(kubeInformers kcpkubernetesinformers.SharedInformerFactory) serviceaccount.ServiceAccountTokenClusterGetter {
		return &serviceAccountTokenCache{
			delayedKubeClusterClient: delayedKubeClusterClient,

			delayedKcpInformers: delayedKcpInformers,
			kubeInformers:       kubeInformers,

			serviceAccountCache: ttlcache.New[cacheKey, *corev1.ServiceAccount](),
			secretCache:         ttlcache.New[cacheKey, *corev1.Secret](),
		}
	}
}

type serviceAccountTokenCache struct {
	delayedKubeClusterClient *kubernetes.ClusterInterface

	delayedKcpInformers *kcpinformers.SharedInformerFactory
	kubeInformers       kcpkubernetesinformers.SharedInformerFactory

	serviceAccountCache *ttlcache.Cache[cacheKey, *corev1.ServiceAccount]
	secretCache         *ttlcache.Cache[cacheKey, *corev1.Secret]
}

func (c *serviceAccountTokenCache) Cluster(clusterName logicalcluster.Name) serviceaccount.ServiceAccountTokenGetter {
	return &serviceAccountTokenGetter{
		kubeClient: (*c.delayedKubeClusterClient).Cluster(clusterName.Path()),

		logicalClusters: (*c.delayedKcpInformers).Core().V1alpha1().LogicalClusters().Cluster(clusterName).Lister(),
		serviceAccounts: c.kubeInformers.Core().V1().ServiceAccounts().Cluster(clusterName).Lister(),
		secrets:         c.kubeInformers.Core().V1().Secrets().Cluster(clusterName).Lister(),

		serviceAccountCache: c.serviceAccountCache,
		secretCache:         c.secretCache,

		clusterName: clusterName,
	}
}

type serviceAccountTokenGetter struct {
	kubeClient clientset.Interface

	logicalClusters corev1alpha1listers.LogicalClusterLister
	serviceAccounts kubecorev1lister.ServiceAccountLister
	secrets         kubecorev1lister.SecretLister

	serviceAccountCache *ttlcache.Cache[cacheKey, *corev1.ServiceAccount]
	secretCache         *ttlcache.Cache[cacheKey, *corev1.Secret]

	clusterName logicalcluster.Name
}

func (g *serviceAccountTokenGetter) GetServiceAccount(namespace, name string) (*corev1.ServiceAccount, error) {
	// local cluster?
	if _, err := g.logicalClusters.Get(corev1alpha1.LogicalClusterName); err != nil {
		return g.serviceAccounts.ServiceAccounts(namespace).Get(name)
	}

	// cached?
	if sa := g.serviceAccountCache.Get(cacheKey{g.clusterName, types.NamespacedName{Namespace: namespace, Name: name}}); sa != nil && sa.Value() != nil {
		return sa.Value(), nil
	}

	// fetch with external client
	// TODO(sttts): here it's little racy, as we might fetch the service account multiple times.
	sa, err := g.kubeClient.CoreV1().ServiceAccounts(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil && !kerrors.IsNotFound(err) {
		return nil, err
	} else if kerrors.IsNotFound(err) {
		ttl := ttlcache.WithTTL[cacheKey, *corev1.ServiceAccount](FailureCacheTTL)
		g.serviceAccountCache.GetOrSet(cacheKey{g.clusterName, types.NamespacedName{Namespace: namespace, Name: name}}, nil, ttl)
	}

	g.serviceAccountCache.Set(cacheKey{g.clusterName, types.NamespacedName{Namespace: namespace, Name: name}}, sa, SuccessCacheTTL)
	return sa, nil
}

func (g *serviceAccountTokenGetter) GetPod(_, name string) (*corev1.Pod, error) {
	return nil, kerrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, name)
}

func (g *serviceAccountTokenGetter) GetSecret(namespace, name string) (*corev1.Secret, error) {
	// local cluster?
	if _, err := g.logicalClusters.Get(corev1alpha1.LogicalClusterName); err != nil {
		return g.secrets.Secrets(namespace).Get(name)
	}

	// cached?
	if secret := g.secretCache.Get(cacheKey{g.clusterName, types.NamespacedName{Namespace: namespace, Name: name}}); secret != nil && secret.Value() != nil {
		return secret.Value(), nil
	}

	// fetch with external client
	// TODO(sttts): here it's little racy, as we might fetch the secret multiple times.
	secret, err := g.kubeClient.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil && !kerrors.IsNotFound(err) {
		return nil, err
	} else if kerrors.IsNotFound(err) {
		ttl := ttlcache.WithTTL[cacheKey, *corev1.Secret](FailureCacheTTL)
		g.secretCache.GetOrSet(cacheKey{g.clusterName, types.NamespacedName{Namespace: namespace, Name: name}}, nil, ttl)
	}

	g.secretCache.Set(cacheKey{g.clusterName, types.NamespacedName{Namespace: namespace, Name: name}}, secret, SuccessCacheTTL)
	return secret, nil
}

func (g *serviceAccountTokenGetter) GetNode(name string) (*corev1.Node, error) {
	return nil, kerrors.NewNotFound(schema.GroupResource{Group: "", Resource: "nodes"}, name)
}
