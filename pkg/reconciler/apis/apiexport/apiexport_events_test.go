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

package apiexport

import (
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kubeinformers "github.com/kcp-dev/client-go/informers"
	coreinformers "github.com/kcp-dev/client-go/informers/core/v1"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpfake "github.com/kcp-dev/sdk/client/clientset/versioned/cluster/fake"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	apisinformers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha1"
	shardinformers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"
)

func TestDeleteHandlers(t *testing.T) {
	t.Parallel()
	for _, resource := range []string{"Secret", "Shard", "APIExportEndpointSlice"} {
		t.Run(resource, func(t *testing.T) {
			t.Parallel()
			for _, wrapped := range []bool{false, true} {
				name := "object"
				if wrapped {
					name = "tombstone"
				}
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					client := kcpfake.NewClientset()
					factory := kcpinformers.NewSharedInformerFactory(client, 0)
					kubeFactory := kubeinformers.NewSharedInformerFactory(nil, 0)
					secrets, shards, slices := &recordingInformer{}, &recordingInformer{}, &recordingInformer{}
					c, err := NewController(client, factory.Apis().V1alpha2().APIExports(),
						&recordingSliceInformer{informer: slices}, &recordingShardInformer{informer: shards},
						nil, kubeFactory.Core().V1().Namespaces(), &recordingSecretInformer{informer: secrets}, "root")
					require.NoError(t, err)
					t.Cleanup(c.queue.ShutDown)
					export := &apisv1alpha2.APIExport{ObjectMeta: metav1.ObjectMeta{
						Name: "export", Annotations: map[string]string{logicalcluster.AnnotationKey: "provider"},
					}}
					var obj any
					var handler cache.ResourceEventHandler
					switch resource {
					case "Secret":
						secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "identity", Namespace: "default"}}
						obj, handler = secret, secrets.handler
						c.listAPIExportsForSecret = func(got *corev1.Secret) ([]*apisv1alpha2.APIExport, error) {
							require.Same(t, secret, got)
							return []*apisv1alpha2.APIExport{export}, nil
						}
					case "Shard":
						obj, handler = &corev1alpha1.Shard{ObjectMeta: metav1.ObjectMeta{Name: "shard-1"}}, shards.handler
						c.listAPIExports = func() ([]*apisv1alpha2.APIExport, error) { return []*apisv1alpha2.APIExport{export}, nil }
					case "APIExportEndpointSlice":
						slice := &apisv1alpha1.APIExportEndpointSlice{ObjectMeta: metav1.ObjectMeta{
							Name: "slice", Annotations: map[string]string{logicalcluster.AnnotationKey: "provider"},
						}}
						slice.Spec.APIExport.Name = export.Name
						obj, handler = slice, slices.handler
						c.getAPIExport = func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIExport, error) {
							require.Equal(t, logicalcluster.Name("provider"), cluster)
							require.Equal(t, export.Name, name)
							return export, nil
						}
					}
					if wrapped {
						obj = cache.DeletedFinalStateUnknown{Obj: obj}
					}
					require.NotPanics(t, func() { handler.OnDelete(obj) })
					require.Equal(t, 1, c.queue.Len())
					key, shutdown := c.queue.Get()
					require.False(t, shutdown)
					c.queue.Done(key)
					require.Equal(t, kcpcache.ToClusterAwareKey("provider", "", "export"), key)
				})
			}
		})
	}
}

type recordingInformer struct {
	kcpcache.ScopeableSharedIndexInformer
	handler cache.ResourceEventHandler
}

func (i *recordingInformer) AddEventHandler(h cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error) {
	i.handler = h
	return nil, nil
}

type recordingSecretInformer struct {
	coreinformers.SecretClusterInformer
	informer *recordingInformer
}

func (i *recordingSecretInformer) Informer() kcpcache.ScopeableSharedIndexInformer { return i.informer }

type recordingShardInformer struct {
	shardinformers.ShardClusterInformer
	informer *recordingInformer
}

func (i *recordingShardInformer) Informer() kcpcache.ScopeableSharedIndexInformer { return i.informer }

type recordingSliceInformer struct {
	apisinformers.APIExportEndpointSliceClusterInformer
	informer *recordingInformer
}

func (i *recordingSliceInformer) Informer() kcpcache.ScopeableSharedIndexInformer { return i.informer }
