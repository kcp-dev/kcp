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

package scenarios

import (
	"context"
	"embed"
	"fmt"
	"io"
	"time"

	"sigs.k8s.io/yaml"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"

	pluginhelpers "github.com/kcp-dev/cli/pkg/helpers"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
)

//go:embed resources/api-provider
var apiProviderResources embed.FS

const (
	pollIntervalAppear  = 100 * time.Millisecond
	pollIntervalReady   = 500 * time.Millisecond
	pollIntervalCleanup = 2 * time.Second
	logThrottleInterval = 10 * time.Second
)

type apiProviderScenario struct{}

func (s *apiProviderScenario) Name() string { return "api-provider" }

// EnterPath returns the absolute workspace path that --enter should switch to,
// or "" if the scenario does not support it.
func (s *apiProviderScenario) EnterPath(state map[string]string) string {
	return state[stateKeyConsumerPath]
}

func (s *apiProviderScenario) Steps(prefix string) []Step {
	orgName := prefix + orgSuffix
	providerName := prefix + providerSuffix
	consumerName := prefix + consumerSuffix

	return []Step{
		{
			Description:        fmt.Sprintf("Creating organization workspace %q", orgName),
			CleanupDescription: fmt.Sprintf("Deleting organization workspace %q (cascades provider and consumer)", orgName),
			Execute: func(ctx context.Context, execCtx ExecutionContext) error {
				return createWorkspaceStep(ctx, execCtx, logicalcluster.NewPath("root"), orgName, &tenancyv1alpha1.WorkspaceTypeReference{Name: "organization", Path: "root"}, map[string]string{quickstartLabel: "true", quickstartPrefixLabel: prefix}, stateKeyOrgPath)
			},
			Cleanup: func(ctx context.Context, execCtx ExecutionContext) error {
				rootPath := logicalcluster.NewPath("root")
				err := execCtx.KCPClusterClient.Cluster(rootPath).TenancyV1alpha1().Workspaces().
					Delete(ctx, orgName, metav1.DeleteOptions{})
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}

				fmt.Fprintf(execCtx.Out, "  Waiting for workspace %q and all children to finish terminating...\n", orgName)
				var lastLog time.Time
				if err := wait.PollUntilContextCancel(ctx, pollIntervalCleanup, true,
					func(ctx context.Context) (bool, error) {
						ws, err := execCtx.KCPClusterClient.Cluster(rootPath).TenancyV1alpha1().Workspaces().
							Get(ctx, orgName, metav1.GetOptions{})
						if apierrors.IsNotFound(err) {
							return true, nil
						}
						if err != nil {
							return false, err
						}

						if time.Since(lastLog) >= logThrottleInterval {
							if ws.DeletionTimestamp != nil {
								fmt.Fprintf(execCtx.Out, "  Still terminating (cascading deletion in progress)...\n")
							} else {
								fmt.Fprintf(execCtx.Out, "  Deletion pending (phase: %s)...\n", ws.Status.Phase)
							}
							lastLog = time.Now()
						}

						return false, nil
					},
				); err != nil {
					return fmt.Errorf("timed out waiting for workspace %q to terminate: %w", orgName, err)
				}

				return nil
			},
		},
		{
			Description: fmt.Sprintf("Creating service provider workspace %q", providerName),
			Execute: func(ctx context.Context, execCtx ExecutionContext) error {
				return createWorkspaceStep(ctx, execCtx, logicalcluster.NewPath(execCtx.State[stateKeyOrgPath]), providerName, &tenancyv1alpha1.WorkspaceTypeReference{Name: "universal", Path: "root"}, map[string]string{quickstartLabel: "true", quickstartPrefixLabel: prefix}, stateKeyProviderPath)
			},
		},
		{
			Description: "Applying APIResourceSchema",
			Execute: func(ctx context.Context, execCtx ExecutionContext) error {
				providerPath := logicalcluster.NewPath(execCtx.State[stateKeyProviderPath])
				return applyEmbeddedResource(execCtx.Out, apiProviderResources, "resources/api-provider/apiresourceschema.yaml", "APIResourceSchema", func(data []byte) (string, error) {
					obj := &apisv1alpha1.APIResourceSchema{}
					if err := yaml.Unmarshal(data, obj); err != nil {
						return "", err
					}
					_, err := execCtx.KCPClusterClient.Cluster(providerPath).ApisV1alpha1().APIResourceSchemas().Create(ctx, obj, metav1.CreateOptions{})
					if err != nil {
						return obj.Name, fmt.Errorf("creating APIResourceSchema: %w", err)
					}

					return obj.Name, nil
				})
			},
		},
		{
			Description: "Applying APIExport",
			Execute: func(ctx context.Context, execCtx ExecutionContext) error {
				providerPath := logicalcluster.NewPath(execCtx.State[stateKeyProviderPath])
				return applyEmbeddedResource(execCtx.Out, apiProviderResources, "resources/api-provider/apiexport.yaml", "APIExport", func(data []byte) (string, error) {
					obj := &apisv1alpha2.APIExport{}
					if err := yaml.Unmarshal(data, obj); err != nil {
						return "", err
					}
					_, err := execCtx.KCPClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(ctx, obj, metav1.CreateOptions{})
					if err != nil {
						return obj.Name, fmt.Errorf("creating APIExport: %w", err)
					}

					return obj.Name, nil
				})
			},
		},
		{
			Description: fmt.Sprintf("Creating consumer workspace %q", consumerName),
			Execute: func(ctx context.Context, execCtx ExecutionContext) error {
				return createWorkspaceStep(ctx, execCtx, logicalcluster.NewPath(execCtx.State[stateKeyOrgPath]), consumerName, &tenancyv1alpha1.WorkspaceTypeReference{Name: "universal", Path: "root"}, map[string]string{quickstartLabel: "true", quickstartPrefixLabel: prefix}, stateKeyConsumerPath)
			},
		},
		{
			Description: "Creating APIBinding in consumer workspace",
			Execute: func(ctx context.Context, execCtx ExecutionContext) error {
				consumerPath := logicalcluster.NewPath(execCtx.State[stateKeyConsumerPath])
				providerPath := execCtx.State[stateKeyProviderPath]
				return createAPIBindingAndWait(ctx, execCtx.KCPClusterClient, execCtx.Out, consumerPath,
					"cowboys", providerPath, "cowboys")
			},
		},
	}
}

