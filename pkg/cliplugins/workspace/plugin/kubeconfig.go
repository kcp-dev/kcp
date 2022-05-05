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

package plugin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/kcp-dev/logicalcluster"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1beta1 "k8s.io/apimachinery/pkg/apis/meta/v1beta1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	tenancyclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	pluginhelpers "github.com/kcp-dev/kcp/pkg/cliplugins/helpers"
)

const (
	kcpPreviousWorkspaceContextKey string = "workspace.kcp.dev/previous"
	kcpCurrentWorkspaceContextKey  string = "workspace.kcp.dev/current"
)

// KubeConfig contains a config loaded from a Kubeconfig
// and allows modifications on it through workspace-related
// actions
type KubeConfig struct {
	startingConfig *clientcmdapi.Config
	overrides      *clientcmd.ConfigOverrides
	currentContext string // including override

	clusterClient  tenancyclient.ClusterInterface
	personalClient tenancyclient.ClusterInterface
	modifyConfig   func(newConfig *clientcmdapi.Config) error

	genericclioptions.IOStreams
}

// NewKubeConfig load a kubeconfig with default config access
func NewKubeConfig(opts *Options) (*KubeConfig, error) {
	configAccess := clientcmd.NewDefaultClientConfigLoadingRules()
	startingConfig, err := configAccess.GetStartingConfig()
	if err != nil {
		return nil, err
	}

	currentContext := startingConfig.CurrentContext
	if opts.KubectlOverrides.CurrentContext != "" {
		currentContext = opts.KubectlOverrides.CurrentContext
	}

	// get lcluster-independent client
	config, err := clientcmd.NewDefaultClientConfig(*startingConfig, opts.KubectlOverrides).ClientConfig()
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(config.Host)
	if err != nil {
		return nil, err
	}
	u.Path = ""

	clusterConfig := rest.CopyConfig(config)
	clusterConfig.Host = u.String()
	clusterConfig.UserAgent = rest.DefaultKubernetesUserAgent()
	clusterClient, err := tenancyclient.NewClusterForConfig(clusterConfig)
	if err != nil {
		return nil, err
	}

	return &KubeConfig{
		startingConfig: startingConfig,
		overrides:      opts.KubectlOverrides,
		currentContext: currentContext,

		clusterClient:  clusterClient,
		personalClient: &personalClusterClient{clusterConfig},
		modifyConfig: func(newConfig *clientcmdapi.Config) error {
			return clientcmd.ModifyConfig(configAccess, *newConfig, true)
		},

		IOStreams: opts.IOStreams,
	}, nil
}

