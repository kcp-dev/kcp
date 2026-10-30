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

package proxy

import (
	"context"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/kubernetes/pkg/serviceaccount"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	kcpcorev1client "github.com/kcp-dev/client-go/kubernetes/typed/core/v1"
	"github.com/kcp-dev/logicalcluster/v3"

	kcpindex "github.com/kcp-dev/kcp/pkg/index"
)

// ShardRoutedServiceAccounts looks ServiceAccounts and their token Secrets up
// on the shard that hosts the logical cluster a service account token names,
// found through the proxy's index, using the shards credential. It is what
// lets the proxy validate service account tokens of workspaces on any shard
// without a connection to the root shard.
//
// It serves two roles for the authenticator: the token getter, through
// Cluster, and the writer the legacy token validator stamps last-used
// annotations with, through SecretsWriter.
type ShardRoutedServiceAccounts struct {
	shardsConfig *rest.Config

	mu      sync.Mutex
	index   shardIndex                                         // nil until the index controller exists
	clients map[string]kcpkubernetesclientset.ClusterInterface // by shard base URL
}

// NewShardRoutedServiceAccounts creates the lookup over the shards credential.
// The index is attached with SetIndex once the index controller exists;
// lookups before that fail with ServiceUnavailable.
func NewShardRoutedServiceAccounts(shardsConfig *rest.Config) *ShardRoutedServiceAccounts {
	return &ShardRoutedServiceAccounts{
		shardsConfig: shardsConfig,
		clients:      map[string]kcpkubernetesclientset.ClusterInterface{},
	}
}

// shardIndex is the part of the proxy's index the lookups need: which shard
// hosts a logical cluster, and where that shard is.
type shardIndex interface {
	Lookup(path logicalcluster.Path) (kcpindex.Result, bool)
	BaseURL(shardName string) (string, bool)
}

// SetIndex attaches the index the lookups resolve shards through.
func (s *ShardRoutedServiceAccounts) SetIndex(index shardIndex) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.index = index
}

// clientFor returns a client for the shard hosting the logical cluster.
func (s *ShardRoutedServiceAccounts) clientFor(cluster logicalcluster.Name) (kcpkubernetesclientset.ClusterInterface, error) {
	s.mu.Lock()
	index := s.index
	s.mu.Unlock()
	if index == nil {
		return nil, apierrors.NewServiceUnavailable("the shard index is not started yet")
	}

	result, found := index.Lookup(cluster.Path())
	if !found || result.ErrorCode != 0 || result.Shard == "" {
		return nil, apierrors.NewNotFound(corev1.Resource("logicalclusters"), cluster.String())
	}
	baseURL, ok := index.BaseURL(result.Shard)
	if !ok {
		return nil, apierrors.NewNotFound(corev1.Resource("shards"), result.Shard)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if client, ok := s.clients[baseURL]; ok {
		return client, nil
	}
	config := rest.CopyConfig(s.shardsConfig)
	config.Host = baseURL
	client, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	s.clients[baseURL] = client
	return client, nil
}

// Cluster implements serviceaccount.ServiceAccountTokenClusterGetter.
func (s *ShardRoutedServiceAccounts) Cluster(cluster logicalcluster.Name) serviceaccount.ServiceAccountTokenGetter {
	return clusterServiceAccounts{lookup: s, cluster: cluster}
}

// SecretsWriter returns the secret client the legacy token validator writes
// through, routed the same way as the lookups.
func (s *ShardRoutedServiceAccounts) SecretsWriter() kcpcorev1client.SecretClusterInterface {
	return routedSecrets{lookup: s}
}

type clusterServiceAccounts struct {
	lookup  *ShardRoutedServiceAccounts
	cluster logicalcluster.Name
}

func (c clusterServiceAccounts) GetServiceAccount(ctx context.Context, namespace, name string) (*corev1.ServiceAccount, error) {
	client, err := c.lookup.clientFor(c.cluster)
	if err != nil {
		return nil, err
	}
	return client.Cluster(c.cluster.Path()).CoreV1().ServiceAccounts(namespace).Get(ctx, name, metav1.GetOptions{})
}

func (c clusterServiceAccounts) GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	client, err := c.lookup.clientFor(c.cluster)
	if err != nil {
		return nil, err
	}
	return client.Cluster(c.cluster.Path()).CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
}

