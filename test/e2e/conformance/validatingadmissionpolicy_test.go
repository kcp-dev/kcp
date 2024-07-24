/*
Copyright 2024 The KCP Authors.

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

package conformance

import (
	"context"
	"strings"
	"testing"
	"time"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/admission/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestValidatingAdmissionPolicyInWorkspace(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	// using known path to cert and key
	cfg := server.BaseConfig(t)

	scheme := runtime.NewScheme()
	err := admissionregistrationv1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add admission registration v1 scheme")
	err = v1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add admission v1 scheme")
	err = wildwestv1alpha1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add cowboy v1alpha1 to scheme")

	orgPath, _ := framework.NewOrganizationFixture(t, server)
	ws1Path, _ := framework.NewWorkspaceFixture(t, server, orgPath)
	ws2Path, _ := framework.NewWorkspaceFixture(t, server, orgPath)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct client for server")
	cowbyClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct cowboy client for server")
	apiExtensionsClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client for server")

	t.Logf("Install the Cowboy resources into logical clusters")
	for _, wsPath := range []logicalcluster.Path{ws1Path, ws2Path} {
		t.Logf("Bootstrapping Workspace CRDs in logical cluster %s", wsPath)
		crdClient := apiExtensionsClusterClient.ApiextensionsV1().CustomResourceDefinitions()
		wildwest.Create(t, wsPath, crdClient, metav1.GroupResource{Group: "wildwest.dev", Resource: "cowboys"})
	}

	t.Logf("Installing validating admission policy into the first workspace")
	policy := &admissionregistrationv1.ValidatingAdmissionPolicy{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "policy-",
		},
		Spec: admissionregistrationv1.ValidatingAdmissionPolicySpec{
			FailurePolicy: ptr.To(admissionregistrationv1.Fail),
			MatchConstraints: &admissionregistrationv1.MatchResources{
				ResourceRules: []admissionregistrationv1.NamedRuleWithOperations{
					{
						RuleWithOperations: admissionregistrationv1.RuleWithOperations{
							Operations: []admissionregistrationv1.OperationType{
								admissionregistrationv1.Create,
								admissionregistrationv1.Update,
							},
							Rule: admissionregistrationv1.Rule{
								APIGroups:   []string{wildwestv1alpha1.SchemeGroupVersion.Group},
								APIVersions: []string{wildwestv1alpha1.SchemeGroupVersion.Version},
								Resources:   []string{"cowboys"},
							},
						},
					},
				},
			},
			Validations: []admissionregistrationv1.Validation{{
				Expression: "object.spec.intent != 'bad'",
			}},
		},
	}
	policy, err = kubeClusterClient.Cluster(ws1Path).AdmissionregistrationV1().ValidatingAdmissionPolicies().Create(ctx, policy, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create ValidatingAdmissionPolicy")
	require.Eventually(t, func() bool {
		p, err := kubeClusterClient.Cluster(ws1Path).AdmissionregistrationV1().ValidatingAdmissionPolicies().Get(ctx, policy.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}

		// check if ValidatingAdmissionPolicy status has been updated
		// and no type checking errors came up
		return p.Generation == p.Status.ObservedGeneration && p.Status.TypeChecking != nil && len(p.Status.TypeChecking.ExpressionWarnings) == 0
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Installing validating admission policy binding into the first workspace")
	binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "binding-",
		},
		Spec: admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
			PolicyName:        policy.Name,
			ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
		},
	}

	_, err = kubeClusterClient.Cluster(ws1Path).AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Create(ctx, binding, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create ValidatingAdmissionPolicyBinding")

	badCowboy := wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "cowboy-",
		},
		Spec: wildwestv1alpha1.CowboySpec{
			Intent: "bad",
		},
	}

	goodCowboy := wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "cowboy-",
		},
		Spec: wildwestv1alpha1.CowboySpec{
			Intent: "good",
		},
	}

	t.Logf("Verifying that creating bad cowboy resource in first logical cluster is rejected")
	require.Eventually(t, func() bool {
		_, err := cowbyClusterClient.Cluster(ws1Path).WildwestV1alpha1().Cowboys("default").Create(ctx, &badCowboy, metav1.CreateOptions{})
		if err != nil {
			if errors.IsInvalid(err) {
				if strings.Contains(err.Error(), "failed expression: object.spec.intent != 'bad'") {
					return true
				}
			}
			t.Logf("Unexpected error when trying to create bad cowboy: %s", err)
		}
		return false
	}, wait.ForeverTestTimeout, 1*time.Second)

	t.Logf("Verifying that creating good cowboy resource in first logical cluster succeeds")
	_, err = cowbyClusterClient.Cluster(ws1Path).WildwestV1alpha1().Cowboys("default").Create(ctx, &goodCowboy, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Verifying that creating bad cowboy resource in second logical cluster succeeds (policy should not apply here)")
	_, err = cowbyClusterClient.Cluster(ws2Path).WildwestV1alpha1().Cowboys("default").Create(ctx, &badCowboy, metav1.CreateOptions{})
	require.NoError(t, err)
}