// UseWorkspace switch the current workspace to the given workspace.
// To do so it retrieves the ClusterWorkspace minimal KubeConfig (mainly cluster infos)
// from the `workspaces` virtual workspace `workspaces/kubeconfig` sub-resources,
// and adds it (along with the Auth info that is currently used) to the Kubeconfig.
// Then it make this new context the current context.
func (kc *KubeConfig) UseWorkspace(ctx context.Context, name string) error {
	// Store the currentContext content for later to set as previous context
	currentContext, found := kc.startingConfig.Contexts[kc.currentContext]
	if !found {
		return fmt.Errorf("current %q context not found", kc.currentContext)
	}

	var newServerHost, workspaceType string
	switch name {
	case "-":
		prev, exists := kc.startingConfig.Contexts[kcpPreviousWorkspaceContextKey]
		if !exists {
			return errors.New("no previous workspace found in kubeconfig")
		}

		newKubeConfig := kc.startingConfig.DeepCopy()
		if currentContext.Cluster == kcpCurrentWorkspaceContextKey {
			oldCluster, found := kc.startingConfig.Clusters[currentContext.Cluster]
			if !found {
				return fmt.Errorf("cluster %q not found in kubeconfig", currentContext.Cluster)
			}
			currentContext = currentContext.DeepCopy()
			currentContext.Cluster = kcpPreviousWorkspaceContextKey
			newKubeConfig.Clusters[kcpPreviousWorkspaceContextKey] = oldCluster
		}
		if prev.Cluster == kcpPreviousWorkspaceContextKey {
			prevCluster, found := kc.startingConfig.Clusters[prev.Cluster]
			if !found {
				return fmt.Errorf("cluster %q not found in kubeconfig", currentContext.Cluster)
			}
			prev = prev.DeepCopy()
			prev.Cluster = kcpCurrentWorkspaceContextKey
			newKubeConfig.Clusters[kcpCurrentWorkspaceContextKey] = prevCluster
		}
		newKubeConfig.Contexts[kcpCurrentWorkspaceContextKey] = prev
		newKubeConfig.Contexts[kcpPreviousWorkspaceContextKey] = currentContext

		newKubeConfig.CurrentContext = kcpCurrentWorkspaceContextKey

		if err := kc.modifyConfig(newKubeConfig); err != nil {
			return err
		}

		return kc.currentWorkspace(ctx, newKubeConfig.Clusters[newKubeConfig.Contexts[kcpCurrentWorkspaceContextKey].Cluster].Server, "", false)

	case "..":
		config, err := clientcmd.NewDefaultClientConfig(*kc.startingConfig, kc.overrides).ClientConfig()
		if err != nil {
			return err
		}
		u, currentClusterName, err := pluginhelpers.ParseClusterURL(config.Host)
		if err != nil {
			return fmt.Errorf("current URL %q does not point to cluster workspace", config.Host)
		}
		parentClusterName, hasParent := currentClusterName.Parent()
		if !hasParent {
			if currentClusterName == tenancyv1alpha1.RootCluster {
				return fmt.Errorf("current workspace is %q", currentClusterName)
			}
			return fmt.Errorf("current workspace %q has no parent", currentClusterName)
		}
		u.Path = path.Join(u.Path, parentClusterName.Path())
		newServerHost = u.String()

	case "":
		return kc.CurrentWorkspace(ctx, false)

	default:
		config, err := clientcmd.NewDefaultClientConfig(*kc.startingConfig, kc.overrides).ClientConfig()
		if err != nil {
			return err
		}
		u, currentClusterName, err := pluginhelpers.ParseClusterURL(config.Host)
		if err != nil {
			return fmt.Errorf("current URL %q does not point to cluster workspace", config.Host)
		}

		if strings.Contains(name, ":") || name == tenancyv1alpha1.RootCluster.String() {
			// absolute logical cluster
			u.Path = path.Join(u.Path, logicalcluster.New(name).Path())
			newServerHost = u.String()
		} else {
			// relative logical cluster, get URL from workspace object in current context
			ws, err := kc.personalClient.Cluster(currentClusterName).TenancyV1beta1().Workspaces().Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if ws.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseReady {
				return fmt.Errorf("workspace %q is not ready", name)
			}

			newServerHost = ws.Status.URL
			workspaceType = ws.Spec.Type
		}
	}

	// modify kubeconfig, using the "workspace" context and cluster
	newKubeConfig := kc.startingConfig.DeepCopy()
	oldCluster, found := kc.startingConfig.Clusters[currentContext.Cluster]
	if !found {
		return fmt.Errorf("cluster %q not found in kubeconfig", currentContext.Cluster)
	}
	newCluster := *oldCluster
	newCluster.Server = newServerHost
	newKubeConfig.Clusters[kcpCurrentWorkspaceContextKey] = &newCluster
	newContext := *currentContext
	newContext.Cluster = kcpCurrentWorkspaceContextKey
	newKubeConfig.Contexts[kcpCurrentWorkspaceContextKey] = &newContext

	// store old context and old cluster
	if currentContext.Cluster == kcpCurrentWorkspaceContextKey {
		currentContext = currentContext.DeepCopy()
		currentContext.Cluster = kcpPreviousWorkspaceContextKey
		newKubeConfig.Clusters[kcpPreviousWorkspaceContextKey] = oldCluster
	}
	newKubeConfig.Contexts[kcpPreviousWorkspaceContextKey] = currentContext

	newKubeConfig.CurrentContext = kcpCurrentWorkspaceContextKey

	if err := kc.modifyConfig(newKubeConfig); err != nil {
		return err
	}

	return kc.currentWorkspace(ctx, newServerHost, workspaceType, false)
}

