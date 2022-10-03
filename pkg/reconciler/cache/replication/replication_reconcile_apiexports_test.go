/*
Copyright 2022 The KCP Authors.

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

package replication

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clientgotesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
)

var scheme *runtime.Scheme

func init() {
	scheme = runtime.NewScheme()
	_ = apisv1alpha1.AddToScheme(scheme)
}

func TestReconcileAPIExports(t *testing.T) {
	scenarios := []struct {
		name                                     string
		initialLocalApiExports                   []runtime.Object
		initialCacheApiExports                   []runtime.Object
		initCacheFakeClientWithInitialApiExports bool
		reconcileKey                             string
		validateFunc                             func(ts *testing.T, cacheClientActions []clientgotesting.Action, localClientActions []clientgotesting.Action, targetClusterCacheClient, targetClusterLocalClient logicalcluster.Name)
	}{
		{
			name:                   "case 1: creation of the object in the cache server",
			initialLocalApiExports: []runtime.Object{newAPIExport("foo")},
			reconcileKey:           "root|foo",
			validateFunc: func(ts *testing.T, cacheClientActions []clientgotesting.Action, localClientActions []clientgotesting.Action, targetClusterCacheClient, targetClusterLocalClient logicalcluster.Name) {
				if len(localClientActions) != 0 {
					ts.Fatal("unexpected REST calls were made to the localDynamicClient")
				}
				if targetClusterCacheClient.String() != "root" {
					ts.Fatalf("wrong cluster = %s was targeted for cacheDynamicClient", targetClusterCacheClient)
				}
				wasCacheApiExportValidated := false
				for _, action := range cacheClientActions {
					if action.Matches("create", "apiexports") {
						createAction := action.(clientgotesting.CreateAction)
						createdUnstructuredApiExport := createAction.GetObject().(*unstructured.Unstructured)
						cacheApiExportFromUnstructured := &apisv1alpha1.APIExport{}
						if err := runtime.DefaultUnstructuredConverter.FromUnstructured(createdUnstructuredApiExport.Object, cacheApiExportFromUnstructured); err != nil {
							ts.Fatalf("failed to convert unstructured to APIExport: %v", err)
						}

						expectedApiExport := newAPIExport("foo")
						if !equality.Semantic.DeepEqual(cacheApiExportFromUnstructured, expectedApiExport) {
							ts.Errorf("unexpected ApiExport was creaetd:\n%s", cmp.Diff(cacheApiExportFromUnstructured, expectedApiExport))
						}
						wasCacheApiExportValidated = true
						break
					}
				}
				if !wasCacheApiExportValidated {
					ts.Errorf("an ApiExport on the cache sever wasn't created")
				}
			},
		},
		{
			name: "case 2: cached object is removed when local object was removed",
			initialLocalApiExports: []runtime.Object{
				func() *apisv1alpha1.APIExport {
					t := metav1.NewTime(time.Now())
					apiExport := newAPIExport("foo")
					apiExport.DeletionTimestamp = &t
					apiExport.Finalizers = []string{"aFinalizer"}
					return apiExport
				}(),
			},
			initialCacheApiExports:                   []runtime.Object{newAPIExport("foo")},
			initCacheFakeClientWithInitialApiExports: true,
			reconcileKey:                             "root|foo",
			validateFunc: func(ts *testing.T, cacheClientActions []clientgotesting.Action, localClientActions []clientgotesting.Action, targetClusterCacheClient, targetClusterLocalClient logicalcluster.Name) {
				if len(localClientActions) != 0 {
					ts.Fatalf("wrong cluster = %s was targeted for cacheDynamicClient", targetClusterCacheClient)
				}
				if targetClusterCacheClient.String() != "root" {
					ts.Fatalf("wrong cluster = %s was targeted", targetClusterCacheClient)
				}
				wasCacheApiExportValidated := false
				for _, action := range cacheClientActions {
					if action.Matches("delete", "apiexports") {
						deleteAction := action.(clientgotesting.DeleteAction)
						if deleteAction.GetName() != "foo" {
							ts.Fatalf("unexpected APIExport was removed = %v, expected = %v", deleteAction.GetName(), "foo")
						}
						wasCacheApiExportValidated = true
						break
					}
				}
				if !wasCacheApiExportValidated {
					ts.Errorf("an ApiExport on the cache sever wasn't deleted")
				}
			},
		},
		{
			name:                                     "case 2: cached object is removed when local object was not found",
			initialCacheApiExports:                   []runtime.Object{newAPIExport("foo")},
			initCacheFakeClientWithInitialApiExports: true,
			reconcileKey:                             "root|foo",
			validateFunc: func(ts *testing.T, cacheClientActions []clientgotesting.Action, localClientActions []clientgotesting.Action, targetClusterCacheClient, targetClusterLocalClient logicalcluster.Name) {
				if targetClusterCacheClient.String() != "root" {
					ts.Fatalf("wrong cluster = %s was targeted for cacheDynamicClient", targetClusterCacheClient)
				}
				if targetClusterCacheClient.String() != "root" {
					ts.Fatalf("wrong cluster = %s was targeted for localDynamicClient", targetClusterLocalClient)
				}
				wasCacheApiExportDeletionValidated := false
				wasCacheApiExportRetrievalValidated := false
				for _, action := range localClientActions {
					if action.Matches("get", "apiexports") {
						getAction := action.(clientgotesting.GetAction)
						if getAction.GetName() != "foo" {
							ts.Fatalf("unexpected ApiExport was retrieved = %s, expected = %s", getAction.GetName(), "foo")
						}
						wasCacheApiExportRetrievalValidated = true
						break
					}
				}
				if !wasCacheApiExportRetrievalValidated {
					ts.Errorf("before deleting an ApiExport the controller should live GET it")
				}
				for _, action := range cacheClientActions {
					if action.Matches("delete", "apiexports") {
						deleteAction := action.(clientgotesting.DeleteAction)
						if deleteAction.GetName() != "foo" {
							ts.Fatalf("unexpected APIExport was removed = %v, expected = %v", deleteAction.GetName(), "foo")
						}
						wasCacheApiExportDeletionValidated = true
						break
					}
				}
				if !wasCacheApiExportDeletionValidated {
					ts.Errorf("an ApiExport on the cache sever wasn't deleted")
				}
			},
		},
		{
			name: "case 3: update, metadata mismatch",
			initialLocalApiExports: []runtime.Object{
				func() *apisv1alpha1.APIExport {
					apiExport := newAPIExport("foo")
					apiExport.Labels["fooLabel"] = "fooLabelVal"
					return apiExport
				}(),
			},
			initialCacheApiExports:                   []runtime.Object{newAPIExport("foo")},
			initCacheFakeClientWithInitialApiExports: true,
			reconcileKey:                             "root|foo",
			validateFunc: func(ts *testing.T, cacheClientActions []clientgotesting.Action, localClientActions []clientgotesting.Action, targetClusterCacheClient, targetClusterLocalClient logicalcluster.Name) {
				if len(localClientActions) != 0 {
					ts.Fatal("unexpected REST calls were made to the localDynamicClient")
				}
				if targetClusterCacheClient.String() != "root" {
					ts.Fatalf("wrong cluster = %s was targeted for cacheDynamicClient", targetClusterCacheClient)
				}
				wasCacheApiExportValidated := false
				for _, action := range cacheClientActions {
					if action.Matches("update", "apiexports") {
						updateAction := action.(clientgotesting.UpdateAction)
						updatedUnstructuredApiExport := updateAction.GetObject().(*unstructured.Unstructured)
						cacheApiExportFromUnstructured := &apisv1alpha1.APIExport{}
						if err := runtime.DefaultUnstructuredConverter.FromUnstructured(updatedUnstructuredApiExport.Object, cacheApiExportFromUnstructured); err != nil {
							ts.Fatalf("failed to convert unstructured to APIExport: %v", err)
						}

						expectedApiExport := newAPIExport("foo")
						expectedApiExport.Labels["fooLabel"] = "fooLabelVal"
						if !equality.Semantic.DeepEqual(cacheApiExportFromUnstructured, expectedApiExport) {
							ts.Errorf("unexpected update to the ApiExport:\n%s", cmp.Diff(cacheApiExportFromUnstructured, expectedApiExport))
						}
						wasCacheApiExportValidated = true
						break
					}
				}
				if !wasCacheApiExportValidated {
					ts.Errorf("an ApiExport on the cache sever wasn't updated")
				}
			},
		},
		{
			name: "case 3: update, spec changed",
			initialLocalApiExports: []runtime.Object{
				func() *apisv1alpha1.APIExport {
					apiExport := newAPIExport("foo")
					apiExport.Spec.PermissionClaims = []apisv1alpha1.PermissionClaim{{GroupResource: apisv1alpha1.GroupResource{}, IdentityHash: "abc"}}
					return apiExport
				}(),
			},
			initialCacheApiExports:                   []runtime.Object{newAPIExport("foo")},
			initCacheFakeClientWithInitialApiExports: true,
			reconcileKey:                             "root|foo",
			validateFunc: func(ts *testing.T, cacheClientActions []clientgotesting.Action, localClientActions []clientgotesting.Action, targetClusterCacheClient, targetClusterLocalClient logicalcluster.Name) {
				if len(localClientActions) != 0 {
					ts.Fatal("unexpected REST calls were made to the localDynamicClient")
				}
				if targetClusterCacheClient.String() != "root" {
					ts.Fatalf("wrong cluster = %s was targeted for cacheDynamicClient", targetClusterCacheClient)
				}
				wasCacheApiExportValidated := false
				for _, action := range cacheClientActions {
					if action.Matches("update", "apiexports") {
						updateAction := action.(clientgotesting.UpdateAction)
						updatedUnstructuredApiExport := updateAction.GetObject().(*unstructured.Unstructured)
						cacheApiExportFromUnstructured := &apisv1alpha1.APIExport{}
						if err := runtime.DefaultUnstructuredConverter.FromUnstructured(updatedUnstructuredApiExport.Object, cacheApiExportFromUnstructured); err != nil {
							ts.Fatalf("failed to convert unstructured to APIExport: %v", err)
						}

						expectedApiExport := newAPIExport("foo")
						expectedApiExport.Spec.PermissionClaims = []apisv1alpha1.PermissionClaim{{GroupResource: apisv1alpha1.GroupResource{}, IdentityHash: "abc"}}
						if !equality.Semantic.DeepEqual(cacheApiExportFromUnstructured, expectedApiExport) {
							ts.Errorf("unexpected update to the ApiExport:\n%s", cmp.Diff(cacheApiExportFromUnstructured, expectedApiExport))
						}
						wasCacheApiExportValidated = true
						break
					}
				}
				if !wasCacheApiExportValidated {
					ts.Errorf("an ApiExport on the cache sever wasn't updated")
				}
			},
		},
		{
			name: "case 3: update, status changed",
			initialLocalApiExports: []runtime.Object{
				func() *apisv1alpha1.APIExport {
					apiExport := newAPIExport("foo")
					apiExport.Status.VirtualWorkspaces = []apisv1alpha1.VirtualWorkspace{{URL: "https://acme.dev"}}
					return apiExport
				}(),
			},
			initialCacheApiExports:                   []runtime.Object{newAPIExport("foo")},
			initCacheFakeClientWithInitialApiExports: true,
			reconcileKey:                             "root|foo",
			validateFunc: func(ts *testing.T, cacheClientActions []clientgotesting.Action, localClientActions []clientgotesting.Action, targetClusterCacheClient, targetClusterLocalClient logicalcluster.Name) {
				if len(localClientActions) != 0 {
					ts.Fatal("unexpected REST calls were made to the localDynamicClient")
				}
				if targetClusterCacheClient.String() != "root" {
					ts.Fatalf("wrong cluster = %s was targeted for cacheDynamicClient", targetClusterCacheClient)
				}
				wasCacheApiExportValidated := false
				for _, action := range cacheClientActions {
					if action.Matches("update", "apiexports") {
						updateAction := action.(clientgotesting.UpdateAction)
						updatedUnstructuredApiExport := updateAction.GetObject().(*unstructured.Unstructured)
						cacheApiExportFromUnstructured := &apisv1alpha1.APIExport{}
						if err := runtime.DefaultUnstructuredConverter.FromUnstructured(updatedUnstructuredApiExport.Object, cacheApiExportFromUnstructured); err != nil {
							ts.Fatalf("failed to convert unstructured to APIExport: %v", err)
						}

						expectedApiExport := newAPIExport("foo")
						expectedApiExport.Status.VirtualWorkspaces = []apisv1alpha1.VirtualWorkspace{{URL: "https://acme.dev"}}
						if !equality.Semantic.DeepEqual(cacheApiExportFromUnstructured, expectedApiExport) {
							ts.Errorf("unexpected update to the ApiExport:\n%s", cmp.Diff(cacheApiExportFromUnstructured, expectedApiExport))
						}
						wasCacheApiExportValidated = true
						break
					}
				}
				if !wasCacheApiExportValidated {
					ts.Errorf("an ApiExport on the cache sever wasn't updated")
				}
			},
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(tt *testing.T) {
			target := &controller{shardName: "amber"}
			localApiExportIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			for _, obj := range scenario.initialLocalApiExports {
				if err := localApiExportIndexer.Add(obj); err != nil {
					tt.Error(err)
				}
			}
			target.localApiExportLister = apislisters.NewAPIExportLister(localApiExportIndexer)
			target.cacheApiExportsIndexer = cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{ByShardAndLogicalClusterAndNamespaceAndName: IndexByShardAndLogicalClusterAndNamespace})
			for _, obj := range scenario.initialCacheApiExports {
				if err := target.cacheApiExportsIndexer.Add(obj); err != nil {
					tt.Error(err)
				}
			}
			fakeCacheDynamicClient := newFakeKcpClusterClient(dynamicfake.NewSimpleDynamicClient(scheme, func() []runtime.Object {
				if scenario.initCacheFakeClientWithInitialApiExports {
					return scenario.initialCacheApiExports
				}
				return []runtime.Object{}
			}()...))
			target.dynamicCacheClient = fakeCacheDynamicClient
			fakeLocalDynamicClient := newFakeKcpClusterClient(dynamicfake.NewSimpleDynamicClient(scheme))
			target.dynamicLocalClient = fakeLocalDynamicClient
			if err := target.reconcileAPIExports(context.TODO(), scenario.reconcileKey, apisv1alpha1.SchemeGroupVersion.WithResource("apiexports")); err != nil {
				tt.Fatal(err)
			}
			if scenario.validateFunc != nil {
				scenario.validateFunc(tt, fakeCacheDynamicClient.fakeDs.Actions(), fakeLocalDynamicClient.fakeDs.Actions(), fakeCacheDynamicClient.cluster, fakeLocalDynamicClient.cluster)
			}
		})
	}
}

func newAPIExport(name string) *apisv1alpha1.APIExport {
	return &apisv1alpha1.APIExport{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apis.kcp.dev/v1alpha1",
			Kind:       "APIExport",
		},
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{},
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: "root",
				"kcp.dev/shard":              "amber",
			},
			Name: name,
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"lrs"},
		},
		Status: apisv1alpha1.APIExportStatus{
			IdentityHash: fmt.Sprintf("%s-identity", name),
		},
	}
}

func newFakeKcpClusterClient(ds *dynamicfake.FakeDynamicClient) *fakeKcpClusterClient {
	return &fakeKcpClusterClient{fakeDs: ds}
}

type fakeKcpClusterClient struct {
	fakeDs  *dynamicfake.FakeDynamicClient
	cluster logicalcluster.Name
}

func (f *fakeKcpClusterClient) Cluster(name logicalcluster.Name) dynamic.Interface {
	f.cluster = name
	return f.fakeDs
}
