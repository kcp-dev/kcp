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

package dns

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/assert"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

var (
	scheme            *runtime.Scheme
	serviceAccountGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "serviceaccounts"}
	roleGVR           = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}
	roleBindingGVR    = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}
	serviceGVR        = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}
	deploymentGVR     = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
)

func init() {
	scheme = runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
}

func TestDNSProcess(t *testing.T) {
	workspace := logicalcluster.New("root")
	syncTargetUID := types.UID("targetuid")
	syncTargetName := "targetname"
	dnsID := shared.GetDNSID(workspace, syncTargetUID, syncTargetName)
	dnsns := "dnsns"

	tests := map[string]struct {
		resources     []runtime.Object
		initialized   bool
		expectReady   bool
		expectActions []clienttesting.Action
		dnsImage      string
	}{
		"endpoint is ready": {
			resources: []runtime.Object{
				endpoints(dnsID, dnsns, "8.8.8.8"),
			},
			expectReady:   true,
			expectActions: []clienttesting.Action{},
			initialized:   true,
			dnsImage:      "dnsimage",
		},
		"endpoint exists but not ready": {
			resources: []runtime.Object{
				endpoints(dnsID, dnsns, ""),
			},
			expectReady:   false,
			expectActions: []clienttesting.Action{},
			initialized:   true,
			dnsImage:      "dnsimage",
		},
		"endpoint exist, DNS objects exists, updating with no changes": {
			resources: []runtime.Object{
				MakeServiceAccount(dnsID, dnsns),
				MakeRole(dnsID, dnsns),
				MakeRoleBinding(dnsID, dnsns),
				MakeService(dnsID, dnsns),
				MakeDeployment(dnsID, dnsns, "dnsimage"),
				endpoints(dnsID, dnsns, "8.8.8.8"),
			},
			expectReady:   true,
			expectActions: []clienttesting.Action{},
			initialized:   false,
			dnsImage:      "dnsimage",
		},
		"endpoint exist, DNS objects exists, updating with changes": {
			resources: []runtime.Object{
				MakeServiceAccount(dnsID, dnsns),
				MakeRole(dnsID, dnsns),
				MakeRoleBinding(dnsID, dnsns),
				MakeService(dnsID, dnsns),
				MakeDeployment(dnsID, dnsns, "dnsimage"),
				endpoints(dnsID, dnsns, "8.8.8.8"),
			},
			expectReady: false,
			expectActions: []clienttesting.Action{
				clienttesting.NewUpdateAction(deploymentGVR, dnsns, MakeDeployment(dnsID, dnsns, "newdnsimage")),
			},
			initialized: false,
			dnsImage:    "newdnsimage",
		},
		"endpoint does not exist, no DNS objects": {
			resources:   []runtime.Object{},
			expectReady: false,
			expectActions: []clienttesting.Action{
				clienttesting.NewCreateAction(serviceAccountGVR, dnsns, MakeServiceAccount(dnsID, dnsns)),
				clienttesting.NewCreateAction(roleGVR, dnsns, MakeRole(dnsID, dnsns)),
				clienttesting.NewCreateAction(roleBindingGVR, dnsns, MakeRoleBinding(dnsID, dnsns)),
				clienttesting.NewCreateAction(deploymentGVR, dnsns, MakeDeployment(dnsID, dnsns, "dnsimage")),
				clienttesting.NewCreateAction(serviceGVR, dnsns, MakeService(dnsID, dnsns)),
			},
			initialized: true,
			dnsImage:    "dnsimage",
		},
		"endpoint does not exist, DNS objects exists, no updates": {
			resources: []runtime.Object{
				MakeServiceAccount(dnsID, dnsns),
				MakeRole(dnsID, dnsns),
				MakeRoleBinding(dnsID, dnsns),
				MakeService(dnsID, dnsns),
				MakeDeployment(dnsID, dnsns, "dnsimage"),
			},
			expectReady:   false,
			expectActions: []clienttesting.Action{},
			initialized:   true,
			dnsImage:      "dnsimage",
		},
		"endpoint does not exist, DNS objects exists, updating with no changes": {
			resources: []runtime.Object{
				MakeServiceAccount(dnsID, dnsns),
				MakeRole(dnsID, dnsns),
				MakeRoleBinding(dnsID, dnsns),
				MakeService(dnsID, dnsns),
				MakeDeployment(dnsID, dnsns, "dnsimage"),
			},
			expectReady:   false,
			expectActions: []clienttesting.Action{},
			initialized:   false,
			dnsImage:      "dnsimage",
		},
		"endpoint does not exist, DNS objects exists, updating with changes": {
			resources: []runtime.Object{
				MakeServiceAccount(dnsID, dnsns),
				MakeRole(dnsID, dnsns),
				MakeRoleBinding(dnsID, dnsns),
				MakeService(dnsID, dnsns),
				MakeDeployment(dnsID, dnsns, "dnsimage"),
			},
			expectReady: false,
			expectActions: []clienttesting.Action{
				clienttesting.NewUpdateAction(deploymentGVR, dnsns, MakeDeployment(dnsID, dnsns, "newdnsimage")),
			},
			initialized: false,
			dnsImage:    "newdnsimage",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			kubeClient := kubefake.NewSimpleClientset(tc.resources...)

			// informerFactory to watch some DNS-related resources in the dns namespace
			informerFactory := informers.NewSharedInformerFactoryWithOptions(kubeClient, time.Hour, informers.WithNamespace(dnsns))
			serviceAccountLister := informerFactory.Core().V1().ServiceAccounts().Lister()
			roleLister := informerFactory.Rbac().V1().Roles().Lister()
			roleBindingLister := informerFactory.Rbac().V1().RoleBindings().Lister()
			deploymentLister := informerFactory.Apps().V1().Deployments().Lister()
			serviceLister := informerFactory.Core().V1().Services().Lister()
			endpointLister := informerFactory.Core().V1().Endpoints().Lister()

			controller := NewDNSProcessor(kubeClient, serviceAccountLister, roleLister, roleBindingLister,
				deploymentLister, serviceLister, endpointLister, syncTargetName, syncTargetUID,
				dnsns, tc.dnsImage)

			controller.initialized.Store(dnsID, tc.initialized)

			informerFactory.Start(ctx.Done())
			informerFactory.WaitForCacheSync(ctx.Done())

			kubeClient.ClearActions()

			ready, err := controller.EnsureDNSUpAndReady(ctx, workspace)
			assert.NoError(t, err)

			assert.Empty(t, cmp.Diff(tc.expectReady, ready))
			assert.Empty(t, cmp.Diff(tc.expectActions, kubeClient.Actions()))
		})
	}
}

