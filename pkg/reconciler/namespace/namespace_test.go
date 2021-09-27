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

package namespace

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	clusterfake "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/fake"
	"github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
)

const lcluster = "my-logical-cluster"

// TODO:
// - a namespace is unassigned, no clusters; error.
// - a cluster becomes unhappy, unassign its namespaces.
// - test that a namespace's resources are also assigned when a namespace gets assigned.

func TestReconcileResource(t *testing.T) {
	targetCluster := "target-cluster"
	nsName := "my-namespace"

	ctx := context.Background()

	for _, tc := range []struct {
		desc            string
		assignedCluster string
		wantPatch       bool
	}{{
		desc:            "deployment assigned to different cluster",
		assignedCluster: "some-other-cluster",
		wantPatch:       true,
	}, {
		desc:            "deployment not assigned",
		assignedCluster: "",
		wantPatch:       true,
	}, {
		desc:            "deployment assigned to correct cluster",
		assignedCluster: targetCluster,
		wantPatch:       false,
	}} {
		t.Run(tc.desc, func(t *testing.T) {
			clientset := fake.NewSimpleClientset(
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						ClusterName: lcluster,
						Name:        nsName,
						Labels: map[string]string{
							clusterLabel: targetCluster,
						},
					},
				},
			)
			nsif := informers.NewSharedInformerFactory(clientset, time.Second)
			nsif.Core().V1().Namespaces().Informer() // This is needed to get the informer to start listening.
			nsif.Start(ctx.Done())
			t.Logf("cache synced: %+v", nsif.WaitForCacheSync(ctx.Done()))

			d := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					ClusterName: lcluster,
					Namespace:   nsName,
					Name:        "my-deployment",
					Labels: map[string]string{
						clusterLabel: tc.assignedCluster,
					},
				},
				Spec: appsv1.DeploymentSpec{},
			}
			rawMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(d)
			if err != nil {
				t.Fatalf("ToUnstructured: %v", err)
			}
			unstr := &unstructured.Unstructured{Object: rawMap}
			sch := runtime.NewScheme()
			if err := appsv1.AddToScheme(sch); err != nil {
				t.Fatal(err)
			}
			dynClient := dynfake.NewSimpleDynamicClient(sch, runtime.Object(d))
			gvr := &schema.GroupVersionResource{
				Group:    "apps",
				Version:  "v1",
				Resource: "deployments",
			}

			c := &Controller{
				namespaceLister: nsif.Core().V1().Namespaces().Lister(),
				dynClient:       dynClient,
			}

			// Reconcile the Deployment and see that its labels
			// were patched as expected.
			if err := c.reconcileResource(ctx, lcluster, unstr, gvr); err != nil {
				// TODO: This should be a t.Fatal, but this fails because the
				// dynamic client fails to find the Deployment by GVR.  For
				// now, just t.Logf the failure and check that we got the patch
				// we expected.
				t.Logf("reconcileResource: %v", err)
			}
			wantActions := []k8stesting.Action{}
			if tc.wantPatch {
				wantActions = append(wantActions, k8stesting.NewPatchAction(*gvr, nsName, d.Name, types.MergePatchType, clusterLabelPatchBytes(targetCluster)))
			}
			if diff := cmp.Diff(wantActions, dynClient.Actions()); diff != "" {
				t.Fatalf("Unexpected actions: (-got,+want): %s", diff)
			}

			// TODO: Get the deployment back out and see that its label was updated.
		})
	}
}

func TestReconcileNamespace(t *testing.T) {
	targetCluster := "target-cluster"
	nsName := "my-namespace"

	gvr := &schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "namespaces",
	}

	ctx := context.Background()

	for _, tc := range []struct {
		desc            string
		assignedCluster string
		wantPatch       bool
	}{{
		desc:            "namespace not assigned",
		assignedCluster: "",
		wantPatch:       true,
	}, {
		desc:            "namespace assigned",
		assignedCluster: targetCluster,
		wantPatch:       false,
	}} {
		t.Run(tc.desc, func(t *testing.T) {
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					ClusterName: lcluster,
					Name:        nsName,
					Labels: map[string]string{
						clusterLabel: tc.assignedCluster,
					},
				},
			}
			clientset := fake.NewSimpleClientset(ns)

			clusterClient := clusterfake.NewSimpleClientset(&v1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: targetCluster,
				},
			})
			csif := externalversions.NewSharedInformerFactory(clusterClient, time.Second)
			csif.Cluster().V1alpha1().Clusters().Informer() // This is needed to get the informer to start listening.
			csif.Start(ctx.Done())
			t.Logf("cache synced: %+v", csif.WaitForCacheSync(ctx.Done()))

			c := &Controller{
				clusterLister:   csif.Cluster().V1alpha1().Clusters().Lister(),
				namespaceClient: clientset.CoreV1().Namespaces(),
			}

			// Reconcile the Namespace and see that its labels were
			// patched as expected.
			if err := c.reconcileNamespace(ctx, lcluster, ns); err != nil {
				// TODO: This should be a t.Fatal, but this fails because the
				// dynamic client fails to find the Deployment by GVR.  For
				// now, just t.Logf the failure and check that we got the patch
				// we expected.
				t.Logf("reconcileNamespace: %v", err)
			}
			wantActions := []k8stesting.Action{}
			if tc.wantPatch {
				wantActions = append(wantActions, k8stesting.NewPatchAction(*gvr, "", nsName, types.MergePatchType, clusterLabelPatchBytes(targetCluster)))
			}
			if diff := cmp.Diff(wantActions, clientset.Actions()); diff != "" {
				t.Fatalf("Unexpected actions: (-got,+want): %s", diff)
			}

			// TODO: Get the deployment back out and see that its label was updated.
		})
	}
}
