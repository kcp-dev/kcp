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

package crdnooverlappinggvr

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/core/v1alpha1"
)

var mockNow = func() metav1.Time {
	ts, _ := time.Parse(time.RFC3339, "2022-01-01T00:00:00Z")
	return metav1.Time{Time: ts}
}

func TestValidate(t *testing.T) {
	scenarios := []struct {
		name                         string
		attr                         admission.Attributes
		clusterName                  logicalcluster.Name
		initialObjects               []runtime.Object
		logicalClusterUpdateConflict bool
		wantErr                      bool
		wantAnnotation               string
	}{
		{
			name: "creating a conflicting CRD is forbidden",
			attr: createAttr(&apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: apiextensions.CustomResourceDefinitionSpec{
					Group: "acme.dev",
					Names: apiextensions.CustomResourceDefinitionNames{Plural: "foo"},
				},
			}),
			clusterName:    "root:acme",
			initialObjects: []runtime.Object{withBinding(createLogicalCluster("root:acme"), "foo1", []apisv1alpha1.BoundAPIResource{{Group: "acme.dev", Resource: "foo"}})},
			wantErr:        true,
			wantAnnotation: `{"foo.acme.dev":{"n":"foo1"}}`,
		},
		{
			name: "updating a CRD is always allowed",
			attr: updateAttr(&apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: apiextensions.CustomResourceDefinitionSpec{
					Group: "acme.dev",
					Names: apiextensions.CustomResourceDefinitionNames{Plural: "foo"},
				},
			}),
			clusterName:    "root:acme",
			initialObjects: []runtime.Object{withBinding(createLogicalCluster("root:acme"), "foo1", []apisv1alpha1.BoundAPIResource{{Group: "acme.dev", Resource: "foo"}})},
		},
		{
			name: "deleting a CRD is always allowed",
			attr: deleteAttr(&apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: apiextensions.CustomResourceDefinitionSpec{
					Group: "acme.dev",
					Names: apiextensions.CustomResourceDefinitionNames{Plural: "foo"},
				},
			}),
			clusterName:    "root:acme",
			initialObjects: []runtime.Object{withBinding(createLogicalCluster("root:acme"), "foo1", []apisv1alpha1.BoundAPIResource{{Group: "acme.dev", Resource: "foo"}})},
		},

		{
			name: "creating a conflicting CRD is allowed in the system:bound-crds workspaces",
			attr: createAttr(&apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: apiextensions.CustomResourceDefinitionSpec{
					Group: "acme.dev",
					Names: apiextensions.CustomResourceDefinitionNames{Plural: "foo"},
				},
			}),
			clusterName:    "system:bound-crds",
			initialObjects: []runtime.Object{},
		},

		{
			name: "creating a non-conflicting CRD is allowed",
			attr: createAttr(&apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: apiextensions.CustomResourceDefinitionSpec{
					Group: "acme.dev",
					Names: apiextensions.CustomResourceDefinitionNames{Plural: "foo"},
				},
			}),
			clusterName:    "root:acme",
			initialObjects: []runtime.Object{withBinding(createLogicalCluster("root:acme"), "foo1", []apisv1alpha1.BoundAPIResource{{Group: "acme.dev", Resource: "bar"}})},
			wantAnnotation: `{"bar.acme.dev":{"n":"foo1"},"foo.acme.dev":{"c":true,"e":"2022-01-01T00:00:00Z"}}`,
		},

		{
			name: "permanent logical cluster update conflict fails the request",
			attr: createAttr(&apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: apiextensions.CustomResourceDefinitionSpec{
					Group: "acme.dev",
					Names: apiextensions.CustomResourceDefinitionNames{Plural: "foo"},
				},
			}),
			clusterName:                  "root:acme",
			initialObjects:               []runtime.Object{withBinding(createLogicalCluster("root:acme"), "foo1", []apisv1alpha1.BoundAPIResource{{Group: "acme.dev", Resource: "bar"}})},
			logicalClusterUpdateConflict: true,
			wantErr:                      true,
		},

		{
			name: "fails without resource binding annotation on LogicalCluster",
			attr: createAttr(&apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: apiextensions.CustomResourceDefinitionSpec{
					Group: "acme.dev",
					Names: apiextensions.CustomResourceDefinitionNames{Plural: "foo"},
				},
			}),
			clusterName:    "root:acme",
			initialObjects: []runtime.Object{createLogicalCluster("root:acme")},
			wantErr:        true,
		},
		{
			name: "fails without LogicalCluster",
			attr: createAttr(&apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: apiextensions.CustomResourceDefinitionSpec{
					Group: "acme.dev",
					Names: apiextensions.CustomResourceDefinitionNames{Plural: "foo"},
				},
			}),
			clusterName:    "root:acme",
			initialObjects: []runtime.Object{},
			wantErr:        true,
		},
		{
			name: "passes without LogicalCluster in system logical cluster",
			attr: createAttr(&apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: apiextensions.CustomResourceDefinitionSpec{
					Group: "acme.dev",
					Names: apiextensions.CustomResourceDefinitionNames{Plural: "foo"},
				},
			}),
			clusterName:    "system:shard",
			initialObjects: []runtime.Object{},
		},

		{
			name: "succeeds with other CRD",
			attr: createAttr(&apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: apiextensions.CustomResourceDefinitionSpec{
					Group: "acme.dev",
					Names: apiextensions.CustomResourceDefinitionNames{Plural: "foo"},
				},
			}),
			clusterName:    "root:acme",
			initialObjects: []runtime.Object{withCRD(createLogicalCluster("root:acme"), schema.GroupResource{Group: "acme.dev", Resource: "bar"}, ptr.To(metav1.Time{Time: mockNow().Add(-time.Minute)}))},
			wantAnnotation: `{"bar.acme.dev":{"c":true,"e":"2021-12-31T23:59:00Z"},"foo.acme.dev":{"c":true,"e":"2022-01-01T00:00:00Z"}}`,
		},

		{
			name: "falls through with same CRD",
			attr: createAttr(&apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: apiextensions.CustomResourceDefinitionSpec{
					Group: "acme.dev",
					Names: apiextensions.CustomResourceDefinitionNames{Plural: "foo"},
				},
			}),
			clusterName:    "root:acme",
			initialObjects: []runtime.Object{withCRD(createLogicalCluster("root:acme"), schema.GroupResource{Group: "acme.dev", Resource: "foo"}, ptr.To(metav1.Time{Time: mockNow().Add(-time.Minute)}))},
			wantAnnotation: `{"foo.acme.dev":{"c":true,"e":"2022-01-01T00:00:00Z"}}`,
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			indexer := cache.NewIndexer(kcpcache.MetaClusterNamespaceKeyFunc, cache.Indexers{kcpcache.ClusterIndexName: kcpcache.ClusterIndexFunc})
			for _, obj := range scenario.initialObjects {
				if err := indexer.Add(obj); err != nil {
					t.Error(err)
				}
			}

			var updatedLogicalCluster *corev1alpha1.LogicalCluster
			a := &crdNoOverlappingGVRAdmission{
				Handler:              admission.NewHandler(admission.Create, admission.Update),
				logicalclusterLister: corev1alpha1listers.NewLogicalClusterClusterLister(indexer),
				updateLogicalCluster: func(ctx context.Context, lc *corev1alpha1.LogicalCluster, opts metav1.UpdateOptions) (*corev1alpha1.LogicalCluster, error) {
					updatedLogicalCluster = lc
					return lc, nil
				},
				now: mockNow,
			}

			if scenario.logicalClusterUpdateConflict {
				a.updateLogicalCluster = func(ctx context.Context, lc *corev1alpha1.LogicalCluster, opts metav1.UpdateOptions) (*corev1alpha1.LogicalCluster, error) {
					return nil, kerrors.NewConflict(schema.GroupResource{}, "conflict", nil)
				}
			}

			ctx := request.WithCluster(context.Background(), request.Cluster{Name: scenario.clusterName})
			if err := a.Validate(ctx, scenario.attr, nil); (err != nil) != scenario.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, scenario.wantErr)
			}

			if scenario.wantAnnotation != "" {
				if updatedLogicalCluster == nil {
					t.Fatal("expected LogicalCluster to be updated, got nil")
				}
				if got := updatedLogicalCluster.Annotations[apibinding.ResourceBindingsAnnotationKey]; got != scenario.wantAnnotation {
					t.Errorf("expected LogicalCluster annotation %q, got %q", scenario.wantAnnotation, got)
				}
			}
		})
	}
}

