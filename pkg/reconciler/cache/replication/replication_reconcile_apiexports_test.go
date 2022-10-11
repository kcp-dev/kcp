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
	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

func TestReconcileAPIExports(t *testing.T) {
	scenarios := []struct {
		name                  string
		initialLocalApiExport *apisv1alpha1.APIExport
		initialCacheApiExport *apisv1alpha1.APIExport
		reconcileKey          string
		getCachedAPIExport    func(shardName string, cluster logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error)
		getLocalAPIExport     func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error)
		getLocalObject        func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error)
		createCachedObject    func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace string, object *unstructured.Unstructured) (*unstructured.Unstructured, error)
		updateCachedObject    func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace string, object *unstructured.Unstructured) (*unstructured.Unstructured, error)
		deleteCachedObject    func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace, name string) error
		validateCalls         func(t *testing.T, calls callContext)
	}{
		{
			name:                  "case 1: creation of the object in the cache server",
			initialLocalApiExport: newAPIExport("foo"),
			reconcileKey:          "root|foo",
			createCachedObject: func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace string, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				if diff := cmp.Diff(gvr, apisv1alpha1.SchemeGroupVersion.WithResource("apiexports")); diff != "" {
					return nil, fmt.Errorf("got CREATE for incorrect GVR: %v", diff)
				}
				if actual, expected := kcpcache.ToClusterAwareKey(cluster.String(), namespace, object.GetName()), kcpcache.ToClusterAwareKey("root", "", "foo"); actual != expected {
					return nil, fmt.Errorf("got CREATE %s, expected CREATE %s", actual, expected)
				}
				cacheApiExportFromUnstructured := &apisv1alpha1.APIExport{}
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, cacheApiExportFromUnstructured); err != nil {
					return nil, fmt.Errorf("failed to convert unstructured to APIExport: %w", err)
				}

				expectedApiExport := newAPIExportWithShardAnnotation("foo")
				if !equality.Semantic.DeepEqual(cacheApiExportFromUnstructured, expectedApiExport) {
					return nil, fmt.Errorf("unexpected ApiExport was created:\n%s", cmp.Diff(cacheApiExportFromUnstructured, expectedApiExport))
				}
				return object, nil
			},
			validateCalls: func(t *testing.T, calls callContext) {
				if !calls.createCachedObject.called {
					t.Error("cached object was not created")
				}
			},
		},
		{
			name: "case 2: cached object is removed when local object was removed",
			initialLocalApiExport: func() *apisv1alpha1.APIExport {
				t := metav1.NewTime(time.Now())
				apiExport := newAPIExport("foo")
				apiExport.DeletionTimestamp = &t
				apiExport.Finalizers = []string{"aFinalizer"}
				return apiExport
			}(),
			initialCacheApiExport: newAPIExportWithShardAnnotation("foo"),
			reconcileKey:          "root|foo",
			deleteCachedObject: func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace, name string) error {
				if diff := cmp.Diff(gvr, apisv1alpha1.SchemeGroupVersion.WithResource("apiexports")); diff != "" {
					return fmt.Errorf("got DELETE for incorrect GVR: %v", diff)
				}
				if actual, expected := kcpcache.ToClusterAwareKey(cluster.String(), namespace, name), kcpcache.ToClusterAwareKey("root", "", "foo"); actual != expected {
					return fmt.Errorf("got DELETE %s, expected DELETE %s", actual, expected)
				}
				return nil
			},
			validateCalls: func(t *testing.T, calls callContext) {
				if !calls.deleteCachedObject.called {
					t.Error("cached object was not deleted")
				}
			},
		},
		{
			name:                  "case 2: cached object is removed when local object was not found",
			initialCacheApiExport: newAPIExportWithShardAnnotation("foo"),
			reconcileKey:          "root|foo",
			getLocalAPIExport: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error) {
				if actual, expected := kcpcache.ToClusterAwareKey(cluster.String(), "", name), kcpcache.ToClusterAwareKey("root", "", "foo"); actual != expected {
					return nil, fmt.Errorf("got GET %s, expected GET %s", actual, expected)
				}
				return nil, errors.NewNotFound(apisv1alpha1.Resource("apiexports"), "foo")
			},
			getLocalObject: func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
				if diff := cmp.Diff(gvr, apisv1alpha1.SchemeGroupVersion.WithResource("apiexports")); diff != "" {
					return nil, fmt.Errorf("got GET for incorrect GVR: %v", diff)
				}
				if actual, expected := kcpcache.ToClusterAwareKey(cluster.String(), namespace, name), kcpcache.ToClusterAwareKey("root", "", "foo"); actual != expected {
					return nil, fmt.Errorf("got GET %s, expected GET %s", actual, expected)
				}
				return nil, errors.NewNotFound(apisv1alpha1.Resource("apiexports"), "foo")
			},
			deleteCachedObject: func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace, name string) error {
				if diff := cmp.Diff(gvr, apisv1alpha1.SchemeGroupVersion.WithResource("apiexports")); diff != "" {
					return fmt.Errorf("got DELETE for incorrect GVR: %v", diff)
				}
				if actual, expected := kcpcache.ToClusterAwareKey(cluster.String(), namespace, name), kcpcache.ToClusterAwareKey("root", "", "foo"); actual != expected {
					return fmt.Errorf("got DELETE %s, expected DELETE %s", actual, expected)
				}
				return nil
			},
			validateCalls: func(t *testing.T, calls callContext) {
				if !calls.getLocalAPIExport.called {
					t.Error("before deleting an ApiExport the controller should cached GET it")
				}
				if !calls.getLocalObject.called {
					t.Error("before deleting an ApiExport the controller should live GET it")
				}
				if !calls.deleteCachedObject.called {
					t.Error("an ApiExport on the cache sever wasn't deleted")
				}
			},
		},
		{
			name: "case 3: update, metadata mismatch",
			initialLocalApiExport: func() *apisv1alpha1.APIExport {
				apiExport := newAPIExport("foo")
				apiExport.Labels["fooLabel"] = "fooLabelVal"
				return apiExport
			}(),
			initialCacheApiExport: newAPIExportWithShardAnnotation("foo"),
			reconcileKey:          "root|foo",
			updateCachedObject: func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace string, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				if diff := cmp.Diff(gvr, apisv1alpha1.SchemeGroupVersion.WithResource("apiexports")); diff != "" {
					return nil, fmt.Errorf("got UPDATE for incorrect GVR: %v", diff)
				}
				if actual, expected := kcpcache.ToClusterAwareKey(cluster.String(), namespace, object.GetName()), kcpcache.ToClusterAwareKey("root", "", "foo"); actual != expected {
					return nil, fmt.Errorf("got UPDATE %s, expected UPDATE %s", actual, expected)
				}
				cacheApiExportFromUnstructured := &apisv1alpha1.APIExport{}
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, cacheApiExportFromUnstructured); err != nil {
					return nil, fmt.Errorf("failed to convert unstructured to APIExport: %w", err)
				}

				expectedApiExport := newAPIExportWithShardAnnotation("foo")
				expectedApiExport.Labels["fooLabel"] = "fooLabelVal"
				if !equality.Semantic.DeepEqual(cacheApiExportFromUnstructured, expectedApiExport) {
					return nil, fmt.Errorf("unexpected update to the ApiExport:\n%s", cmp.Diff(cacheApiExportFromUnstructured, expectedApiExport))
				}
				return object, nil
			},
			validateCalls: func(t *testing.T, calls callContext) {
				if !calls.updateCachedObject.called {
					t.Error("an ApiExport on the cache sever wasn't updated")
				}
			},
		},
		{
			name: "case 3: update, spec changed",
			initialLocalApiExport: func() *apisv1alpha1.APIExport {
				apiExport := newAPIExport("foo")
				apiExport.Spec.PermissionClaims = []apisv1alpha1.PermissionClaim{{GroupResource: apisv1alpha1.GroupResource{}, IdentityHash: "abc"}}
				return apiExport
			}(),
			initialCacheApiExport: newAPIExportWithShardAnnotation("foo"),
			reconcileKey:          "root|foo",
			updateCachedObject: func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace string, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				if diff := cmp.Diff(gvr, apisv1alpha1.SchemeGroupVersion.WithResource("apiexports")); diff != "" {
					return nil, fmt.Errorf("got UPDATE for incorrect GVR: %v", diff)
				}
				if actual, expected := kcpcache.ToClusterAwareKey(cluster.String(), namespace, object.GetName()), kcpcache.ToClusterAwareKey("root", "", "foo"); actual != expected {
					return nil, fmt.Errorf("got UPDATE %s, expected UPDATE %s", actual, expected)
				}
				cacheApiExportFromUnstructured := &apisv1alpha1.APIExport{}
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, cacheApiExportFromUnstructured); err != nil {
					return nil, fmt.Errorf("failed to convert unstructured to APIExport: %w", err)
				}

				expectedApiExport := newAPIExportWithShardAnnotation("foo")
				expectedApiExport.Spec.PermissionClaims = []apisv1alpha1.PermissionClaim{{GroupResource: apisv1alpha1.GroupResource{}, IdentityHash: "abc"}}
				if !equality.Semantic.DeepEqual(cacheApiExportFromUnstructured, expectedApiExport) {
					return nil, fmt.Errorf("unexpected update to the ApiExport:\n%s", cmp.Diff(cacheApiExportFromUnstructured, expectedApiExport))
				}
				return object, nil
			},
			validateCalls: func(t *testing.T, calls callContext) {
				if !calls.updateCachedObject.called {
					t.Error("an ApiExport on the cache sever wasn't updated")
				}
			},
		},
		{
			name: "case 3: update, status changed",
			initialLocalApiExport: func() *apisv1alpha1.APIExport {
				apiExport := newAPIExport("foo")
				apiExport.Status.VirtualWorkspaces = []apisv1alpha1.VirtualWorkspace{{URL: "https://acme.dev"}}
				return apiExport
			}(),
			initialCacheApiExport: newAPIExportWithShardAnnotation("foo"),
			reconcileKey:          "root|foo",
			updateCachedObject: func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace string, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
				if diff := cmp.Diff(gvr, apisv1alpha1.SchemeGroupVersion.WithResource("apiexports")); diff != "" {
					return nil, fmt.Errorf("got UPDATE for incorrect GVR: %v", diff)
				}
				if actual, expected := kcpcache.ToClusterAwareKey(cluster.String(), namespace, object.GetName()), kcpcache.ToClusterAwareKey("root", "", "foo"); actual != expected {
					return nil, fmt.Errorf("got UPDATE %s, expected UPDATE %s", actual, expected)
				}
				cacheApiExportFromUnstructured := &apisv1alpha1.APIExport{}
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, cacheApiExportFromUnstructured); err != nil {
					return nil, fmt.Errorf("failed to convert unstructured to APIExport: %w", err)
				}

				expectedApiExport := newAPIExportWithShardAnnotation("foo")
				expectedApiExport.Status.VirtualWorkspaces = []apisv1alpha1.VirtualWorkspace{{URL: "https://acme.dev"}}
				if !equality.Semantic.DeepEqual(cacheApiExportFromUnstructured, expectedApiExport) {
					return nil, fmt.Errorf("unexpected update to the ApiExport:\n%s", cmp.Diff(cacheApiExportFromUnstructured, expectedApiExport))
				}
				return object, nil
			},
			validateCalls: func(t *testing.T, calls callContext) {
				if !calls.updateCachedObject.called {
					t.Error("an ApiExport on the cache sever wasn't updated")
				}
			},
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(tt *testing.T) {
			calls := callContext{
				getCachedAPIExport: getCachedAPIExportRecord{
					delegate: scenario.getCachedAPIExport,
					defaulted: func(shardName string, cluster logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error) {
						return scenario.initialCacheApiExport, nil
					},
				},
				getLocalAPIExport: getLocalAPIExportRecord{
					delegate: scenario.getLocalAPIExport,
					defaulted: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error) {
						return scenario.initialLocalApiExport, nil
					},
				},
				getLocalObject: getLocalObjectRecord{
					delegate: scenario.getLocalObject,
					defaulted: func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
						err := fmt.Errorf("unexpected live call to get local object %s %s", gvr, kcpcache.ToClusterAwareKey(cluster.String(), namespace, name))
						t.Error(err)
						return nil, err
					},
				},
				createCachedObject: createCachedObjectRecord{
					delegate: scenario.createCachedObject,
					defaulted: func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace string, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						err := fmt.Errorf("unexpected live call to create cached object %s %s", gvr, kcpcache.ToClusterAwareKey(cluster.String(), namespace, object.GetName()))
						t.Error(err)
						return nil, err
					},
				},
				updateCachedObject: updateCachedObjectRecord{
					delegate: scenario.updateCachedObject,
					defaulted: func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace string, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
						err := fmt.Errorf("unexpected live call to update cached object %s %s", gvr, kcpcache.ToClusterAwareKey(cluster.String(), namespace, object.GetName()))
						t.Error(err)
						return nil, err
					},
				},
				deleteCachedObject: deleteCachedObjectRecord{
					delegate: scenario.deleteCachedObject,
					defaulted: func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace, name string) error {
						err := fmt.Errorf("unexpected live call to delete cached object %s %s", gvr, kcpcache.ToClusterAwareKey(cluster.String(), namespace, name))
						t.Error(err)
						return err
					},
				},
			}
			target := &controller{
				shardName:          "amber",
				getCachedAPIExport: calls.getCachedAPIExport.call,
				getLocalAPIExport:  calls.getLocalAPIExport.call,
				getLocalObject:     calls.getLocalObject.call,
				createCachedObject: calls.createCachedObject.call,
				updateCachedObject: calls.updateCachedObject.call,
				deleteCachedObject: calls.deleteCachedObject.call,
			}
			if err := target.reconcileAPIExports(context.TODO(), scenario.reconcileKey, apisv1alpha1.SchemeGroupVersion.WithResource("apiexports")); err != nil {
				tt.Fatal(err)
			}
			if scenario.validateCalls != nil {
				scenario.validateCalls(t, calls)
			}
		})
	}
}

