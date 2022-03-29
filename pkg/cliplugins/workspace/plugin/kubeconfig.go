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
	"reflect"
	"strings"
	"time"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1beta1 "k8s.io/apimachinery/pkg/apis/meta/v1beta1"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	virtualcommandoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	tenancyclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/virtual/workspaces/registry"
)

const (
	kcpPreviousWorkspaceContextKey         string = "workspace.kcp.dev/previous"
	kcpCurrentWorkspaceContextKey          string = "workspace.kcp.dev/current"
	kcpOverridesContextAuthInfoKey         string = "workspace.kcp.dev/overridden-auth"
	kcpVirtualWorkspaceInternalContextName string = "workspace.kcp.dev/workspace-directory"
)

// KubeConfig contains a config loaded from a Kubeconfig
// and allows modifications on it through workspace-related
// actions
type KubeConfig struct {
	configAccess   clientcmd.ConfigAccess
	startingConfig *api.Config
}

// NewKubeConfig load a kubeconfig with default config access
func NewKubeConfig(opts *Options) (*KubeConfig, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	configAccess := clientcmd.NewDefaultClientConfigLoadingRules()

	var err error
	startingConfig, err := configAccess.GetStartingConfig()
	if err != nil {
		return nil, err
	}

	return &KubeConfig{
		configAccess:   configAccess,
		startingConfig: startingConfig,
	}, nil
}

