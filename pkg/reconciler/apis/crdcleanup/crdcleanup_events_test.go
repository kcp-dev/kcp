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

package crdcleanup

import (
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	crdinformers "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	apisinformers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"

	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
)

func TestDeletionEvents(t *testing.T) {
	t.Parallel()
	boundCluster := apibinding.SystemBoundCRDsClusterName
	crd := func(cluster logicalcluster.Name) *apiextensionsv1.CustomResourceDefinition {
		return &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{
			Name: "schema-one", Annotations: map[string]string{logicalcluster.AnnotationKey: cluster.String()},
		}}
	}
	binding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "binding", Annotations: map[string]string{logicalcluster.AnnotationKey: "consumer"}},
		Status: apisv1alpha2.APIBindingStatus{BoundResources: []apisv1alpha2.BoundAPIResource{
			{Schema: apisv1alpha2.BoundAPIResourceSchema{UID: "schema-one"}},
			{Schema: apisv1alpha2.BoundAPIResourceSchema{UID: "schema-two"}},
		}},
	}
	for _, tc := range []struct {
		name    string
		object  any
		binding bool
		want    []string
	}{
		{name: "bound CRD", object: crd(boundCluster), want: []string{"schema-one"}},
		{name: "bound CRD tombstone", object: cache.DeletedFinalStateUnknown{Obj: crd(boundCluster)}, want: []string{"schema-one"}},
		{name: "other CRD", object: crd("other")},
		{name: "other CRD tombstone", object: cache.DeletedFinalStateUnknown{Obj: crd("other")}},
		{name: "APIBinding", object: binding, binding: true, want: []string{"schema-one", "schema-two"}},
		{name: "APIBinding tombstone", object: cache.DeletedFinalStateUnknown{Obj: binding}, binding: true, want: []string{"schema-one", "schema-two"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			crds, bindings := &recordingInformer{}, &recordingInformer{}
			c, err := NewController(&recordingCRDInformer{informer: crds}, nil, &recordingAPIBindingInformer{informer: bindings}, nil, nil)
			require.NoError(t, err)
			t.Cleanup(c.queue.ShutDown)
			handler := crds.handler
			if tc.binding {
				handler = bindings.handler
			}
			require.NotPanics(t, func() { handler.OnDelete(tc.object) })
			require.Equal(t, len(tc.want), c.queue.Len())
			var got []string
			for c.queue.Len() > 0 {
				key, shutdown := c.queue.Get()
				require.False(t, shutdown)
				got = append(got, key)
				c.queue.Done(key)
			}
			want := make([]string, 0, len(tc.want))
			for _, uid := range tc.want {
				want = append(want, kcpcache.ToClusterAwareKey(boundCluster.String(), "", uid))
			}
			require.ElementsMatch(t, want, got)
		})
	}
}

// Capture the real handlers registered by NewController without starting informer goroutines.
type recordingInformer struct {
	kcpcache.ScopeableSharedIndexInformer
	handler cache.ResourceEventHandler
}

func (i *recordingInformer) AddEventHandler(handler cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error) {
	i.handler = handler
	return nil, nil
}

type recordingCRDInformer struct {
	crdinformers.CustomResourceDefinitionClusterInformer
	informer *recordingInformer
}

func (i *recordingCRDInformer) Informer() kcpcache.ScopeableSharedIndexInformer { return i.informer }

type recordingAPIBindingInformer struct {
	apisinformers.APIBindingClusterInformer
	informer *recordingInformer
}

func (i *recordingAPIBindingInformer) Informer() kcpcache.ScopeableSharedIndexInformer {
	return i.informer
}
