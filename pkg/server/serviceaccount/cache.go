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

package serviceaccount

import (
	"context"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientset "k8s.io/client-go/kubernetes"
	kubecorev1lister "k8s.io/client-go/listers/core/v1"
	"k8s.io/kubernetes/pkg/serviceaccount"

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	"github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	corev1alpha1listers "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"
)

// Since the SATokenGetter does not get a context upstream but we need
// to perform network requests they are bounded by this request timeout.
// Not great, not terrible.
const requestTimeout = 10 * time.Minute

// Cache implements serviceaccount.ServiceAccountTokenClusterGetter.
type Cache struct {
	lock sync.Mutex
	// kubeShardClient is the client used to pull service accounts and secrets from other shards.
	kubeShardClient kubernetes.ClusterInterface
	// kcpInformers is used to lookup if the logical cluster a service account originates from the local shard.
	kcpInformers kcpinformers.SharedInformerFactory
	// kubeInformers is used to lookup service accounts and secrets from shard-local logical clusters.
	kubeInformers kcpkubernetesinformers.SharedInformerFactory

	serviceAccounts coalescingCache[*corev1.ServiceAccount]
	secrets         coalescingCache[*corev1.Secret]
}

// NewCache returns an initialized Cache.
func NewCache() *Cache {
	return &Cache{
		serviceAccounts: newCoalescingCache[*corev1.ServiceAccount](),
		secrets:         newCoalescingCache[*corev1.Secret](),
	}
}

// SetKubeShardClient sets the client used to communicate with other shards.
func (c *Cache) SetKubeShardClient(kubeShardClient kubernetes.ClusterInterface) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.kubeShardClient = kubeShardClient
}

// SetInformers sets the kcp informer factory to check if a logical cluster is on the local shard.
func (c *Cache) SetInformers(kcpInformers kcpinformers.SharedInformerFactory) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.kcpInformers = kcpInformers
}

// TokenGetter implements the OptionalTokenGetter for the kubeapiserver.
// It sets the kube informer factory to retrieve service accounts and secrets.
func (c *Cache) TokenGetter(kubeInformers kcpkubernetesinformers.SharedInformerFactory) serviceaccount.ServiceAccountTokenClusterGetter {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.kubeInformers = kubeInformers
	return c
}

// Cluster returns a ServiceAccountTokenGetter scoped to the given cluster.
func (c *Cache) Cluster(clusterName logicalcluster.Name) serviceaccount.ServiceAccountTokenGetter {
	c.lock.Lock()
	defer c.lock.Unlock()

	// These should not happen but in case they do it's preferable to panic with a clear error message.
	if c.kubeShardClient == nil {
		panic("serviceaccount cache kubeShardClient is nil")
	}
	if c.kcpInformers == nil {
		panic("serviceaccount cache kcpInformers is nil")
	}
	if c.kubeInformers == nil {
		panic("serviceaccount cache kubeInformers is nil")
	}
	// If any of these happen verify that the pointers and setup order are still valid.
	//
	// The current implementation works by creating a "ref holder" (this
	// Cache) in the server options, which is then used to pass the
	// OptionalTokenGetter (a closure) to the upstream SA auth stack via
	// controlplaneapiserver.BuildGenericConfig and to backfill the
	// clients and informers later in the setup when they are available.
	//
	// The upstream auth stack calls the OptionalTokenGetter before the
	// kubeShardClient and kcpInformers are set with the kubeInformers. It
	// does this once during the setup (the .BuildGenericConfig).
	//
	// Because this happens before kcp has the client and informers the
	// ref holder is required.
	//
	// Once the setup finishes and the shard starts operating the Cache
	// has both informer factories and the client.

	return &serviceAccountTokenGetter{
		logicalClusterLister: c.kcpInformers.Core().V1alpha1().LogicalClusters().Cluster(clusterName).Lister(),
		clusterName:          clusterName,

		serviceAccountsLister: c.kubeInformers.Core().V1().ServiceAccounts().Cluster(clusterName).Lister(),
		secretsLister:         c.kubeInformers.Core().V1().Secrets().Cluster(clusterName).Lister(),

		kubeShardClient: c.kubeShardClient.Cluster(clusterName.Path()),
		serviceAccounts: c.serviceAccounts,
		secrets:         c.secrets,
	}
}

var _ serviceaccount.ServiceAccountTokenGetter = (*serviceAccountTokenGetter)(nil)

// serviceAccountTokenGetter implements serviceaccount.ServiceAccountTokenGetter.
type serviceAccountTokenGetter struct {
	// lookup if a logical cluster is shard-local
	logicalClusterLister corev1alpha1listers.LogicalClusterLister
	clusterName          logicalcluster.Name

	// lookup in shard-local
	serviceAccountsLister kubecorev1lister.ServiceAccountLister
	secretsLister         kubecorev1lister.SecretLister

	// lookup in remote shards
	kubeShardClient clientset.Interface
	serviceAccounts coalescingCache[*corev1.ServiceAccount]
	secrets         coalescingCache[*corev1.Secret]
}

func (s *serviceAccountTokenGetter) GetPod(_, name string) (*corev1.Pod, error) {
	return nil, kerrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, name)
}

func (s *serviceAccountTokenGetter) GetNode(name string) (*corev1.Node, error) {
	return nil, kerrors.NewNotFound(schema.GroupResource{Group: "", Resource: "nodes"}, name)
}

func (s *serviceAccountTokenGetter) GetServiceAccount(namespace, name string) (*corev1.ServiceAccount, error) {
	// This is a trade off. Logical clusters are treated as remote and
	// requests round-trip through the front-proxy; but given that we
	// are in kube-land with eventual consistency this is fine. The
	// occurrences of this should be relatively low.
	if _, err := s.logicalClusterLister.Get(corev1alpha1.LogicalClusterName); err == nil {
		return s.serviceAccountsLister.ServiceAccounts(namespace).Get(name)
	}
	return s.serviceAccounts.get(s.clusterName, namespace, name, func() (*corev1.ServiceAccount, error) {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		return s.kubeShardClient.CoreV1().ServiceAccounts(namespace).Get(ctx, name, metav1.GetOptions{})
	})
}

func (s *serviceAccountTokenGetter) GetSecret(namespace, name string) (*corev1.Secret, error) {
	if _, err := s.logicalClusterLister.Get(corev1alpha1.LogicalClusterName); err == nil {
		return s.secretsLister.Secrets(namespace).Get(name)
	}
	return s.secrets.get(s.clusterName, namespace, name, func() (*corev1.Secret, error) {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		return s.kubeShardClient.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	})
}