func TestMultipleDNSInitialization(t *testing.T) {
	syncTargetUID := types.UID("targetuid")
	syncTargetName := "targetname"
	dnsns := "dnsns"

	workspace1 := logicalcluster.New("root1")
	workspace2 := logicalcluster.New("root2")

	dnsID1 := shared.GetDNSID(workspace1, syncTargetUID, syncTargetName)
	dnsID2 := shared.GetDNSID(workspace2, syncTargetUID, syncTargetName)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	kubeClient := kubefake.NewSimpleClientset(
		endpoints(dnsID1, dnsns, "8.8.8.8"),
		endpoints(dnsID2, dnsns, "8.8.8.9"))

	// informerFactory to watch some DNS-related resources in the dns namespace
	informerFactory := informers.NewSharedInformerFactoryWithOptions(kubeClient, time.Hour, informers.WithNamespace(dnsns))
	serviceAccountLister := informerFactory.Core().V1().ServiceAccounts().Lister()
	roleLister := informerFactory.Rbac().V1().Roles().Lister()
	roleBindingLister := informerFactory.Rbac().V1().RoleBindings().Lister()
	deploymentLister := informerFactory.Apps().V1().Deployments().Lister()
	serviceLister := informerFactory.Core().V1().Services().Lister()
	endpointLister := informerFactory.Core().V1().Endpoints().Lister()

	controller := NewDNSProcessor(kubeClient, serviceAccountLister, roleLister, roleBindingLister,
		deploymentLister, serviceLister, endpointLister, syncTargetName, syncTargetUID,
		dnsns, "animage")

	informerFactory.Start(ctx.Done())
	informerFactory.WaitForCacheSync(ctx.Done())

	ready, err := controller.EnsureDNSUpAndReady(ctx, workspace1)
	assert.NoError(t, err)
	assert.True(t, ready)
	init1, _ := controller.initialized.Load(dnsID1)
	assert.True(t, init1.(bool))
	init2, _ := controller.initialized.Load(dnsID2)
	assert.Nil(t, init2)

	ready, err = controller.EnsureDNSUpAndReady(ctx, workspace2)
	assert.NoError(t, err)
	assert.True(t, ready)
	init1, _ = controller.initialized.Load(dnsID1)
	assert.True(t, init1.(bool))
	init2, _ = controller.initialized.Load(dnsID2)
	assert.True(t, init2.(bool))
}

func endpoints(name, namespace, ip string) *corev1.Endpoints {
	endpoint := &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	if ip != "" {
		endpoint.Subsets = []corev1.EndpointSubset{
			{Addresses: []corev1.EndpointAddress{
				{
					IP: ip,
				}}},
		}
	}
	return endpoint
}
