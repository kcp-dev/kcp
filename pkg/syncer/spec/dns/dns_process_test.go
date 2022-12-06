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
	"github.com/stretchr/testify/require"

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
	endpointGVR       = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "endpoints"}
	networkPolicyGVR  = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}
)

func init() {
	scheme = runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
}

func TestDNSProcess(t *testing.T) {
	clusterName := logicalcluster.Name("root")
	syncTargetClusterName := logicalcluster.Name("targetclustername")
	syncTargetUID := types.UID("targetuid")
	syncTargetName := "targetname"

	locator := shared.NewNamespaceLocator(clusterName, syncTargetClusterName, syncTargetUID, syncTargetName, "")
	tenantID, err := shared.GetTenantID(locator)
	require.NoError(t, err)

	dnsID := shared.GetDNSID(clusterName, syncTargetUID, syncTargetName)
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
				MakeServiceAccount(dnsID, dnsns, tenantID),
				MakeRole(dnsID, dnsns, tenantID),
				MakeRoleBinding(dnsID, dnsns, tenantID),
				MakeService(dnsID, dnsns, tenantID),
				MakeDeployment(dnsID, dnsns, tenantID, "dnsimage"),
				endpoints(dnsID, dnsns, "8.8.8.8"),
				MakeNetworkPolicy(dnsID, dnsns, tenantID, &corev1.EndpointSubset{}),
			},
			expectReady:   true,
			expectActions: []clienttesting.Action{},
			initialized:   false,
			dnsImage:      "dnsimage",
		},
		"endpoint exist, DNS objects exists, updating with changes": {
			resources: []runtime.Object{
				MakeServiceAccount(dnsID, dnsns, tenantID),
				MakeRole(dnsID, dnsns, tenantID),
				MakeRoleBinding(dnsID, dnsns, tenantID),
				MakeService(dnsID, dnsns, tenantID),
				MakeDeployment(dnsID, dnsns, tenantID, "dnsimage"),
				endpoints(dnsID, dnsns, "8.8.8.8"),
				MakeNetworkPolicy(dnsID, dnsns, tenantID, &corev1.EndpointSubset{}),
			},
			expectReady: false,
			expectActions: []clienttesting.Action{
				clienttesting.NewUpdateAction(deploymentGVR, dnsns, MakeDeployment(dnsID, dnsns, tenantID, "newdnsimage")),
			},
			initialized: false,
			dnsImage:    "newdnsimage",
		},
		"endpoint does not exist, no DNS objects": {
			resources: []runtime.Object{
				endpoints("kubernetes", "default", "10.0.0.0"),
			},
			expectReady: false,
			expectActions: []clienttesting.Action{
				clienttesting.NewCreateAction(serviceAccountGVR, dnsns, MakeServiceAccount(dnsID, dnsns, tenantID)),
				clienttesting.NewCreateAction(roleGVR, dnsns, MakeRole(dnsID, dnsns, tenantID)),
				clienttesting.NewCreateAction(roleBindingGVR, dnsns, MakeRoleBinding(dnsID, dnsns, tenantID)),
				clienttesting.NewCreateAction(deploymentGVR, dnsns, MakeDeployment(dnsID, dnsns, tenantID, "dnsimage")),
				clienttesting.NewCreateAction(serviceGVR, dnsns, MakeService(dnsID, dnsns, tenantID)),
				clienttesting.NewGetAction(endpointGVR, "default", "kubernetes"),
				clienttesting.NewCreateAction(networkPolicyGVR, dnsns, MakeNetworkPolicy(dnsID, dnsns, tenantID, &corev1.EndpointSubset{
					Addresses: []corev1.EndpointAddress{{IP: "10.0.0.0"}},
				})),
			},
			initialized: true,
			dnsImage:    "dnsimage",
		},
		"endpoint does not exist, DNS objects exists, no updates": {
			resources: []runtime.Object{
				MakeServiceAccount(dnsID, dnsns, tenantID),
				MakeRole(dnsID, dnsns, tenantID),
				MakeRoleBinding(dnsID, dnsns, tenantID),
				MakeService(dnsID, dnsns, tenantID),
				MakeDeployment(dnsID, dnsns, tenantID, "dnsimage"),
				MakeNetworkPolicy(dnsID, dnsns, tenantID, &corev1.EndpointSubset{}),
			},
			expectReady:   false,
			expectActions: []clienttesting.Action{},
			initialized:   true,
			dnsImage:      "dnsimage",
		},
		"endpoint does not exist, DNS objects exists, updating with no changes": {
			resources: []runtime.Object{
				MakeServiceAccount(dnsID, dnsns, tenantID),
				MakeRole(dnsID, dnsns, tenantID),
				MakeRoleBinding(dnsID, dnsns, tenantID),
				MakeService(dnsID, dnsns, tenantID),
				MakeDeployment(dnsID, dnsns, tenantID, "dnsimage"),
				MakeNetworkPolicy(dnsID, dnsns, tenantID, &corev1.EndpointSubset{}),
			},
			expectReady:   false,
			expectActions: []clienttesting.Action{},
			initialized:   false,
			dnsImage:      "dnsimage",
		},
		"endpoint does not exist, DNS objects exists, updating with changes": {
			resources: []runtime.Object{
				MakeServiceAccount(dnsID, dnsns, tenantID),
				MakeRole(dnsID, dnsns, tenantID),
				MakeRoleBinding(dnsID, dnsns, tenantID),
				MakeService(dnsID, dnsns, tenantID),
				MakeDeployment(dnsID, dnsns, tenantID, "dnsimage"),
				MakeNetworkPolicy(dnsID, dnsns, tenantID, &corev1.EndpointSubset{}),
			},
			expectReady: false,
			expectActions: []clienttesting.Action{
				clienttesting.NewUpdateAction(deploymentGVR, dnsns, MakeDeployment(dnsID, dnsns, tenantID, "newdnsimage")),
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

			controller := NewDNSProcessor(kubeClient, informerFactory, syncTargetName, syncTargetUID,
				dnsns, tc.dnsImage)

			controller.initialized.Store(dnsID, tc.initialized)

			informerFactory.Start(ctx.Done())
			informerFactory.WaitForCacheSync(ctx.Done())

			kubeClient.ClearActions()

			ready, err := controller.EnsureDNSUpAndReady(ctx, tenantID, clusterName)
			assert.NoError(t, err)

			assert.Empty(t, cmp.Diff(tc.expectReady, ready))
			assert.Empty(t, cmp.Diff(tc.expectActions, kubeClient.Actions()))
		})
	}
}

