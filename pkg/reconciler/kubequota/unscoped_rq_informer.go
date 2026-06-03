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

package kubequota

import (
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	corev1schema "k8s.io/apimachinery/pkg/runtime/schema"
	coreinformers "k8s.io/client-go/informers/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kubernetes/pkg/controller/resourcequota"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpcorev1informers "github.com/kcp-dev/client-go/informers/core/v1"
	"github.com/kcp-dev/logicalcluster/v3"
)

var _ coreinformers.ResourceQuotaInformer = (*unscopedRQInformer)(nil)

// unscopedRQInformer is a shim around the cluster-aware resource quota informer.
// It is used to give the upstream quota reconciler cluster-aware
// a cluster-aware informer without requiring upstream code changes.
//
// The cluster awareness is smuggled in the namespace in the form of `<cluster>|<namespace>`.
type unscopedRQInformer struct {
	clusterInformer kcpcorev1informers.ResourceQuotaClusterInformer
}

func newUnscopedRQInformer(ci kcpcorev1informers.ResourceQuotaClusterInformer) coreinformers.ResourceQuotaInformer {
	return &unscopedRQInformer{clusterInformer: ci}
}

func (u *unscopedRQInformer) Informer() cache.SharedIndexInformer {
	return &transformingInformer{SharedIndexInformer: u.clusterInformer.Informer()}
}

func (u *unscopedRQInformer) Lister() corelisters.ResourceQuotaLister {
	return &encodedRQLister{indexer: u.clusterInformer.Informer().GetIndexer()}
}

// encodedRQLister adapts a kcp cluster-aware indexer of ResourceQuotas
// to upstream's namespace-keyed ResourceQuotaLister, decoding
// <cluster>|<namespace> on each call.
type encodedRQLister struct {
	indexer cache.Indexer
}

func (l *encodedRQLister) List(selector labels.Selector) ([]*corev1.ResourceQuota, error) {
	var ret []*corev1.ResourceQuota
	err := cache.ListAll(l.indexer, selector, func(m interface{}) {
		if rq, ok := m.(*corev1.ResourceQuota); ok {
			ret = append(ret, encodeRQ(rq))
		}
	})
	return ret, err
}

func (l *encodedRQLister) ResourceQuotas(encoded string) corelisters.ResourceQuotaNamespaceLister {
	cluster, namespace := resourcequota.ParseEncodedNamespace(encoded)
	return &encodedRQNamespaceLister{
		indexer:   l.indexer,
		cluster:   cluster,
		namespace: namespace,
	}
}

type encodedRQNamespaceLister struct {
	indexer   cache.Indexer
	cluster   logicalcluster.Name
	namespace string
}

func (l *encodedRQNamespaceLister) List(selector labels.Selector) ([]*corev1.ResourceQuota, error) {
	var ret []*corev1.ResourceQuota
	err := kcpcache.ListAllByClusterAndNamespace(l.indexer, l.cluster, l.namespace, selector, func(m interface{}) {
		if rq, ok := m.(*corev1.ResourceQuota); ok {
			ret = append(ret, encodeRQ(rq))
		}
	})
	return ret, err
}

func (l *encodedRQNamespaceLister) Get(name string) (*corev1.ResourceQuota, error) {
	key := kcpcache.ToClusterAwareKey(l.cluster.String(), l.namespace, name)
	obj, exists, err := l.indexer.GetByKey(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, apierrors.NewNotFound(corev1schema.GroupResource{Resource: "resourcequotas"}, name)
	}
	return encodeRQ(obj.(*corev1.ResourceQuota)), nil
}

// encodeRQ returns a deep copy of rq whose metadata.namespace has been
// rewritten to <cluster>|<namespace>. Required because the upstream
// resourcequota.Controller takes the lister-returned rq.Namespace and
// passes it straight to ResourceQuotas(...) for status writes; the
// shim needs the encoded form to recover the cluster.
func encodeRQ(rq *corev1.ResourceQuota) *corev1.ResourceQuota {
	cluster := logicalcluster.From(rq)
	if cluster.Empty() || resourcequota.IsEncodedNamespace(rq.Namespace) {
		return rq
	}
	cp := rq.DeepCopy()
	cp.Namespace = resourcequota.EncodeNamespace(cluster, rq.Namespace)
	return cp
}