// UseWorkspace switch the current workspace to the given workspace.
// To do so it retrieves the ClusterWorkspace minimal KubeConfig (mainly cluster infos)
// from the `workspaces` virtual workspace `workspaces/kubeconfig` sub-resources,
// and adds it (along with the Auth info that is currently used) to the Kubeconfig.
// Then it make this new context the current context.
func (kc *KubeConfig) UseWorkspace(ctx context.Context, opts *Options, requestedWorkspaceName string) error {
	workspaceIsRoot := false
	currentContextName, err := kc.getCurrentContextName(opts)
	if err != nil {
		return err
	}

	// Store the currentContext content, so that it will replace the previous context content
	// at the end
	currentContext, err := kc.getContext(currentContextName)
	if err != nil {
		return err
	}
	currentContextCluster, err := kc.getContextCluster(currentContextName)
	if err != nil {
		return err
	}
	currentContextAuthName, err := kc.getContextAuthName(currentContextName)
	if err != nil {
		return err
	}

	var tenancyClient *tenancyclient.Clientset
	contextName := currentContextName
	if requestedWorkspaceName == "-" {
		if _, previousWorkspaceExists := kc.startingConfig.Contexts[kcpPreviousWorkspaceContextKey]; !previousWorkspaceExists {
			return errors.New("No previous workspace exists !")
		}

		var err error
		var orgName logicalcluster.LogicalCluster
		orgName, requestedWorkspaceName, err = kc.getWorkspace(kcpPreviousWorkspaceContextKey)
		if err != nil {
			return err
		}

		if orgName.Empty() && requestedWorkspaceName == tenancyv1alpha1.RootCluster.String() {
			workspaceIsRoot = true
		}

		contextName = kcpPreviousWorkspaceContextKey
		tenancyClient, err = kc.tenancyClient(contextName, opts, true)
		if err != nil {
			return err
		}

		if opts.Scope == registry.PersonalScope && !orgName.Empty() && orgName != tenancyv1alpha1.RootCluster {
			// If we're in the personal scope, and request a non-organization workspace, the workspaceName
			// returned based on the current context URL might be different from the one visible to the end-user
			// due to pretty names (== alias) management.
			// Let's grab the user-facing name (pretty name) based on the workspace internal (organization-wide) name.
			if ws, err := getWorkspaceFromInternalName(ctx, requestedWorkspaceName, tenancyClient); err != nil {
				return err
			} else {
				requestedWorkspaceName = ws.Name
			}
		}
	} else {
		tenancyClient, err = kc.tenancyClient(contextName, opts, false)
		if err != nil {
			return err
		}
	}

	// Retrieve the kubeconfig cluster info that will be updated
	// in the new current workspace context
	var newContextCluster *api.Cluster
	if workspaceIsRoot {
		// If we're switching to the root workspace, let's not try to retrieve the updated
		// kubeconfig of the root cluster, which makes no sense.
		newContextCluster, err = kc.getContextCluster(contextName)
		if err != nil {
			return err
		}
	} else {
		// Let's request the KubeConfig from the virtual workspaces
		workspaceKubeConfigBytes, err := tenancyClient.TenancyV1beta1().RESTClient().Get().Resource("workspaces").SubResource("kubeconfig").Name(requestedWorkspaceName).Do(ctx).Raw()
		if err != nil {
			return err
		}
		workspaceConfig, err := clientcmd.Load(workspaceKubeConfigBytes)
		if err != nil {
			return err
		}
		newContextCluster = workspaceConfig.Clusters[workspaceConfig.CurrentContext]
	}

	// Update the current workspace cluster info with the new cluster info
	kc.startingConfig.Clusters[kcpCurrentWorkspaceContextKey] = newContextCluster.DeepCopy()

	// Add authentication info based on either the kubectl overrides or the current or previous kubectl context
	newContext, err := kc.getContext(contextName)
	if err != nil {
		return err
	}
	authInfoName := newContext.AuthInfo
	if authInfoName == "" {
		authInfoName = currentContext.AuthInfo
	}
	kubectlOverrides := opts.KubectlOverrides
	if !reflect.ValueOf(kubectlOverrides.AuthInfo).IsZero() {
		kc.startingConfig.AuthInfos[kcpOverridesContextAuthInfoKey] = &kubectlOverrides.AuthInfo
		authInfoName = kcpOverridesContextAuthInfoKey
	} else {
		if authInfoName != kcpOverridesContextAuthInfoKey && currentContextAuthName != kcpOverridesContextAuthInfoKey {
			delete(kc.startingConfig.AuthInfos, kcpOverridesContextAuthInfoKey)
		}
	}

	kc.startingConfig.Contexts[kcpCurrentWorkspaceContextKey] = &api.Context{
		Cluster:  kcpCurrentWorkspaceContextKey,
		AuthInfo: authInfoName,
	}

	// Set the kubeconfig current context to the current workspace context
	kc.startingConfig.CurrentContext = kcpCurrentWorkspaceContextKey

	// Get the orgName and workspace internal name from the new current workspace context Server URL
	orgName, workspaceInternalName, err := kc.getWorkspace(kcpCurrentWorkspaceContextKey)
	if err != nil {
		return err
	}

	_ = outputCurrentWorkspaceMessage(orgName, requestedWorkspaceName, workspaceInternalName, opts)

	kc.startingConfig.Clusters[kcpPreviousWorkspaceContextKey] = currentContextCluster.DeepCopy()
	kc.startingConfig.Contexts[kcpPreviousWorkspaceContextKey] = &api.Context{
		Cluster:  kcpPreviousWorkspaceContextKey,
		AuthInfo: currentContextAuthName,
	}

	return clientcmd.ModifyConfig(kc.configAccess, *kc.startingConfig, true)
}

// CurrentWorkspace outputs the current workspace.
func (kc *KubeConfig) CurrentWorkspace(ctx context.Context, opts *Options) error {
	org, workspaceName, err := kc.getCurrentWorkspace(opts)
	if err != nil {
		return err
	}

	workspacePrettyName := workspaceName
	if !org.Empty() {
		workspaceDirectoryRestConfig, err := kc.workspaceDirectoryRestConfigFromCurrentContext(opts, true)
		if err != nil {
			return err
		}

		tenancyClient, err := tenancyclient.NewForConfig(workspaceDirectoryRestConfig)
		if err != nil {
			return err
		}

		if ws, err := getWorkspaceFromInternalName(ctx, workspaceName, tenancyClient); err != nil {
			_ = outputCurrentWorkspaceMessage(org, workspacePrettyName, workspaceName, opts)
			return err
		} else {
			if opts.Scope == registry.PersonalScope {
				workspacePrettyName = ws.Name
			}
		}
	}

	return outputCurrentWorkspaceMessage(org, workspacePrettyName, workspaceName, opts)
}

