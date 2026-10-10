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

package apireconciler

import (
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	schemainformers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha1"
	exportinformers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"
)

func TestDeleteHandlers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name            string
		schema, wrapped bool
	}{
		{name: "APIExport"},
		{name: "APIExport tombstone", wrapped: true},
		{name: "APIResourceSchema", schema: true},
		{name: "APIResourceSchema tombstone", schema: true, wrapped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			factory := kcpinformers.NewSharedInformerFactory(nil, 0)
			exports := factory.Apis().V1alpha2().APIExports()
			schemas := factory.Apis().V1alpha1().APIResourceSchemas()
			exportEvents := &recordingInformer{ScopeableSharedIndexInformer: exports.Informer()}
			schemaEvents := &recordingInformer{ScopeableSharedIndexInformer: schemas.Informer()}
			c, err := NewAPIReconciler(nil,
				&recordingSchemaInformer{APIResourceSchemaClusterInformer: schemas, informer: schemaEvents},
				&recordingExportInformer{APIExportClusterInformer: exports, informer: exportEvents},
				factory.Apis().V1alpha2().APIBindings(), nil, nil)
			require.NoError(t, err)
			t.Cleanup(c.queue.ShutDown)

			export := &apisv1alpha2.APIExport{ObjectMeta: metav1.ObjectMeta{
				Name: "export", Annotations: map[string]string{logicalcluster.AnnotationKey: "provider"},
			}}
			// Use the real lister for schema events to verify the affected export is queued.
			require.NoError(t, exports.Informer().GetIndexer().Add(export))
			var obj any = export
			handler := exportEvents.handler
			if tc.schema {
				obj = &apisv1alpha1.APIResourceSchema{ObjectMeta: metav1.ObjectMeta{
					Name: "schema", Annotations: map[string]string{logicalcluster.AnnotationKey: "provider"},
				}}
				handler = schemaEvents.handler
			} else {
				require.NoError(t, exports.Informer().GetIndexer().Delete(export))
			}
			if tc.wrapped {
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
}

// Capture registered handlers while retaining the informers' real indexes and listers.
type recordingInformer struct {
	kcpcache.ScopeableSharedIndexInformer
	handler cache.ResourceEventHandler
}

func (i *recordingInformer) AddEventHandler(h cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error) {
	i.handler = h
	return nil, nil
}

type recordingExportInformer struct {
	exportinformers.APIExportClusterInformer
	informer *recordingInformer
}

func (i *recordingExportInformer) Informer() kcpcache.ScopeableSharedIndexInformer { return i.informer }

type recordingSchemaInformer struct {
	schemainformers.APIResourceSchemaClusterInformer
	informer *recordingInformer
}

func (i *recordingSchemaInformer) Informer() kcpcache.ScopeableSharedIndexInformer { return i.informer }