type callContext struct {
	getCachedAPIExport getCachedAPIExportRecord
	getLocalAPIExport  getLocalAPIExportRecord
	getLocalObject     getLocalObjectRecord
	createCachedObject createCachedObjectRecord
	updateCachedObject updateCachedObjectRecord
	deleteCachedObject deleteCachedObjectRecord
}

type getCachedAPIExportRecord struct {
	called              bool
	delegate, defaulted func(shardName string, cluster logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error)
}

func (r *getCachedAPIExportRecord) call(shardName string, cluster logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error) {
	r.called = true
	delegate := r.delegate
	if delegate == nil {
		delegate = r.defaulted
	}
	return delegate(shardName, cluster, name)
}

type getLocalAPIExportRecord struct {
	called              bool
	delegate, defaulted func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error)
}

func (r *getLocalAPIExportRecord) call(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error) {
	r.called = true
	delegate := r.delegate
	if delegate == nil {
		delegate = r.defaulted
	}
	return delegate(cluster, name)
}

type getLocalObjectRecord struct {
	called              bool
	delegate, defaulted func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error)
}

func (r *getLocalObjectRecord) call(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
	r.called = true
	delegate := r.delegate
	if delegate == nil {
		delegate = r.defaulted
	}
	return delegate(ctx, gvr, cluster, namespace, name)
}

