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
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	applyconfigurationscorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/kubernetes/pkg/controller/resourcequota"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
)

var _ corev1client.ResourceQuotasGetter = (*ctxResourceQuotasClient)(nil)

// ctxResourceQuotasClient is a shim around the cluster-aware kube client.
// The cluster is encoded in the namespace in the unscoped informer
// factory and RQ informer and this client unpacks these when the
// upstream kubequota does requests.
type ctxResourceQuotasClient struct {
	client kcpkubernetesclientset.ClusterInterface
}

func newCtxResourceQuotasClient(client kcpkubernetesclientset.ClusterInterface) corev1client.ResourceQuotasGetter {
	return &ctxResourceQuotasClient{client: client}
}

func (c *ctxResourceQuotasClient) ResourceQuotas(encodedNamespace string) corev1client.ResourceQuotaInterface {
	cluster, namespace := resourcequota.ParseEncodedNamespace(encodedNamespace)
	return &ctxResourceQuotaInterface{
		client:    c.client,
		cluster:   cluster,
		namespace: namespace,
	}
}

type ctxResourceQuotaInterface struct {
	client    kcpkubernetesclientset.ClusterInterface
	cluster   logicalcluster.Name
	namespace string
}

func (c *ctxResourceQuotaInterface) scoped() corev1client.ResourceQuotaInterface {
	return c.client.Cluster(c.cluster.Path()).CoreV1().ResourceQuotas(c.namespace)
}

func (c *ctxResourceQuotaInterface) rebind(rq *corev1.ResourceQuota) *corev1.ResourceQuota {
	cp := rq.DeepCopy()
	cp.Namespace = c.namespace
	return cp
}

func (c *ctxResourceQuotaInterface) Create(ctx context.Context, rq *corev1.ResourceQuota, opts metav1.CreateOptions) (*corev1.ResourceQuota, error) {
	return c.scoped().Create(ctx, c.rebind(rq), opts)
}

func (c *ctxResourceQuotaInterface) Update(ctx context.Context, rq *corev1.ResourceQuota, opts metav1.UpdateOptions) (*corev1.ResourceQuota, error) {
	return c.scoped().Update(ctx, c.rebind(rq), opts)
}

func (c *ctxResourceQuotaInterface) UpdateStatus(ctx context.Context, rq *corev1.ResourceQuota, opts metav1.UpdateOptions) (*corev1.ResourceQuota, error) {
	return c.scoped().UpdateStatus(ctx, c.rebind(rq), opts)
}

func (c *ctxResourceQuotaInterface) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	return c.scoped().Delete(ctx, name, opts)
}

func (c *ctxResourceQuotaInterface) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error {
	return c.scoped().DeleteCollection(ctx, opts, listOpts)
}

func (c *ctxResourceQuotaInterface) Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.ResourceQuota, error) {
	return c.scoped().Get(ctx, name, opts)
}

func (c *ctxResourceQuotaInterface) List(ctx context.Context, opts metav1.ListOptions) (*corev1.ResourceQuotaList, error) {
	return c.scoped().List(ctx, opts)
}

func (c *ctxResourceQuotaInterface) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return c.scoped().Watch(ctx, opts)
}

func (c *ctxResourceQuotaInterface) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.ResourceQuota, error) {
	return c.scoped().Patch(ctx, name, pt, data, opts, subresources...)
}

func (c *ctxResourceQuotaInterface) Apply(ctx context.Context, rq *applyconfigurationscorev1.ResourceQuotaApplyConfiguration, opts metav1.ApplyOptions) (*corev1.ResourceQuota, error) {
	return c.scoped().Apply(ctx, c.rebindApply(rq), opts)
}

func (c *ctxResourceQuotaInterface) ApplyStatus(ctx context.Context, rq *applyconfigurationscorev1.ResourceQuotaApplyConfiguration, opts metav1.ApplyOptions) (*corev1.ResourceQuota, error) {
	return c.scoped().ApplyStatus(ctx, c.rebindApply(rq), opts)
}

func (c *ctxResourceQuotaInterface) rebindApply(ac *applyconfigurationscorev1.ResourceQuotaApplyConfiguration) *applyconfigurationscorev1.ResourceQuotaApplyConfiguration {
	cp := *ac
	ns := c.namespace
	cp.Namespace = &ns
	return &cp
}