func (s *apiProviderScenario) Samples(prefix string) []Step {
	consumerName := prefix + consumerSuffix
	return []Step{
		{
			Description: fmt.Sprintf("Applying sample Cowboy resource in %q", consumerName),
			Execute: func(ctx context.Context, execCtx ExecutionContext) error {
				consumerPath := logicalcluster.NewPath(execCtx.State[stateKeyConsumerPath])
				if err := applySampleCowboy(ctx, execCtx, consumerPath); err != nil {
					return err
				}
				execCtx.State[stateKeyWithSamples] = "true"
				return nil
			},
		},
	}
}

func (s *apiProviderScenario) PrintSummary(out io.Writer, prefix string, state map[string]string) error {
	orgName := prefix + orgSuffix
	providerName := prefix + providerSuffix
	consumerName := prefix + consumerSuffix

	var tryItOut string
	if state[stateKeyWithSamples] == "true" {
		tryItOut = fmt.Sprintf(`  Try it out:
    kubectl ws :%s
    kubectl get cowboys`, state[stateKeyConsumerPath])
	} else {
		tryItOut = fmt.Sprintf(`  Try it out:
    kubectl ws :%s
    kubectl apply -f - <<EOF
    apiVersion: wildwest.dev/v1alpha1
    kind: Cowboy
    metadata:
      name: john-wayne
      namespace: default
    spec:
      intent: good
    EOF
    kubectl get cowboys`, state[stateKeyConsumerPath])
	}

	_, err := fmt.Fprintf(out, `
Quickstart complete! Here's what was created:

  Workspace hierarchy:
    root
    +-- %s (organization)
        +-- %s (universal) - service provider
        |   +-- APIResourceSchema: today.cowboys.wildwest.dev
        |   +-- APIExport: cowboys
        +-- %s (universal) - API consumer
            +-- APIBinding: cowboys -> %s:cowboys

%s

  Cleanup:
    kubectl kcp quickstart --cleanup --name-prefix %s
`,
		orgName,
		providerName,
		consumerName,
		state[stateKeyProviderPath],
		tryItOut,
		prefix,
	)
	return err
}