// CurrentWorkspace outputs the current workspace.
func (kc *KubeConfig) CurrentWorkspace(ctx context.Context, shortWorkspaceOutput bool) error {
	config, err := clientcmd.NewDefaultClientConfig(*kc.startingConfig, kc.overrides).ClientConfig()
	if err != nil {
		return err
	}

	return kc.currentWorkspace(ctx, config.Host, "", shortWorkspaceOutput)
}

func (kc *KubeConfig) currentWorkspace(ctx context.Context, host, workspaceType string, shortWorkspaceOutput bool) error {
	_, clusterName, err := pluginhelpers.ParseClusterURL(host)
	if err != nil {
		if shortWorkspaceOutput {
			return nil
		}
		_, err = fmt.Fprintf(kc.Out, "Current workspace is the URL %q.\n", host)
		return err
	}

	if shortWorkspaceOutput {
		fmt.Fprintf(kc.Out, "%s\n", clusterName) // nolint: errcheck
		return nil
	}

	parentClusterName, workspaceName := clusterName.Split()
	workspacePrettyName := workspaceName
	if !parentClusterName.Empty() && clusterName.HasPrefix(tenancyv1alpha1.RootCluster) {
		// we are in a workspace below root, but not root itself
		ws, err := getWorkspaceFromInternalName(ctx, workspaceName, kc.clusterClient.Cluster(parentClusterName))
		if err == nil {
			workspacePrettyName = ws.Name
		}
	}

	message := fmt.Sprintf("Current workspace is %q", clusterName)
	if workspaceType != "" {
		message += fmt.Sprintf(" (type %q)", workspaceType)
	}
	if workspaceName != workspacePrettyName {
		message += fmt.Sprintf(" aliased as %q", workspacePrettyName)
	}
	_, err = fmt.Fprintln(kc.Out, message+".")
	return err
}

// CreateWorkspace creates a workspace owned by the the current user
// (kubeconfig user possibly overridden by CLI options).
func (kc *KubeConfig) CreateWorkspace(ctx context.Context, workspaceName string, workspaceType string, ignoreExisting, useAfterCreation bool, readyWaitTimeout time.Duration) error {
	config, err := clientcmd.NewDefaultClientConfig(*kc.startingConfig, kc.overrides).ClientConfig()
	if err != nil {
		return err
	}
	_, currentClusterName, err := pluginhelpers.ParseClusterURL(config.Host)
	if err != nil {
		return fmt.Errorf("current URL %q does not point to cluster workspace", config.Host)
	}

	preExisting := false
	ws, err := kc.personalClient.Cluster(currentClusterName).TenancyV1beta1().Workspaces().Create(ctx, &tenancyv1beta1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name: workspaceName,
		},
		Spec: tenancyv1beta1.WorkspaceSpec{
			Type: workspaceType,
		},
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) && ignoreExisting {
		preExisting = true
		ws, err = kc.personalClient.Cluster(currentClusterName).TenancyV1beta1().Workspaces().Get(ctx, workspaceName, metav1.GetOptions{})
	}
	if err != nil {
		return err
	}

	if preExisting {
		if workspaceType != "" && ws.Spec.Type != workspaceType {
			return fmt.Errorf("workspace %q already exists with different type %q", workspaceName, ws.Spec.Type)
		}
		if ws.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseReady && readyWaitTimeout > 0 {
			fmt.Fprintf(kc.Out, "Workspace %q (type %q) already exists. Waiting for being ready.\n", workspaceName, ws.Spec.Type) // nolint: errcheck
		} else {
			fmt.Fprintf(kc.Out, "Workspace %q (type %q) already exists.\n", workspaceName, ws.Spec.Type)
		}
	} else if ws.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseReady && readyWaitTimeout > 0 {
		fmt.Fprintf(kc.Out, "Workspace %q (type %q) created. Waiting for being ready.\n", workspaceName, ws.Spec.Type) // nolint: errcheck
	} else if ws.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseReady {
		return fmt.Errorf("workspace %q (type %q) created but not ready", workspaceName, ws.Spec.Type)
	}

	// STOP THE BLEEDING: the virtual workspace is still informer based (not good). We have to wait until it shows up.
	if err := wait.PollImmediate(time.Millisecond*100, time.Second*5, func() (bool, error) {
		if _, err := kc.personalClient.Cluster(currentClusterName).TenancyV1beta1().Workspaces().Get(ctx, ws.Name, metav1.GetOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}); err != nil {
		return err
	}

	// wait for being ready
	if ws.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseReady {
		if err := wait.PollImmediate(time.Millisecond*500, readyWaitTimeout, func() (bool, error) {
			ws, err = kc.personalClient.Cluster(currentClusterName).TenancyV1beta1().Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			if ws.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseReady {
				return true, nil
			}
			return false, nil
		}); err != nil {
			return err
		}
	}

	if useAfterCreation {
		u, err := url.Parse(ws.Status.URL)
		if err != nil {
			return err
		}
		internalName := path.Base(u.Path)
		if err := kc.UseWorkspace(ctx, internalName); err != nil {
			return err
		}
	}
	return nil
}