// ListWorkspaces outputs the list of workspaces of the current user
// (kubeconfig user possibly overridden by CLI options).
func (kc *KubeConfig) ListWorkspaces(ctx context.Context, opts *Options) error {
	workspaceDirectoryRestConfig, err := kc.workspaceDirectoryRestConfigFromCurrentContext(opts, false)
	if err != nil {
		return err
	}

	tenancyClient, err := tenancyclient.NewForConfig(workspaceDirectoryRestConfig)
	if err != nil {
		return err
	}

	result := tenancyClient.TenancyV1beta1().RESTClient().Get().Resource("workspaces").SetHeader("Accept", strings.Join([]string{
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

// CreateWorkspace creates a workspace owned by the the current user
// (kubeconfig user possibly overridden by CLI options).
func (kc *KubeConfig) CreateWorkspace(ctx context.Context, opts *Options, workspaceName string, useAfterCreation bool, wsType string) error {
	workspaceDirectoryRestConfig, err := kc.workspaceDirectoryRestConfigFromCurrentContext(opts, false)
	if err != nil {
		return err
	}

	tenancyClient, err := tenancyclient.NewForConfig(workspaceDirectoryRestConfig)
	if err != nil {
		return err
	}

	if _, err := tenancyClient.TenancyV1beta1().Workspaces().Create(ctx, &tenancyv1beta1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name: workspaceName,
		},
		Spec: tenancyv1beta1.WorkspaceSpec{
			Type: wsType,
		},
	}, metav1.CreateOptions{}); err != nil {
		return err
	}

	if err = write(opts, fmt.Sprintf("Workspace \"%s\" created.\n", workspaceName)); err != nil {
		return err
	}

	if useAfterCreation {
		time.Sleep(1 * time.Second)
		if err := kc.UseWorkspace(ctx, opts, workspaceName); err != nil {
			return err
		}
	}
	return nil
}

// DeleteWorkspace deletes a workspace owned by the the current user
// (kubeconfig user possibly overridden by CLI options).
func (kc *KubeConfig) DeleteWorkspace(ctx context.Context, opts *Options, workspaceName string) error {
	workspaceDirectoryRestConfig, err := kc.workspaceDirectoryRestConfigFromCurrentContext(opts, false)
	if err != nil {
		return err
	}

	tenancyClient, err := tenancyclient.NewForConfig(workspaceDirectoryRestConfig)
	if err != nil {
		return err
	}

	if err := tenancyClient.TenancyV1beta1().Workspaces().Delete(ctx, workspaceName, metav1.DeleteOptions{}); err != nil {
		return err
	}

	return write(opts, fmt.Sprintf("Workspace \"%s\" deleted.\n", workspaceName))
}

// getCurrentContextName returns the current context from kubeconfig.
func (kc *KubeConfig) getCurrentContextName(opts *Options) (string, error) {
	currentContextName := kc.startingConfig.CurrentContext

	kubectlOverrides := opts.KubectlOverrides
	if kubectlOverrides.CurrentContext != "" {
		currentContextName = kubectlOverrides.CurrentContext
	}

	if currentContextName == "" {
		return "", errors.New("no current context")
	}

	return currentContextName, nil
}

// getContextClusterName returns the cluster Name for a given kubeconfig context.
func (kc *KubeConfig) getContext(contextName string) (*api.Context, error) {
	if context := kc.startingConfig.Contexts[contextName]; context != nil {
		return context, nil
	} else {
		return nil, fmt.Errorf("cannot find context name %q", contextName)
	}
}

// getContextClusterName returns the cluster Name for a given kubeconfig context.
func (kc *KubeConfig) getContextClusterName(contextName string) (string, error) {
	if context, err := kc.getContext(contextName); err == nil {
		return context.Cluster, nil
	} else {
		return "", err
	}
}

// getContextCluster returns the Cluster struct for a given kubeconfig context.
func (kc *KubeConfig) getContextCluster(contextName string) (*api.Cluster, error) {
	if clusterName, err := kc.getContextClusterName(contextName); err == nil {
		return kc.startingConfig.Clusters[clusterName], nil
	} else {
		return nil, err
	}
}

// getServer returns the cluster Server URL for a given kubeconfig context.
func (kc *KubeConfig) getServer(contextName string) (string, error) {
	if clusterName, err := kc.getContextClusterName(contextName); err == nil {
		return kc.startingConfig.Clusters[clusterName].Server, nil
	} else {
		return "", err
	}
}

// getContextAuthName returns the auth name for a given kubeconfig context.
func (kc *KubeConfig) getContextAuthName(contextName string) (string, error) {
	if context, err := kc.getContext(contextName); err == nil {
		return context.AuthInfo, nil
	} else {
		return "", err
	}
}

// workspaceDirectoryRestConfigWithoutAuth creates a kubeconfig context
// that corresponds to the expected workspace directory context
// (thus pointing to the `workspaces` virtual workspace).
// The produced contexted is based on the kubeconfig current context, overridden by
// by the kubectl overrides.
// No Auth info is added in this workspace directory new context.
// The current kubeconfig is not modified, and the returned additional context is transient.
func (kc *KubeConfig) workspaceDirectoryRestConfigWithoutAuth(contextName string, opts *Options, alwaysUpToOrg bool) (*api.Config, error) {
	contextCluster, err := kc.getContextCluster(contextName)
	if err != nil {
		return nil, err
	}

	workspaceDirectoryAwareConfig := api.NewConfig()

	serverURL, err := url.Parse(contextCluster.Server)
	if err != nil {
		return nil, err
	}

	orgClusterName, workspaceName, basePath, err := getWorkspaceAndBasePath(contextCluster.Server)
	if err != nil {
		return nil, err
	}

	orgClusterName = upToOrg(orgClusterName, workspaceName, alwaysUpToOrg)

	// construct virtual workspace URL. This might redirect to another server if the virtual workspace apiserver is running standalone.
	serverURL.Path = path.Join(basePath, virtualcommandoptions.DefaultRootPathPrefix, "workspaces", orgClusterName.String(), opts.Scope)

	workspaceDirectoryCluster := contextCluster.DeepCopy()
	workspaceDirectoryCluster.Server = serverURL.String()

	kubectlOverrides := opts.KubectlOverrides
	if kubectlOverrides.ClusterInfo.CertificateAuthority != "" {
		workspaceDirectoryCluster.CertificateAuthority = kubectlOverrides.ClusterInfo.CertificateAuthority
	}
	if kubectlOverrides.ClusterInfo.TLSServerName != "" {
		workspaceDirectoryCluster.TLSServerName = kubectlOverrides.ClusterInfo.TLSServerName
	}
	if kubectlOverrides.ClusterInfo.InsecureSkipTLSVerify {
		workspaceDirectoryCluster.InsecureSkipTLSVerify = kubectlOverrides.ClusterInfo.InsecureSkipTLSVerify
	}
	workspaceDirectoryAwareConfig.Clusters[kcpVirtualWorkspaceInternalContextName] = workspaceDirectoryCluster
	workspaceDirectoryContext := &api.Context{
		Cluster: kcpVirtualWorkspaceInternalContextName,
	}
	workspaceDirectoryAwareConfig.Contexts[kcpVirtualWorkspaceInternalContextName] = workspaceDirectoryContext
	workspaceDirectoryAwareConfig.CurrentContext = kcpVirtualWorkspaceInternalContextName

	return workspaceDirectoryAwareConfig, nil
}

// workspaceDirectoryRestConfig returns the rest.Config to access the workspace directory
// virtual workspace based on the stored config and CLI overrides, as well as the Auth info
// of the current kubeconfig context.
func (kc *KubeConfig) workspaceDirectoryRestConfig(contextName string, opts *Options, alwaysUpToOrg bool) (*rest.Config, error) {
	workpaceDirectoryAwareConfig, err := kc.workspaceDirectoryRestConfigWithoutAuth(contextName, opts, alwaysUpToOrg)
	if err != nil {
		return nil, err
	}

	context, err := kc.getContext(contextName)
	if err != nil {
		return nil, err
	}

	authInfoOverride := api.AuthInfo{}
	kubectlOverrides := opts.KubectlOverrides
	if !reflect.ValueOf(kubectlOverrides.AuthInfo).IsZero() {
		authInfoOverride = *(&kubectlOverrides.AuthInfo).DeepCopy()
	} else {
		if context != nil {
			if authInfo := kc.startingConfig.AuthInfos[context.AuthInfo]; authInfo != nil {
				authInfoOverride = *authInfo
			}
		}
	}

	overrides := &clientcmd.ConfigOverrides{
		AuthInfo: authInfoOverride,
	}

	config, err := clientcmd.NewDefaultClientConfig(*workpaceDirectoryAwareConfig, overrides).ClientConfig()
	if err != nil {
		return nil, err
	}

	config.UserAgent = rest.DefaultKubernetesUserAgent()
	return config, nil
}

// tenancyClient returns the tenancy client set to access the workspace directory
// virtual workspace based on the stored config and CLI overrides, as well as the Auth info
// of the current kubeconfig context.
func (kc *KubeConfig) tenancyClient(contextName string, opts *Options, alwaysUpToOrg bool) (*tenancyclient.Clientset, error) {
	if workspaceDirectoryRestConfig, err := kc.workspaceDirectoryRestConfig(contextName, opts, alwaysUpToOrg); err != nil {
		return nil, err
	} else {
		return tenancyclient.NewForConfig(workspaceDirectoryRestConfig)
	}
}

// workspaceDirectoryRestConfigFromCurrentContext returns the rest.Config to access the workspace directory
// virtual workspace based on the stored config and CLI overrides, as well as the Auth info
// of the current kubeconfig context.
func (kc *KubeConfig) workspaceDirectoryRestConfigFromCurrentContext(opts *Options, parent bool) (*rest.Config, error) {
	currentContextName, err := kc.getCurrentContextName(opts)
	if err != nil {
		return nil, err
	}
	return kc.workspaceDirectoryRestConfig(currentContextName, opts, parent)
}

// getWorkspace gets the workspace from a kubeconfig context.
func (kc *KubeConfig) getWorkspace(contextName string) (orgName logicalcluster.LogicalCluster, workspaceName string, err error) {
	serverInfo, err := kc.getServer(contextName)
	if err != nil {
		return logicalcluster.LogicalCluster{}, "", err
	}

	orgClusterName, workspaceName, _, err := getWorkspaceAndBasePath(serverInfo)
	if err != nil {
		return logicalcluster.LogicalCluster{}, "", err
	}
	return orgClusterName, workspaceName, err
}

// getCurrentWorkspace gets the current workspace from the kubeconfig.
func (kc *KubeConfig) getCurrentWorkspace(opts *Options) (orgName logicalcluster.LogicalCluster, workspaceName string, err error) {
	currentContextName, err := kc.getCurrentContextName(opts)
	if err != nil {
		return logicalcluster.LogicalCluster{}, "", err
	}

	return kc.getWorkspace(currentContextName)
}
