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

package apibinding

import (
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	crdinformers "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpfake "github.com/kcp-dev/sdk/client/clientset/versioned/cluster/fake"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
)

func TestCRDDeleteHandler(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		cluster logicalcluster.Name
		wrapped bool
	}{
		{"bound CRD", SystemBoundCRDsClusterName, false},
		{"bound CRD tombstone", SystemBoundCRDsClusterName, true},
		{"other CRD", "other", false},
		{"other CRD tombstone", "other", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := kcpfake.NewClientset()
			factory := kcpinformers.NewSharedInformerFactory(client, 0)
			crds := &recordingInformer{}
			c, err := NewController(nil, client, factory.Apis().V1alpha2().APIBindings(),
				factory.Apis().V1alpha2().APIExports(), factory.Apis().V1alpha1().APIResourceSchemas(),
				factory.Apis().V1alpha1().APIConversions(), factory.Core().V1alpha1().LogicalClusters(),
				factory.Apis().V1alpha2().APIExports(), factory.Apis().V1alpha1().APIResourceSchemas(),
				factory.Apis().V1alpha1().APIConversions(), &recordingCRDInformer{informer: crds}, "root")
			require.NoError(t, err)
			t.Cleanup(c.queue.ShutDown)
			crd := &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{
				Name: "schema-uid", Annotations: map[string]string{
					logicalcluster.AnnotationKey:            tc.cluster.String(),
					apisv1alpha1.AnnotationSchemaClusterKey: "provider",
					apisv1alpha1.AnnotationSchemaNameKey:    "schema",
				},
			}}
			schema := &apisv1alpha1.APIResourceSchema{ObjectMeta: metav1.ObjectMeta{Name: "schema"}}
			export := &apisv1alpha2.APIExport{}
			binding := &apisv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{
				Name: "binding", Annotations: map[string]string{logicalcluster.AnnotationKey: "consumer"},
			}}
			c.getAPIResourceSchema = func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
				require.Equal(t, logicalcluster.Name("provider"), cluster)
				require.Equal(t, "schema", name)
				return schema, nil
			}
			c.getAPIExportsBySchema = func(got *apisv1alpha1.APIResourceSchema) ([]*apisv1alpha2.APIExport, error) {
				require.Same(t, schema, got)
				return []*apisv1alpha2.APIExport{export}, nil
			}
			c.listAPIBindingsByAPIExport = func(got *apisv1alpha2.APIExport) ([]*apisv1alpha2.APIBinding, error) {
				require.Same(t, export, got)
				return []*apisv1alpha2.APIBinding{binding}, nil
			}
			var obj any = crd
			if tc.wrapped {
				obj = cache.DeletedFinalStateUnknown{Obj: crd}
			}
			require.NotPanics(t, func() { crds.handler.OnDelete(obj) })
			bound := tc.cluster == SystemBoundCRDsClusterName
			require.Equal(t, bound, c.deletedCRDTracker.Has(crd.Name))
			if !bound {
				require.Zero(t, c.queue.Len())
				return
			}
			require.Equal(t, 1, c.queue.Len())
			key, shutdown := c.queue.Get()
			require.False(t, shutdown)
			c.queue.Done(key)
			require.Equal(t, kcpcache.ToClusterAwareKey("consumer", "", "binding"), key)
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

type recordingCRDInformer struct {
	crdinformers.CustomResourceDefinitionClusterInformer
	informer *recordingInformer
}

func (i *recordingCRDInformer) Informer() kcpcache.ScopeableSharedIndexInformer { return i.informer }
