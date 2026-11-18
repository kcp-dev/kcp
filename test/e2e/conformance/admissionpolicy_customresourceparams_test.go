/*
Copyright 2026 The kcp Authors.

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

	"github.com/stretchr/testify/require"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"

	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

var sheriffsGVR = wildwestv1alpha1.SchemeGroupVersion.WithResource("sheriffs")

// sheriffParamKind is a paramKind which is not built-in, so that it is only known in
// the logical clusters which serve it, unlike e.g. a ConfigMap.
var sheriffParamKind = &admissionregistrationv1.ParamKind{
	APIVersion: wildwestv1alpha1.SchemeGroupVersion.String(),
	Kind:       "Sheriff",
}

// TestValidatingAdmissionPolicyCrossWorkspaceAPIBindingWithCustomResourceParams verifies that a policy in the
// workspace of an APIExport can use a custom resource of that workspace as its params.
func TestValidatingAdmissionPolicyCrossWorkspaceAPIBindingWithCustomResourceParams(t *testing.T) {
	t.Parallel()
	framework.Suite(t, framework.ParallelSuite)

	server := kcptesting.SharedKcpServer(t)

	const failedExpression = "failed expression: object.spec.intent != params.spec.intent"

	createPolicy := func(ctx context.Context, t *testing.T, f *crossShardCowboysExport) *admissionregistrationv1.ValidatingAdmissionPolicy {
		t.Helper()

		t.Logf("Installing validating admission policy with a custom resource paramKind into the source workspace")
		policy := &admissionregistrationv1.ValidatingAdmissionPolicy{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "policy-",
			},
			Spec: admissionregistrationv1.ValidatingAdmissionPolicySpec{
				FailurePolicy:    ptr.To(admissionregistrationv1.Fail),
				ParamKind:        sheriffParamKind,
				MatchConstraints: matchCowboys(),
				Validations: []admissionregistrationv1.Validation{{
					Expression: "object.spec.intent != params.spec.intent",
				}},
			},
		}
		policy, err := f.kubeClusterClient.Cluster(f.sourcePath).AdmissionregistrationV1().ValidatingAdmissionPolicies().Create(ctx, policy, metav1.CreateOptions{})
		require.NoError(t, err, "failed to create ValidatingAdmissionPolicy")
		return policy
	}

	createBinding := func(ctx context.Context, t *testing.T, f *crossShardCowboysExport, policy *admissionregistrationv1.ValidatingAdmissionPolicy, paramRef *admissionregistrationv1.ParamRef) {
		t.Helper()

		t.Logf("Installing validating admission policy binding with paramRef into the source workspace")
		binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "binding-",
			},
			Spec: admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
				PolicyName:        policy.Name,
				ParamRef:          paramRef,
				ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
			},
		}
		_, err := f.kubeClusterClient.Cluster(f.sourcePath).AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Create(ctx, binding, metav1.CreateOptions{})
		require.NoError(t, err, "failed to create ValidatingAdmissionPolicyBinding")
	}

	paramRefByName := &admissionregistrationv1.ParamRef{
		Name:                    "policy-params",
		ParameterNotFoundAction: ptr.To(admissionregistrationv1.DenyAction),
	}

	t.Run("CRD in the source workspace, referenced by name", func(t *testing.T) {
		t.Parallel()

		ctx, cancelFunc := context.WithCancel(context.Background())
		t.Cleanup(cancelFunc)

		f := newCrossShardCowboysExport(ctx, t, server)
		f.installSheriffsCRD(t)
		f.createSheriff(ctx, t, paramRefByName.Name, "bad", nil)

		createBinding(ctx, t, f, createPolicy(ctx, t, f), paramRefByName)

		f.requireCowboyEventuallyRejected(ctx, t, "bad", failedExpression)
		f.requireCowboyAdmitted(ctx, t, "good")
	})

	t.Run("CRD in the source workspace, referenced by selector", func(t *testing.T) {
		t.Parallel()

		ctx, cancelFunc := context.WithCancel(context.Background())
		t.Cleanup(cancelFunc)

		f := newCrossShardCowboysExport(ctx, t, server)
		f.installSheriffsCRD(t)
		forbidden := map[string]string{"policy-params": "forbidden"}
		f.createSheriff(ctx, t, "forbid-bad", "bad", forbidden)
		f.createSheriff(ctx, t, "forbid-worse", "worse", forbidden)
		// not selected, so its intent is not forbidden
		f.createSheriff(ctx, t, "not-selected", "good", nil)

		createBinding(ctx, t, f, createPolicy(ctx, t, f), &admissionregistrationv1.ParamRef{
			Selector:                &metav1.LabelSelector{MatchLabels: forbidden},
			ParameterNotFoundAction: ptr.To(admissionregistrationv1.DenyAction),
		})

		f.requireCowboyEventuallyRejected(ctx, t, "bad", failedExpression)
		f.requireCowboyEventuallyRejected(ctx, t, "worse", failedExpression)
		f.requireCowboyAdmitted(ctx, t, "good")
	})

	t.Run("bound API in the source workspace, referenced by name", func(t *testing.T) {
		t.Parallel()

		ctx, cancelFunc := context.WithCancel(context.Background())
		t.Cleanup(cancelFunc)

		f := newCrossShardCowboysExport(ctx, t, server)
		f.bindSheriffs(ctx, t, server)
		f.createSheriff(ctx, t, paramRefByName.Name, "bad", nil)

		createBinding(ctx, t, f, createPolicy(ctx, t, f), paramRefByName)

		f.requireCowboyEventuallyRejected(ctx, t, "bad", failedExpression)
		f.requireCowboyAdmitted(ctx, t, "good")
	})

	t.Run("CRD created in the source workspace after the policy", func(t *testing.T) {
		t.Parallel()

		ctx, cancelFunc := context.WithCancel(context.Background())
		t.Cleanup(cancelFunc)

		f := newCrossShardCowboysExport(ctx, t, server)

		createBinding(ctx, t, f, createPolicy(ctx, t, f), paramRefByName)

		t.Logf("Verifying that the policy fails closed in the target workspace as long as its paramKind does not exist")
		f.requireCowboyEventuallyRejected(ctx, t, "bad", "failed to find resource referenced by paramKind")

		f.installSheriffsCRD(t)
		f.createSheriff(ctx, t, paramRefByName.Name, "bad", nil)

		// A shard refreshes what it knows about the resources of a logical cluster on another shard
		// every 30s, like kube-apiserver does for its admission plugins. This takes that long at most.
		start := time.Now()
		f.requireCowboyEventuallyRejectedWithin(ctx, t, "bad", failedExpression, 2*wait.ForeverTestTimeout)
		t.Logf("The policy used its params %s after its paramKind was created", time.Since(start).Round(time.Second))
		f.requireCowboyAdmitted(ctx, t, "good")
	})
}

// TestMutatingAdmissionPolicyCrossWorkspaceAPIBindingWithCustomResourceParams verifies that a policy in the
// workspace of an APIExport can use a custom resource of that workspace as its params.
func TestMutatingAdmissionPolicyCrossWorkspaceAPIBindingWithCustomResourceParams(t *testing.T) {
	t.Parallel()
	framework.Suite(t, framework.ParallelSuite)

	server := kcptesting.SharedKcpServer(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	f := newCrossShardCowboysExport(ctx, t, server)
	f.installSheriffsCRD(t)
	f.createSheriff(ctx, t, "policy-params", "good", nil)

	t.Logf("Installing mutating admission policy with a custom resource paramKind into the source workspace")
	policy := &admissionregistrationv1.MutatingAdmissionPolicy{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "policy-",
		},
		Spec: admissionregistrationv1.MutatingAdmissionPolicySpec{
			FailurePolicy:      ptr.To(admissionregistrationv1.Fail),
			ReinvocationPolicy: admissionregistrationv1.NeverReinvocationPolicy,
			ParamKind:          sheriffParamKind,
			MatchConstraints:   matchCowboys(),
			Mutations: []admissionregistrationv1.Mutation{
				{
					PatchType: admissionregistrationv1.PatchTypeJSONPatch,
					JSONPatch: &admissionregistrationv1.JSONPatch{
						Expression: `[JSONPatch{op: "replace", path: "/spec/intent", value: params.spec.intent}]`,
					},
				},
			},
		},
	}
	policy, err := f.kubeClusterClient.Cluster(f.sourcePath).AdmissionregistrationV1().MutatingAdmissionPolicies().Create(ctx, policy, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create MutatingAdmissionPolicy")

	t.Logf("Installing mutating admission policy binding with paramRef into the source workspace")
	binding := &admissionregistrationv1.MutatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "binding-",
		},
		Spec: admissionregistrationv1.MutatingAdmissionPolicyBindingSpec{
			PolicyName: policy.Name,
			ParamRef: &admissionregistrationv1.ParamRef{
				Name:                    "policy-params",
				ParameterNotFoundAction: ptr.To(admissionregistrationv1.DenyAction),
			},
		},
	}
	_, err = f.kubeClusterClient.Cluster(f.sourcePath).AdmissionregistrationV1().MutatingAdmissionPolicyBindings().Create(ctx, binding, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create MutatingAdmissionPolicyBinding")

	t.Logf("Verifying that creating bad cowboy in target workspace is mutated by param-driven policy in source workspace")
	require.Eventually(t, func() bool {
		cowboy, err := f.createCowboy(ctx, "bad")
		if err != nil {
			t.Logf("Unexpected error when trying to create bad cowboy: %s", err)
			return false
		}
		if cowboy.Spec.Intent != "good" {
			t.Logf("Cowboy not yet mutated (intent=%q); the binding may not have propagated", cowboy.Spec.Intent)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, 1*time.Second)
}

// crossShardCowboysExport is an APIExport of cowboys in a source workspace, which is bound in a target workspace.
//
// The source workspace is hosted on the root shard and the target workspace on another shard, if there is one,
// so that the shard of the target workspace has to resolve and read the params of a policy on another shard.
// The source workspace requires groups, which the shards do not have, to verify that this neither needs
// the access of a user to the source workspace nor is subject to the restrictions of one.
type crossShardCowboysExport struct {
	sourcePath logicalcluster.Path
	targetPath logicalcluster.Path

	kcpClusterClient           kcpclientset.ClusterInterface
	kubeClusterClient          kcpkubernetesclientset.ClusterInterface
	cowboyClusterClient        wildwestclientset.ClusterInterface
	apiExtensionsClusterClient kcpapiextensionsclientset.ClusterInterface
	dynamicClusterClient       kcpdynamic.ClusterInterface
}

func newCrossShardCowboysExport(ctx context.Context, t *testing.T, server kcptestingserver.RunningServer) *crossShardCowboysExport {
	t.Helper()

	cfg := server.BaseConfig(t)
	f := &crossShardCowboysExport{}

	var err error
	f.kcpClusterClient, err = kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")
	f.kubeClusterClient, err = kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct client for server")
	f.cowboyClusterClient, err = wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct cowboy client for server")
	f.apiExtensionsClusterClient, err = kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client for server")
	f.dynamicClusterClient, err = kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client for server")

	targetShard := corev1alpha1.RootShard
	for _, shardName := range server.ShardNames() {
		if shardName != corev1alpha1.RootShard {
			targetShard = shardName
			break
		}
	}
	f.sourcePath, _ = framework.NewRootShardOrganizationFixture(t, server,
		kcptesting.PrivilegedWorkspaceOption(kcptesting.WithNamePrefix("source")),
		kcptesting.PrivilegedWorkspaceOption(kcptesting.WithRootShard()),
		framework.WithRequiredGroups("policy-params-e2e-required-group"),
	)
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	f.targetPath, _ = kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithShard(targetShard))

	t.Logf("Export cowboys from workspace %q", f.sourcePath)
	exportWildwestResource(ctx, t, f.kcpClusterClient, f.sourcePath, "cowboys", apiextensionsv1.CustomResourceDefinitionNames{
		Kind:     "Cowboy",
		ListKind: "CowboyList",
		Plural:   "cowboys",
		Singular: "cowboy",
	}, apiextensionsv1.NamespaceScoped)

	t.Logf("Bind cowboys in workspace %q", f.targetPath)
	bindWildwestResource(ctx, t, f.kcpClusterClient, f.targetPath, f.sourcePath, "cowboys")
	require.Eventually(t, func() bool {
		_, err := f.cowboyClusterClient.Cluster(f.targetPath).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "cowboys are not served in the target workspace")

	return f
}

// installSheriffsCRD makes sheriffs a resource which only the source workspace serves.
func (f *crossShardCowboysExport) installSheriffsCRD(t *testing.T) {
	t.Helper()

	t.Logf("Install the sheriffs CRD into workspace %q", f.sourcePath)
	wildwest.Create(t, f.sourcePath, f.apiExtensionsClusterClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: sheriffsGVR.Group, Resource: sheriffsGVR.Resource})
	f.waitForSheriffs(t)
}

// bindSheriffs makes sheriffs a resource which the source workspace serves through an APIBinding.
func (f *crossShardCowboysExport) bindSheriffs(ctx context.Context, t *testing.T, server kcptestingserver.RunningServer) {
	t.Helper()

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	sheriffsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	t.Logf("Export sheriffs from workspace %q", sheriffsPath)
	exportWildwestResource(ctx, t, f.kcpClusterClient, sheriffsPath, "sheriffs", apiextensionsv1.CustomResourceDefinitionNames{
		Kind:     "Sheriff",
		ListKind: "SheriffList",
		Plural:   "sheriffs",
		Singular: "sheriff",
	}, apiextensionsv1.ClusterScoped)

	t.Logf("Bind sheriffs in workspace %q", f.sourcePath)
	bindWildwestResource(ctx, t, f.kcpClusterClient, f.sourcePath, sheriffsPath, "sheriffs")
	f.waitForSheriffs(t)
}

func (f *crossShardCowboysExport) waitForSheriffs(t *testing.T) {
	t.Helper()

	require.Eventually(t, func() bool {
		_, err := f.dynamicClusterClient.Cluster(f.sourcePath).Resource(sheriffsGVR).List(t.Context(), metav1.ListOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "sheriffs are not served in the source workspace")
}

// createSheriff creates a Sheriff, which policies use as their params, in the source workspace.
func (f *crossShardCowboysExport) createSheriff(ctx context.Context, t *testing.T, name, intent string, labels map[string]string) {
	t.Helper()

	t.Logf("Creating Sheriff %q to use as policy parameter in the source workspace", name)
	sheriff := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": sheriffsGVR.GroupVersion().String(),
		"kind":       "Sheriff",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"intent": intent},
	}}
	sheriff.SetLabels(labels)
	_, err := f.dynamicClusterClient.Cluster(f.sourcePath).Resource(sheriffsGVR).Create(ctx, sheriff, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create param Sheriff")
}

// createCowboy creates a Cowboy in the target workspace.
func (f *crossShardCowboysExport) createCowboy(ctx context.Context, intent string) (*wildwestv1alpha1.Cowboy, error) {
	cowboy := &wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "cowboy-",
		},
		Spec: wildwestv1alpha1.CowboySpec{
			Intent: intent,
		},
	}
	return f.cowboyClusterClient.Cluster(f.targetPath).WildwestV1alpha1().Cowboys("default").Create(ctx, cowboy, metav1.CreateOptions{})
}

func (f *crossShardCowboysExport) requireCowboyEventuallyRejected(ctx context.Context, t *testing.T, intent, reason string) {
	t.Helper()
	f.requireCowboyEventuallyRejectedWithin(ctx, t, intent, reason, wait.ForeverTestTimeout)
}

func (f *crossShardCowboysExport) requireCowboyEventuallyRejectedWithin(ctx context.Context, t *testing.T, intent, reason string, timeout time.Duration) {
	t.Helper()

	t.Logf("Verifying that creating %s cowboy resource in target workspace is rejected by policy in source workspace", intent)
	require.Eventually(t, func() bool {
		_, err := f.createCowboy(ctx, intent)
		if errors.IsInvalid(err) || errors.IsForbidden(err) {
			if strings.Contains(err.Error(), reason) {
				return true
			}
		}
		t.Logf("Unexpected result when trying to create %s cowboy: %v", intent, err)
		return false
	}, timeout, 1*time.Second)
}

func (f *crossShardCowboysExport) requireCowboyAdmitted(ctx context.Context, t *testing.T, intent string) {
	t.Helper()

	t.Logf("Verifying that creating %s cowboy resource in target workspace succeeds", intent)
	_, err := f.createCowboy(ctx, intent)
	require.NoError(t, err)
}

// matchCowboys matches the creation and update of cowboys.
func matchCowboys() *admissionregistrationv1.MatchResources {
	return &admissionregistrationv1.MatchResources{
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
	}
}

// exportWildwestResource creates an APIExport, named like the resource, of a wildwest.dev resource with an intent.
func exportWildwestResource(ctx context.Context, t *testing.T, kcpClusterClient kcpclientset.ClusterInterface, exportPath logicalcluster.Path, resource string, names apiextensionsv1.CustomResourceDefinitionNames, scope apiextensionsv1.ResourceScope) {
	t.Helper()

	apiResourceSchema := &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("today.%s.%s", resource, wildwestv1alpha1.SchemeGroupVersion.Group),
		},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: wildwestv1alpha1.SchemeGroupVersion.Group,
			Names: names,
			Scope: scope,
			Versions: []apisv1alpha1.APIResourceVersion{
				{
					Name:    wildwestv1alpha1.SchemeGroupVersion.Version,
					Served:  true,
					Storage: true,
					Schema: runtime.RawExtension{
						Raw: []byte(`{
							"properties": {
								"apiVersion": {"type": "string"},
								"kind": {"type": "string"},
								"metadata": {"type": "object"},
								"spec": {
									"type": "object",
									"properties": {
										"intent": {"type": "string"}
									}
								}
							},
							"type": "object"
						}`),
					},
				},
			},
		},
	}
	_, err := kcpClusterClient.Cluster(exportPath).ApisV1alpha1().APIResourceSchemas().Create(ctx, apiResourceSchema, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create APIResourceSchema")

	apiExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: resource,
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   resource,
					Group:  wildwestv1alpha1.SchemeGroupVersion.Group,
					Schema: apiResourceSchema.Name,
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(exportPath).ApisV1alpha2().APIExports().Create(ctx, apiExport, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create APIExport")
}

// bindWildwestResource binds the APIExport created by exportWildwestResource.
func bindWildwestResource(ctx context.Context, t *testing.T, kcpClusterClient kcpclientset.ClusterInterface, bindingPath, exportPath logicalcluster.Path, resource string) {
	t.Helper()

	apiBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: resource,
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: exportPath.String(),
					Name: resource,
				},
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(bindingPath).ApisV1alpha2().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}
