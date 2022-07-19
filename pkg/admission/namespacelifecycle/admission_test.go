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

package namespacelifecycle

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	kubeadmission "k8s.io/apiserver/pkg/admission/initializer"
	"k8s.io/apiserver/pkg/endpoints/request"
	informers "k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

// newHandlerForTestWithClock returns a configured handler for testing.
func newHandlerForNamespaceLifeCycle(kubeClient clientset.Interface, lister fakeClusterWorkspaceLister) (*workspaceNamespaceLifecycle, error) {
	f := informers.NewSharedInformerFactory(kubeClient, 5*time.Minute)
	handler, err := newLifcycle()
	if err != nil {
		return nil, err
	}
	handler.workspaceLister = lister
	pluginInitializer := kubeadmission.New(kubeClient, f, nil, nil)
	pluginInitializer.Initialize(handler)
	err = admission.ValidateInitialization(handler)
	return handler, err
}

// newMockClientForTest creates a mock client that returns a client configured for the specified list of namespaces.
func newMockClientForTest(namespaces ...*corev1.Namespace) *fake.Clientset {
	mockClient := &fake.Clientset{}
	mockClient.AddReactor("list", "namespaces", func(action core.Action) (bool, runtime.Object, error) {
		namespaceList := &corev1.NamespaceList{
			ListMeta: metav1.ListMeta{
				ResourceVersion: fmt.Sprintf("%d", len(namespaces)),
			},
		}
		for _, namespace := range namespaces {
			namespaceList.Items = append(namespaceList.Items, *namespace)
		}
		return true, namespaceList, nil
	})
	return mockClient
}

type fakeClusterWorkspaceLister []*tenancyv1alpha1.ClusterWorkspace

func (l fakeClusterWorkspaceLister) List(selector labels.Selector) (ret []*tenancyv1alpha1.ClusterWorkspace, err error) {
	return l.ListWithContext(context.Background(), selector)
}

func (l fakeClusterWorkspaceLister) ListWithContext(ctx context.Context, selector labels.Selector) (ret []*tenancyv1alpha1.ClusterWorkspace, err error) {
	return l, nil
}

func (l fakeClusterWorkspaceLister) Get(name string) (*tenancyv1alpha1.ClusterWorkspace, error) {
	return l.GetWithContext(context.Background(), name)
}

func (l fakeClusterWorkspaceLister) GetWithContext(ctx context.Context, name string) (*tenancyv1alpha1.ClusterWorkspace, error) {
	for _, t := range l {
		if t.Name == name {
			return t, nil
		}
	}
	return nil, apierrors.NewNotFound(tenancyv1alpha1.Resource("clusterworkspacetype"), name)
}

func TestAdmit(t *testing.T) {
	now := metav1.Now()

	tests := []struct {
		name       string
		workspaces fakeClusterWorkspaceLister
		namespace  *corev1.Namespace
		wantErr    bool
	}{
		{
			name: "delete immortal namespace in workspace",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			},
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:org#$#test",
					},
				},
			},
			wantErr: true,
		},
		{
			name: "delete regular namespace in workspace",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "default",
				},
			},
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:org#$#test",
					},
				},
			},
			wantErr: true,
		},
		{
			name: "delete immortal namespace in deleting workspace",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "default",
				},
			},
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "root:org#$#test",
						DeletionTimestamp: &now,
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newMockClientForTest(tt.namespace)
			handler, err := newHandlerForNamespaceLifeCycle(client, tt.workspaces)
			if err != nil {
				t.Errorf("failed to initialize handler: %v", err)
			}
			a := admission.NewAttributesRecord(
				tt.namespace, tt.namespace, corev1.SchemeGroupVersion.WithKind("Namespace").GroupKind().WithVersion("version"), "", metav1.NamespaceDefault, corev1.Resource("namespaces").WithVersion("version"), "", admission.Delete, &metav1.DeleteOptions{}, false, nil)
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.New("root:org:test")})
			if err := handler.Admit(ctx, a, nil); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