func TestMultipleDNSInitialization(t *testing.T) {
	syncTargetClusterName := logicalcluster.Name("targetclustername")
	syncTargetUID := types.UID("targetuid")
	syncTargetName := "targetname"
	dnsns := "dnsns"

	clusterName1 := logicalcluster.Name("root1")
	clusterName2 := logicalcluster.Name("root2")

	locator1 := shared.NewNamespaceLocator(clusterName1, syncTargetClusterName, syncTargetUID, syncTargetName, "")
	locator2 := shared.NewNamespaceLocator(clusterName2, syncTargetClusterName, syncTargetUID, syncTargetName, "")

	tenantID1, _ := shared.GetTenantID(locator1)
	tenantID2, _ := shared.GetTenantID(locator2)

	dnsID1 := shared.GetDNSID(clusterName1, syncTargetUID, syncTargetName)
	dnsID2 := shared.GetDNSID(clusterName2, syncTargetUID, syncTargetName)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	kubeClient := kubefake.NewSimpleClientset(
		endpoints(dnsID1, dnsns, "8.8.8.8"),
		endpoints(dnsID2, dnsns, "8.8.8.9"))

	// informerFactory to watch some DNS-related resources in the dns namespace
	informerFactory := informers.NewSharedInformerFactoryWithOptions(kubeClient, time.Hour, informers.WithNamespace(dnsns))

	controller := NewDNSProcessor(kubeClient, informerFactory, syncTargetName, syncTargetUID,
		dnsns, "animage")

	informerFactory.Start(ctx.Done())
	informerFactory.WaitForCacheSync(ctx.Done())

	ready, err := controller.EnsureDNSUpAndReady(ctx, tenantID1, clusterName1)
	assert.NoError(t, err)
	assert.True(t, ready)
	init1, _ := controller.initialized.Load(dnsID1)
	assert.True(t, init1.(bool))
	init2, _ := controller.initialized.Load(dnsID2)
	assert.Nil(t, init2)

	ready, err = controller.EnsureDNSUpAndReady(ctx, tenantID2, clusterName2)
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
			{
				Addresses: []corev1.EndpointAddress{
					{
						IP: ip,
					}},
			},
		}
	}
	return endpoint
}
