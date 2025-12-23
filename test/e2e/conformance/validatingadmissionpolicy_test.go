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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/admission/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestValidatingAdmissionPolicyInWorkspace(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

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

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	ws1Path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	ws2Path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

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
			// DEBUG
			spew.Dump(kubeClusterClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{}))
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

func TestValidatingAdmissionPolicyCrossWorkspaceAPIBinding(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cfg := server.BaseConfig(t)

	scheme := runtime.NewScheme()
	err := admissionregistrationv1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add admission registration v1 scheme")
	err = v1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add admission v1 scheme")
	err = wildwestv1alpha1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add cowboy v1alpha1 to scheme")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	sourcePath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	targetPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct client for server")

	cowbyClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct cowboy client for server")

	t.Logf("Install a cowboys APIResourceSchema into workspace %q", sourcePath)

	apiResourceSchema := &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today.cowboys.wildwest.dev",
		},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: "wildwest.dev",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     "Cowboy",
				ListKind: "CowboyList",
				Plural:   "cowboys",
				Singular: "cowboy",
			},
			Scope: "Namespaced",
			Versions: []apisv1alpha1.APIResourceVersion{
				{
					Name:    "v1alpha1",
					Served:  true,
					Storage: true,
					Schema: runtime.RawExtension{
						Raw: []byte(`{
							"description": "Cowboy is part of the wild west",
							"properties": {
								"apiVersion": {"type": "string"},
								"kind": {"type": "string"},
								"metadata": {"type": "object"},
								"spec": {
									"type": "object",
									"properties": {
										"intent": {"type": "string"}
									}
								},
								"status": {
									"type": "object",
									"properties": {
										"result": {"type": "string"}
									}
								}
							},
							"type": "object"
						}`),
					},
					Subresources: apiextensionsv1.CustomResourceSubresources{
						Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
					},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(sourcePath).ApisV1alpha1().APIResourceSchemas().Create(ctx, apiResourceSchema, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIExport for it")
	cowboysAPIExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboybebop",
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "cowboys",
					Group:  "wildwest.dev",
					Schema: "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(sourcePath).ApisV1alpha2().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIBinding in workspace %q that points to the cowboybebop export", targetPath)
	apiBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: sourcePath.String(),
					Name: cowboysAPIExport.Name,
				},
			},
		},
	}

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(targetPath).ApisV1alpha2().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Ensure cowboys are served in target workspace")
	require.Eventually(t, func() bool {
		_, err := cowbyClusterClient.Cluster(targetPath).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Installing validating admission policy into the source workspace")
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
	policy, err = kubeClusterClient.Cluster(sourcePath).AdmissionregistrationV1().ValidatingAdmissionPolicies().Create(ctx, policy, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create ValidatingAdmissionPolicy")
	require.Eventually(t, func() bool {
		p, err := kubeClusterClient.Cluster(sourcePath).AdmissionregistrationV1().ValidatingAdmissionPolicies().Get(ctx, policy.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}

		return p.Generation == p.Status.ObservedGeneration && p.Status.TypeChecking != nil && len(p.Status.TypeChecking.ExpressionWarnings) == 0
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	newCowboy := func(intent string) *wildwestv1alpha1.Cowboy {
		return &wildwestv1alpha1.Cowboy{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "cowboy-",
			},
			Spec: wildwestv1alpha1.CowboySpec{
				Intent: intent,
			},
		}
	}

	t.Logf("Verifying that creating bad cowboy resource in target workspace succeeds before binding. Although, the policy is inactive without binding)")
	_, err = cowbyClusterClient.Cluster(targetPath).WildwestV1alpha1().Cowboys("default").Create(ctx, newCowboy("bad"), metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Installing validating admission policy binding into the source workspace")
	binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "binding-",
		},
		Spec: admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
			PolicyName:        policy.Name,
			ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
		},
	}

	_, err = kubeClusterClient.Cluster(sourcePath).AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Create(ctx, binding, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create ValidatingAdmissionPolicyBinding")

	t.Logf("Verifying that creating bad cowboy resource in target workspace is rejected by policy in source workspace")
	require.Eventually(t, func() bool {
		_, err := cowbyClusterClient.Cluster(targetPath).WildwestV1alpha1().Cowboys("default").Create(ctx, newCowboy("bad"), metav1.CreateOptions{})
		if err != nil {
			if errors.IsInvalid(err) {
				t.Logf("Error: %v", err)
				if strings.Contains(err.Error(), "failed expression: object.spec.intent != 'bad'") {
					return true
				}
			}
			t.Logf("Unexpected error when trying to create bad cowboy: %s", err)
		}
		return false
	}, wait.ForeverTestTimeout, 1*time.Second)

	t.Logf("Verifying that creating good cowboy resource in target workspace succeeds")
	_, err = cowbyClusterClient.Cluster(targetPath).WildwestV1alpha1().Cowboys("default").Create(ctx, newCowboy("good"), metav1.CreateOptions{})
	require.NoError(t, err)
}