func createAttr(obj *apiextensions.CustomResourceDefinition) admission.Attributes {
	return admission.NewAttributesRecord(
		obj,
		nil,
		apiextensionsv1.Kind("CustomResourceDefinition").WithVersion("v1"),
		"",
		"test",
		apiextensionsv1.Resource("customresourcedefinitions").WithVersion("v1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func updateAttr(obj *apiextensions.CustomResourceDefinition) admission.Attributes {
	return admission.NewAttributesRecord(
		obj,
		obj,
		apiextensionsv1.Kind("CustomResourceDefinition").WithVersion("v1"),
		"",
		"test",
		apiextensionsv1.Resource("customresourcedefinitions").WithVersion("v1"),
		"",
		admission.Update,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func deleteAttr(obj *apiextensions.CustomResourceDefinition) admission.Attributes {
	return admission.NewAttributesRecord(
		obj,
		nil,
		apiextensionsv1.Kind("CustomResourceDefinition").WithVersion("v1"),
		"",
		"test",
		apiextensionsv1.Resource("customresourcedefinitions").WithVersion("v1"),
		"",
		admission.Delete,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func createLogicalCluster(clusterName string) *corev1alpha1.LogicalCluster {
	return &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: corev1alpha1.LogicalClusterName,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: clusterName,
			},
		},
	}
}

func withCRD(lc *corev1alpha1.LogicalCluster, gr schema.GroupResource, expiry *metav1.Time) *corev1alpha1.LogicalCluster {
	rbs := make(apibinding.ResourceBindingsAnnotation)
	if v := lc.Annotations[apibinding.ResourceBindingsAnnotationKey]; v != "" {
		if err := json.Unmarshal([]byte(v), &rbs); err != nil {
			panic(err)
		}
	}

	rbs[gr.String()] = apibinding.ExpirableLock{
		Lock:      apibinding.Lock{CRD: true},
		CRDExpiry: expiry,
	}

	bs, err := json.Marshal(rbs)
	if err != nil {
		panic(err)
	}
	lc.Annotations[apibinding.ResourceBindingsAnnotationKey] = string(bs)
	return lc
}

func withBinding(lc *corev1alpha1.LogicalCluster, binding string, boundResources []apisv1alpha1.BoundAPIResource) *corev1alpha1.LogicalCluster {
	rbs := make(apibinding.ResourceBindingsAnnotation)
	if v := lc.Annotations[apibinding.ResourceBindingsAnnotationKey]; v != "" {
		if err := json.Unmarshal([]byte(v), &rbs); err != nil {
			panic(err)
		}
	}

	for _, br := range boundResources {
		rbs[schema.GroupResource{Group: br.Group, Resource: br.Resource}.String()] = apibinding.ExpirableLock{
			Lock: apibinding.Lock{Name: binding},
		}
	}

	bs, err := json.Marshal(rbs)
	if err != nil {
		panic(err)
	}
	lc.Annotations[apibinding.ResourceBindingsAnnotationKey] = string(bs)
	return lc
}
