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
	"testing"

	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	apisv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
)

func TestValidate(t *testing.T) {
	scenarios := []struct {
		name           string
		attr           admission.Attributes
		clusterName    logicalcluster.Name
		initialObjects []runtime.Object
		wantErr        bool
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
			initialObjects: []runtime.Object{createBinding("foo1", "root:acme", []apisv1alpha1.BoundAPIResource{{Group: "acme.dev", Resource: "foo"}})},
			wantErr:        true,
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
			initialObjects: []runtime.Object{createBinding("foo1", "root:acme", []apisv1alpha1.BoundAPIResource{{Group: "acme.dev", Resource: "foo"}})},
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
			initialObjects: []runtime.Object{createBinding("foo1", "root:acme", []apisv1alpha1.BoundAPIResource{{Group: "acme.dev", Resource: "bar"}})},
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

			a := &crdNoOverlappingGVRAdmission{Handler: admission.NewHandler(admission.Create, admission.Update), apiBindingClusterLister: apisv1alpha1listers.NewAPIBindingClusterLister(indexer)}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: scenario.clusterName})
			if err := a.Validate(ctx, scenario.attr, nil); (err != nil) != scenario.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, scenario.wantErr)
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

func createBinding(name string, clusterName string, boundResources []apisv1alpha1.BoundAPIResource) *apisv1alpha1.APIBinding {
	return &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: clusterName,
			},
			Name: name,
		},
		Status: apisv1alpha1.APIBindingStatus{BoundResources: boundResources},
	}
}
