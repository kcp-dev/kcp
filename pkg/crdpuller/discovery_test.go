/*
Copyright 2021 The KCP Authors.

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

package crdpuller

import (
	"context"
	"fmt"
	"testing"

	openapi_v2 "github.com/google/gnostic/openapiv2"
	"github.com/google/go-cmp/cmp"

	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/openapi"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

func TestPuller(t *testing.T) {
	crdClient := fake.NewSimpleClientset()

	puller, err := newPuller(&fakeDiscovery{}, crdClient.ApiextensionsV1())
	if err != nil {
		t.Error(err)
	}

	_, err = puller.PullCRDs(context.Background(), "pods")
	if err != nil {
		t.Error(err)
	}

	wantActions := []k8stesting.Action{}
	wantActions = append(wantActions, k8stesting.NewGetAction(schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}, "", "pods.core"))
	if diff := cmp.Diff(wantActions, crdClient.Actions()); diff != "" {
		t.Fatalf("Unexpected actions: (-got,+want): %s", diff)
	}
}

type fakeDiscovery struct{}

func (fakeDiscovery) RESTClient() rest.Interface {
	return nil
}

func (fakeDiscovery) ServerGroups() (*metav1.APIGroupList, error) {
	return &metav1.APIGroupList{
		Groups: []metav1.APIGroup{
			{
				Name: "",
				Versions: []metav1.GroupVersionForDiscovery{
					{
						GroupVersion: "v1",
						Version:      "v1",
					},
				},
				PreferredVersion: metav1.GroupVersionForDiscovery{
					GroupVersion: "v1",
					Version:      "v1",
				},
			},
		},
	}, nil
}

func (fakeDiscovery) ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error) {
	if groupVersion == "v1" {
		return &metav1.APIResourceList{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{
					Name:       "pods",
					Namespaced: false,
					Kind:       "Pod",
				},
			},
		}, nil
	}
	if groupVersion == "metrics.k8s.io/v1beta1" {
		return &metav1.APIResourceList{
			GroupVersion: "metrics.k8s.io/v1beta1",
			APIResources: []metav1.APIResource{
				{
					Name:       "pods",
					Namespaced: false,
					Kind:       "Pod",
				},
			},
		}, nil
	}
	return nil, fmt.Errorf("groupVersion %s not found", groupVersion)
}

func (fakeDiscovery) ServerResources() ([]*metav1.APIResourceList, error) {
	return nil, nil
}

func (fakeDiscovery) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	return nil, nil, nil
}

func (fakeDiscovery) ServerPreferredResources() ([]*metav1.APIResourceList, error) {
	return []*metav1.APIResourceList{
		{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{
					Name:       "pods",
					Namespaced: false,
					Kind:       "Pod",
				},
			},
		},
	}, nil
}

func (fakeDiscovery) ServerPreferredNamespacedResources() ([]*metav1.APIResourceList, error) {
	return nil, nil
}

func (fakeDiscovery) ServerVersion() (*version.Info, error) {
	return nil, nil
}

func (d fakeDiscovery) OpenAPISchema() (*openapi_v2.Document, error) {
	return &openapi_v2.Document{}, nil
}

func (d fakeDiscovery) OpenAPIV3() openapi.Client {
	return openapi.NewClient(nil)
}