// GetPod and GetNode back bound tokens of pods and nodes, which do not exist
// in kcp.
func (c clusterServiceAccounts) GetPod(_ context.Context, namespace, name string) (*corev1.Pod, error) {
	return nil, apierrors.NewNotFound(corev1.Resource("pods"), namespace+"/"+name)
}

func (c clusterServiceAccounts) GetNode(_ context.Context, name string) (*corev1.Node, error) {
	return nil, apierrors.NewNotFound(corev1.Resource("nodes"), name)
}

// routedSecrets is a SecretClusterInterface that routes per logical cluster;
// listing or watching across clusters is not something the proxy does.
type routedSecrets struct {
	lookup *ShardRoutedServiceAccounts
}

func (r routedSecrets) Cluster(path logicalcluster.Path) kcpcorev1client.SecretsNamespacer {
	cluster, ok := path.Name()
	if !ok {
		return erroringSecretsNamespacer{err: apierrors.NewBadRequest("secrets are addressed by logical cluster name, not path " + path.String())}
	}
	client, err := r.lookup.clientFor(cluster)
	if err != nil {
		return erroringSecretsNamespacer{err: err}
	}
	return clusterSecretsNamespacer{secrets: client.Cluster(cluster.Path()).CoreV1()}
}

func (routedSecrets) List(context.Context, metav1.ListOptions) (*corev1.SecretList, error) {
	return nil, apierrors.NewMethodNotSupported(corev1.Resource("secrets"), "list")
}

func (routedSecrets) Watch(context.Context, metav1.ListOptions) (watch.Interface, error) {
	return nil, apierrors.NewMethodNotSupported(corev1.Resource("secrets"), "watch")
}

type clusterSecretsNamespacer struct {
	secrets typedcorev1.SecretsGetter
}

func (n clusterSecretsNamespacer) Namespace(namespace string) typedcorev1.SecretInterface {
	return n.secrets.Secrets(namespace)
}

// erroringSecretsNamespacer answers every call with the lookup error.
type erroringSecretsNamespacer struct {
	err error
}

func (n erroringSecretsNamespacer) Namespace(string) typedcorev1.SecretInterface {
	return erroringSecrets(n)
}

type erroringSecrets struct {
	err error
}

var _ typedcorev1.SecretInterface = erroringSecrets{}

func (e erroringSecrets) Create(context.Context, *corev1.Secret, metav1.CreateOptions) (*corev1.Secret, error) {
	return nil, e.err
}

func (e erroringSecrets) Update(context.Context, *corev1.Secret, metav1.UpdateOptions) (*corev1.Secret, error) {
	return nil, e.err
}

func (e erroringSecrets) Delete(context.Context, string, metav1.DeleteOptions) error {
	return e.err
}

func (e erroringSecrets) DeleteCollection(context.Context, metav1.DeleteOptions, metav1.ListOptions) error {
	return e.err
}

func (e erroringSecrets) Get(context.Context, string, metav1.GetOptions) (*corev1.Secret, error) {
	return nil, e.err
}

func (e erroringSecrets) List(context.Context, metav1.ListOptions) (*corev1.SecretList, error) {
	return nil, e.err
}

func (e erroringSecrets) Watch(context.Context, metav1.ListOptions) (watch.Interface, error) {
	return nil, e.err
}

func (e erroringSecrets) Patch(context.Context, string, types.PatchType, []byte, metav1.PatchOptions, ...string) (*corev1.Secret, error) {
	return nil, e.err
}

func (e erroringSecrets) Apply(context.Context, *corev1ac.SecretApplyConfiguration, metav1.ApplyOptions) (*corev1.Secret, error) {
	return nil, e.err
}