// ListWorkspaces outputs the list of workspaces of the current user
// (kubeconfig user possibly overridden by CLI options).
func (kc *KubeConfig) ListWorkspaces(ctx context.Context, opts *Options) error {
	config, err := clientcmd.NewDefaultClientConfig(*kc.startingConfig, kc.overrides).ClientConfig()
	if err != nil {
		return err
	}
	_, currentClusterName, err := pluginhelpers.ParseClusterURL(config.Host)
	if err != nil {
		return fmt.Errorf("current URL %q does not point to cluster workspace", config.Host)
	}

	result := kc.personalClient.Cluster(currentClusterName).TenancyV1beta1().RESTClient().Get().Resource("workspaces").SetHeader("Accept", strings.Join([]string{
		fmt.Sprintf("application/json;as=Table;v=%s;g=%s", metav1.SchemeGroupVersion.Version, metav1.GroupName),
		fmt.Sprintf("application/json;as=Table;v=%s;g=%s", metav1beta1.SchemeGroupVersion.Version, metav1beta1.GroupName),
		"application/json",
	}, ",")).Do(ctx)

	var statusCode int
	if result.StatusCode(&statusCode).Error() != nil {
		return result.Error()
	}

	if statusCode != http.StatusOK {
		rawResult, err := result.Raw()
		if err != nil {
			return err
		}
		return errors.New(string(rawResult))
	}

	table, err := result.Get()
	if err != nil {
		return err
	}

	printer := printers.NewTablePrinter(printers.PrintOptions{
		Wide: true,
	})

	return printer.PrintObj(table, opts.Out)
}

func (kc *KubeConfig) CreateContext(ctx context.Context, name string, overwrite bool) error {
	config, err := clientcmd.NewDefaultClientConfig(*kc.startingConfig, nil).RawConfig()
	if err != nil {
		return err
	}
	currentContext, ok := config.Contexts[config.CurrentContext]
	if !ok {
		return fmt.Errorf("current context %q is not found in kubeconfig", config.CurrentContext)
	}
	currentCluster, ok := config.Clusters[currentContext.Cluster]
	if !ok {
		return fmt.Errorf("current cluster %q is not found in kubeconfig", currentContext.Cluster)
	}
	_, currentClusterName, err := pluginhelpers.ParseClusterURL(currentCluster.Server)
	if err != nil {
		return fmt.Errorf("current URL %q does not point to cluster workspace", currentCluster.Server)
	}

	if name == "" {
		name = currentClusterName.String()
	}

	_, existedBefore := kc.startingConfig.Contexts[name]
	if existedBefore && !overwrite {
		return fmt.Errorf("context %q already exists in kubeconfig, use --overwrite to update it", name)
	}

	newKubeConfig := kc.startingConfig.DeepCopy()
	newCluster := *currentCluster
	newKubeConfig.Clusters[name] = &newCluster
	newContext := *currentContext
	newContext.Cluster = name
	newKubeConfig.Contexts[name] = &newContext
	newKubeConfig.CurrentContext = name

	if err := kc.modifyConfig(newKubeConfig); err != nil {
		return err
	}

	if existedBefore {
		if kc.startingConfig.CurrentContext == name {
			fmt.Fprintf(kc.Out, "Updated context %q.\n", name) // nolint: errcheck
		} else {
			fmt.Fprintf(kc.Out, "Updated context %q and switched to it.\n", name) // nolint:	errcheck
		}
	} else {
		fmt.Fprintf(kc.Out, "Created context %q and switched to it.\n", name) // nolint: errcheck
	}

	return nil
}