func createWorkspaceAndWait(ctx context.Context, client kcpclientset.ClusterInterface, parentPath logicalcluster.Path, name string, wsType *tenancyv1alpha1.WorkspaceTypeReference, labels map[string]string) (logicalcluster.Path, bool, error) {
	ws := &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: tenancyv1alpha1.WorkspaceSpec{
			Type: wsType,
		},
	}

	alreadyExisted := false
	ws, err := client.Cluster(parentPath).TenancyV1alpha1().Workspaces().
		Create(ctx, ws, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		alreadyExisted = true
		ws, err = client.Cluster(parentPath).TenancyV1alpha1().Workspaces().
			Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return logicalcluster.Path{}, false, fmt.Errorf("creating workspace %q: %w", name, err)
	}

	if err := wait.PollUntilContextCancel(ctx, pollIntervalAppear, true,
		func(ctx context.Context) (bool, error) {
			_, err := client.Cluster(parentPath).TenancyV1alpha1().Workspaces().
				Get(ctx, ws.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return err == nil, err
		},
	); err != nil {
		return logicalcluster.Path{}, false, fmt.Errorf("timed out waiting for workspace %q to appear: %w", name, err)
	}

	if ws.Status.Phase != corev1alpha1.LogicalClusterPhaseReady {
		if err := wait.PollUntilContextCancel(ctx, pollIntervalReady, true,
			func(ctx context.Context) (bool, error) {
				ws, err = client.Cluster(parentPath).TenancyV1alpha1().Workspaces().
					Get(ctx, ws.Name, metav1.GetOptions{})
				if err != nil {
					return false, err
				}
				return ws.Status.Phase == corev1alpha1.LogicalClusterPhaseReady, nil
			},
		); err != nil {
			return logicalcluster.Path{}, false, fmt.Errorf("timed out waiting for workspace %q to become ready (current phase: %s): %w",
				name, ws.Status.Phase, err)
		}
	}

	if ws.Spec.URL == "" {
		return logicalcluster.Path{}, false, fmt.Errorf("workspace %q is Ready but Spec.URL is empty", name)
	}
	_, childPath, err := pluginhelpers.ParseClusterURL(ws.Spec.URL)
	if err != nil {
		return logicalcluster.Path{}, false, fmt.Errorf("workspace %q has invalid Spec.URL %q: %w", name, ws.Spec.URL, err)
	}

	return childPath, alreadyExisted, nil
}

func createAPIBindingAndWait(
	ctx context.Context,
	client kcpclientset.ClusterInterface,
	out io.Writer,
	workspacePath logicalcluster.Path,
	bindingName string,
	exportPath string,
	exportName string,
) error {
	binding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: bindingName},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: exportPath,
					Name: exportName,
				},
			},
		},
	}

	alreadyExisted := false
	_, err := client.Cluster(workspacePath).ApisV1alpha2().APIBindings().
		Create(ctx, binding, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		alreadyExisted = true
	} else if err != nil {
		return fmt.Errorf("creating APIBinding %q: %w", bindingName, err)
	}

	if err := wait.PollUntilContextCancel(ctx, pollIntervalReady, true,
		func(ctx context.Context) (bool, error) {
			b, err := client.Cluster(workspacePath).ApisV1alpha2().APIBindings().
				Get(ctx, bindingName, metav1.GetOptions{})
			if err != nil {
				return false, err
			}

			return b.Status.Phase == apisv1alpha2.APIBindingPhaseBound, nil
		},
	); err != nil {
		return fmt.Errorf("timed out waiting for APIBinding %q to become bound: %w", bindingName, err)
	}

	if alreadyExisted {
		fmt.Fprintf(out, "  APIBinding %q already exists\n", bindingName)
	} else {
		fmt.Fprintf(out, "  APIBinding %q created and bound\n", bindingName)
	}

	return nil
}

func applyEmbeddedResource(out io.Writer, efs embed.FS, filename string, kind string, create func(data []byte) (string, error)) error {
	data, err := efs.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("reading embedded resource %s: %w", filename, err)
	}

	name, err := create(data)
	if apierrors.IsAlreadyExists(err) {
		fmt.Fprintf(out, "  %s %q already exists\n", kind, name)
		return nil
	}
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "  %s %q created\n", kind, name)
	return nil
}

var cowboyGVR = schema.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha1", Resource: "cowboys"}

func applySampleCowboy(ctx context.Context, execCtx ExecutionContext, workspacePath logicalcluster.Path) error {
	data, err := apiProviderResources.ReadFile("resources/api-provider/sample-cowboy.yaml")
	if err != nil {
		return fmt.Errorf("reading sample-cowboy.yaml: %w", err)
	}

	raw := map[string]any{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing sample-cowboy.yaml: %w", err)
	}
	obj := &unstructured.Unstructured{Object: raw}

	_, err = execCtx.DynamicClient.Cluster(workspacePath).Resource(cowboyGVR).
		Namespace(obj.GetNamespace()).
		Create(ctx, obj, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		fmt.Fprintf(execCtx.Out, "  Cowboy %q already exists\n", obj.GetName())
		return nil
	}
	if err != nil {
		return fmt.Errorf("creating Cowboy %q: %w", obj.GetName(), err)
	}

	fmt.Fprintf(execCtx.Out, "  Cowboy %q created\n", obj.GetName())
	return nil
}

func createWorkspaceStep(ctx context.Context, execCtx ExecutionContext, parentPath logicalcluster.Path, name string, wsType *tenancyv1alpha1.WorkspaceTypeReference, labels map[string]string, stateKey string) error {
	path, existed, err := createWorkspaceAndWait(ctx, execCtx.KCPClusterClient, parentPath, name, wsType, labels)
	if err != nil {
		return err
	}

	execCtx.State[stateKey] = path.String()
	if existed {
		fmt.Fprintf(execCtx.Out, "  Workspace %q already exists at %s\n", name, path)
	} else {
		fmt.Fprintf(execCtx.Out, "  Workspace %q created and ready at %s\n", name, path)
	}

	return nil
}