type createCachedObjectRecord struct {
	called              bool
	delegate, defaulted func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace string, object *unstructured.Unstructured) (*unstructured.Unstructured, error)
}

func (r *createCachedObjectRecord) call(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace string, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	r.called = true
	delegate := r.delegate
	if delegate == nil {
		delegate = r.defaulted
	}
	return delegate(ctx, gvr, cluster, namespace, object)
}

type updateCachedObjectRecord struct {
	called              bool
	delegate, defaulted func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace string, object *unstructured.Unstructured) (*unstructured.Unstructured, error)
}

func (r *updateCachedObjectRecord) call(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace string, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	r.called = true
	delegate := r.delegate
	if delegate == nil {
		delegate = r.defaulted
	}
	return delegate(ctx, gvr, cluster, namespace, object)
}

type deleteCachedObjectRecord struct {
	called              bool
	delegate, defaulted func(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace, name string) error
}

func (r *deleteCachedObjectRecord) call(ctx context.Context, gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace, name string) error {
	r.called = true
	delegate := r.delegate
	if delegate == nil {
		delegate = r.defaulted
	}
	return delegate(ctx, gvr, cluster, namespace, name)
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

func newAPIExportWithShardAnnotation(name string) *apisv1alpha1.APIExport {
	apiExport := newAPIExport(name)
	apiExport.Annotations["kcp.dev/shard"] = "amber"
	return apiExport
}
