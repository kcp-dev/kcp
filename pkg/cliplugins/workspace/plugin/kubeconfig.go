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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1beta1 "k8s.io/apimachinery/pkg/apis/meta/v1beta1"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	virtualcommandoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	tenancyhelpers "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	tenancyclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
)

const (
	kcpWorkspaceContextNamePrefix          string = "workspace.kcp.dev/"
	kcpPreviousWorkspaceContextKey         string = "workspace.kcp.dev/previous"
	kcpCurrentWorkspaceContextKey          string = "workspace.kcp.dev/current"
	kcpVirtualWorkspaceInternalContextName string = "workspace.kcp.dev/workspace-directory"
)

// KubeConfig contains a config loaded from a Kubeconfig
// and allows modifications on it through workspace-related
// actions
type KubeConfig struct {
	configAccess   clientcmd.ConfigAccess
	startingConfig *api.Config
	scope          string
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
		scope:          opts.Scope,
	}, nil
}

// workspaceDirectoryRestConfigWithoutAuth creates a kubeconfig context
// that corresponds to the expected workspace directory context
// (thus pointing to the `workspaces` virtual workspace).
// The produced contexted is based on the kubeconfig current context, overridden by
// by the kubectl overrides.
// No Auth info is added in this workspace directory new context.
// The current kubeconfig is not modified, and the returned additional context is transient.
func (kc *KubeConfig) workspaceDirectoryRestConfigWithoutAuth(contextName string, options *Options, alwaysUpToOrg bool) (*api.Config, error) {
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
	serverURL.Path = path.Join(basePath, virtualcommandoptions.DefaultRootPathPrefix, "workspaces", orgClusterName, kc.scope)

	workspaceDirectoryCluster := contextCluster.DeepCopy()
	workspaceDirectoryCluster.Server = serverURL.String()

	kubectlOverrides := options.KubectlOverrides
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
func (kc *KubeConfig) workspaceDirectoryRestConfig(contextName string, options *Options, alwaysUpToOrg bool) (*rest.Config, error) {
	workpaceDirectoryAwareConfig, err := kc.workspaceDirectoryRestConfigWithoutAuth(contextName, options, alwaysUpToOrg)
	if err != nil {
		return nil, err
	}

	currentContextName, err := kc.getCurrentContextName(options)
	if err != nil {
		return nil, err
	}
	currentContext, err := kc.getContext(currentContextName)
	if err != nil {
		return nil, err
	}

	authInfoOverride := api.AuthInfo{}
	kubectlOverrides := options.KubectlOverrides
	if !reflect.ValueOf(kubectlOverrides.AuthInfo).IsZero() {
		authInfoOverride = *(&kubectlOverrides.AuthInfo).DeepCopy()
	} else {
		if currentContext != nil {
			if authInfo := kc.startingConfig.AuthInfos[currentContext.AuthInfo]; authInfo != nil {
				authInfoOverride = *authInfo
			}
		}
	}

	overrides := &clientcmd.ConfigOverrides{
		AuthInfo: authInfoOverride,
	}
	return clientcmd.NewDefaultClientConfig(*workpaceDirectoryAwareConfig, overrides).ClientConfig()
}

// workspaceDirectoryRestConfigFromCurrentContext returns the rest.Config to access the workspace directory
// virtual workspace based on the stored config and CLI overrides, as well as the Auth info
// of the current kubeconfig context.
func (kc *KubeConfig) workspaceDirectoryRestConfigFromCurrentContext(options *Options, parent bool) (*rest.Config, error) {
	currentContextName, err := kc.getCurrentContextName(options)
	if err != nil {
		return nil, err
	}
	return kc.workspaceDirectoryRestConfig(currentContextName, options, parent)
}

// UseWorkspace switch the current workspace to the given workspace.
// To do so it retrieves the ClusterWorkspace minimal KubeConfig (mainly cluster infos)
// from the `workspaces` virtual workspace `workspaces/kubeconfig` sub-resources,
// and adds it (along with the Auth info that is currently used) to the Kubeconfig.
// Then it make this new context the current context.
func (kc *KubeConfig) UseWorkspace(ctx context.Context, opts *Options, workspaceName string) error {
	searchInParent := false
	currentContextName, err := kc.getCurrentContextName(opts)
	if err != nil {
		return err
	}

	orgName, _, err := kc.getWorkspace(currentContextName)
	if err != nil {
		return err
	}

	contextName := currentContextName
	if workspaceName == "-" {
		if _, previousWorkspaceExists := kc.startingConfig.Contexts[kcpPreviousWorkspaceContextKey]; !previousWorkspaceExists {
			return errors.New("No previous workspace exists !")
		}

		var err error
		orgName, workspaceName, err = kc.getWorkspace(kcpPreviousWorkspaceContextKey)
		if err != nil {
			return err
		}
		searchInParent = true
		contextName = kcpPreviousWorkspaceContextKey
	}

	currentContext, err := kc.getContext(currentContextName)
	if err != nil {
		return err
	}
	currentContextCluster, err := kc.getContextCluster(currentContextName)
	if err != nil {
		return err
	}

	var newContextCluster *api.Cluster
	if (orgName == "" || orgName == tenancyhelpers.RootCluster) && searchInParent {
		newContextCluster, err = kc.getContextCluster(contextName)
		if err != nil {
			return err
		}
	} else {
		workspaceDirectoryRestConfig, err := kc.workspaceDirectoryRestConfig(contextName, opts, searchInParent)
		if err != nil {
			return err
		}

		tenancyClient, err := tenancyclient.NewForConfig(workspaceDirectoryRestConfig)
		if err != nil {
			return err
		}

		workspaceKubeConfigBytes, err := tenancyClient.TenancyV1beta1().RESTClient().Get().Resource("workspaces").SubResource("kubeconfig").Name(workspaceName).Do(ctx).Raw()
		if err != nil {
			return err
		}

		workspaceConfig, err := clientcmd.Load(workspaceKubeConfigBytes)
		if err != nil {
			return err
		}

		workspaceConfigCurrentContext := workspaceConfig.CurrentContext

		newContextCluster = workspaceConfig.Clusters[workspaceConfigCurrentContext]
	}
	kc.startingConfig.Clusters[kcpCurrentWorkspaceContextKey] = newContextCluster.DeepCopy()

	authInfoName := currentContext.AuthInfo
	kubectlOverrides := opts.KubectlOverrides
	if !reflect.ValueOf(kubectlOverrides.AuthInfo).IsZero() {
		kc.startingConfig.AuthInfos[kcpCurrentWorkspaceContextKey] = &kubectlOverrides.AuthInfo
		authInfoName = kcpCurrentWorkspaceContextKey
	} else {
		delete(kc.startingConfig.AuthInfos, kcpCurrentWorkspaceContextKey)
	}

	kc.startingConfig.Contexts[kcpCurrentWorkspaceContextKey] = &api.Context{
		Cluster:  kcpCurrentWorkspaceContextKey,
		AuthInfo: authInfoName,
	}

	kc.startingConfig.CurrentContext = kcpCurrentWorkspaceContextKey

	newOrgName, newWorkspaceName, err := kc.getWorkspace(kcpCurrentWorkspaceContextKey)
	if err != nil {
		return err
	}

	_ = outputCurrentWorkspaceMessage(newOrgName, newWorkspaceName, opts)

	kc.startingConfig.Clusters[kcpPreviousWorkspaceContextKey] = currentContextCluster.DeepCopy()
	kc.startingConfig.Contexts[kcpPreviousWorkspaceContextKey] = &api.Context{
		Cluster: kcpPreviousWorkspaceContextKey,
	}

	return clientcmd.ModifyConfig(kc.configAccess, *kc.startingConfig, true)
}

// getWorkspace gets the workspace from a kubeconfig context.
func (kc *KubeConfig) getWorkspace(contextName string) (orgName, workspaceName string, err error) {
	serverInfo, err := kc.getServer(contextName)
	if err != nil {
		return "", "", err
	}

	orgClusterName, workspaceName, _, err := getWorkspaceAndBasePath(serverInfo)
	if err != nil {
		return "", "", err
	}

	if orgClusterName == "" {
		orgName = ""
	} else {
		_, orgName, err = tenancyhelpers.ParseLogicalClusterName(orgClusterName)
		if err != nil {
			return "", "", err
		}
	}

	return orgName, workspaceName, nil
}

// getCurrentWorkspace gets the current workspace from the kubeconfig.
func (kc *KubeConfig) getCurrentWorkspace(opts *Options) (orgName, workspaceName string, err error) {
	currentContextName, err := kc.getCurrentContextName(opts)
	if err != nil {
		return "", "", err
	}

	return kc.getWorkspace(currentContextName)
}

// checkWorkspaceExists checks whether this workspace exsts in the
// user workspace directory, by requesting the `workspaces` virtual workspace.
func checkWorkspaceExists(ctx context.Context, workspaceName string, tenancyClient tenancyclient.Interface) error {
	_, err := tenancyClient.TenancyV1beta1().Workspaces().Get(ctx, workspaceName, metav1.GetOptions{})
	return err
}

func outputCurrentWorkspaceMessage(orgName, workspaceName string, opts *Options) error {
	if workspaceName != "" {
		message := fmt.Sprintf("Current workspace is %q", workspaceName)
		if orgName != "" {
			message = fmt.Sprintf("%s in organization %q", message, orgName)
		}
		err := write(opts, fmt.Sprintf("%s.\n", message))
		return err
	}
	return nil
}

// CurrentWorkspace outputs the current workspace.
func (kc *KubeConfig) CurrentWorkspace(ctx context.Context, opts *Options) error {
	org, workspaceName, err := kc.getCurrentWorkspace(opts)
	if err != nil {
		return err
	}

	if org != "" {
		workspaceDirectoryRestConfig, err := kc.workspaceDirectoryRestConfigFromCurrentContext(opts, true)
		if err != nil {
			return err
		}

		tenancyClient, err := tenancyclient.NewForConfig(workspaceDirectoryRestConfig)
		if err != nil {
			return err
		}

		if err := checkWorkspaceExists(ctx, workspaceName, tenancyClient); err != nil {
			_ = outputCurrentWorkspaceMessage(org, workspaceName, opts)
			return err
		}
	}

	return outputCurrentWorkspaceMessage(org, workspaceName, opts)
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
func (kc *KubeConfig) CreateWorkspace(ctx context.Context, opts *Options, workspaceName string, useAfterCreation bool) error {
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
		Spec: tenancyv1beta1.WorkspaceSpec{},
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

// getClusterNameAndBasePath gets the logical cluster name and the base URL for the current
// workspace.
func getWorkspaceAndBasePath(urlPath string) (orgClusterName, workspaceName, basePath string, err error) {
	// get workspace from current server URL and check it point to an org or the root workspace
	serverURL, err := url.Parse(urlPath)
	if err != nil {
		return "", "", "", err
	}

	possiblePrefixes := []string{
		"/clusters/",
		path.Join(virtualcommandoptions.DefaultRootPathPrefix, "workspaces") + "/",
	}

	var clusterName string
	for _, prefix := range possiblePrefixes {
		clusterIndex := strings.Index(serverURL.Path, prefix)
		if clusterIndex < 0 {
			continue
		}
		clusterName = strings.SplitN(serverURL.Path[clusterIndex+len(prefix):], "/", 2)[0]
		basePath = serverURL.Path[:clusterIndex]
	}

	if clusterName == "" {
		return "", "", basePath, fmt.Errorf("current cluster URL %s is not pointing to a workspace", serverURL)
	}

	var org string
	if clusterName == tenancyhelpers.RootCluster {
		orgClusterName = ""
		workspaceName = tenancyhelpers.RootCluster
	} else if org, workspaceName, err = tenancyhelpers.ParseLogicalClusterName(clusterName); err != nil {
		return "", "", "", fmt.Errorf("unable to parse cluster name %s", clusterName)
	} else if org == "system:" {
		return "", "", "", fmt.Errorf("no workspaces are accessible from %s", clusterName)
	} else if org == tenancyhelpers.RootCluster {
		orgClusterName = tenancyhelpers.RootCluster
	} else {
		orgClusterName, err = tenancyhelpers.ParentClusterName(clusterName)
		if err != nil {
			// should never happen
			return "", "", "", fmt.Errorf("unable to derive parent cluster name for %s", clusterName)
		}
	}

	return orgClusterName, workspaceName, basePath, nil
}

// upToOrg derives the org workspace cluster name to operate on,
// from a given workspace logical cluster name.
func upToOrg(orgClusterName, workspaceName string, always bool) string {

	if orgClusterName == "" && workspaceName == tenancyhelpers.RootCluster {
		return tenancyhelpers.RootCluster
	}

	if orgClusterName == tenancyhelpers.RootCluster && !always {
		return tenancyhelpers.EncodeOrganizationAndClusterWorkspace(tenancyhelpers.RootCluster, workspaceName)
	}

	return orgClusterName
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

func (kc *KubeConfig) getContextCluster(contextName string) (*api.Cluster, error) {
	if clusterName, err := kc.getContextClusterName(contextName); err == nil {
		return kc.startingConfig.Clusters[clusterName], nil
	} else {
		return nil, err
	}
}

func (kc *KubeConfig) getServer(contextName string) (string, error) {
	if clusterName, err := kc.getContextClusterName(contextName); err == nil {
		return kc.startingConfig.Clusters[clusterName].Server, nil
	} else {
		return "", err
	}
}
