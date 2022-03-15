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
	kcpPreviousWorkspaceContextKey         string = "workspace.kcp.dev/-"
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

// ensureWorkspaceDirectoryContextExists tries to find a context in the kubeconfig
// that corresponds to the expected workspace directory context
// (thus pointing to the `workspaces` virtual workspace).
// If none is found produce one, based on the kubeconfig current context, overridden by
// by the workspace directory overrides.
// No Auth info is added in this workspace directory new context.
// The current kubeconfig is not modified though, but a copy of it is returned
func (kc *KubeConfig) ensureWorkspaceDirectoryContextExists(options *Options, parent bool) (*api.Config, error) {
	workspaceDirectoryAwareConfig := kc.startingConfig.DeepCopy()
	currentContextName := workspaceDirectoryAwareConfig.CurrentContext
	if options.KubectlOverrides.CurrentContext != "" {
		currentContextName = options.KubectlOverrides.CurrentContext
	}
	currentContext := workspaceDirectoryAwareConfig.Contexts[currentContextName]

	if currentContext == nil {
		return nil, errors.New("no current context")
	}

	workspaceDirectoryCluster := workspaceDirectoryAwareConfig.Clusters[currentContext.Cluster].DeepCopy()
	workspaceDirectoryContext := &api.Context{
		Cluster: kcpVirtualWorkspaceInternalContextName,
	}

	// get workspace from current server URL and check it point to an org or the root workspace
	serverURL, err := url.Parse(workspaceDirectoryCluster.Server)
	if err != nil {
		return nil, err
	}
	possiblePrefixes := []string{
		"/clusters/",
		path.Join(virtualcommandoptions.DefaultRootPathPrefix, "workspaces") + "/",
	}
	var clusterName, basePath string
	for _, prefix := range possiblePrefixes {
		clusterIndex := strings.Index(serverURL.Path, prefix)
		if clusterIndex < 0 {
			continue
		}
		clusterName = strings.SplitN(serverURL.Path[clusterIndex+len(prefix):], "/", 2)[0]
		basePath = serverURL.Path[:clusterIndex]
	}
	if clusterName == "" {
		return nil, fmt.Errorf("current cluster URL %s is not pointing to a workspace", serverURL)
	}

	// derive the org workspace to operator on
	var orgClusterName string
	if clusterName == tenancyhelpers.RootCluster {
		orgClusterName = clusterName
	} else if org, _, err := tenancyhelpers.ParseLogicalClusterName(clusterName); err != nil {
		return nil, fmt.Errorf("unable to parse cluster name %s", clusterName)
	} else if org == "system:" {
		return nil, fmt.Errorf("no workspaces are accessible from %s", clusterName)
	} else if org == tenancyhelpers.RootCluster {
		if parent {
			orgClusterName = tenancyhelpers.RootCluster
		} else {
			// already in an org workspace
			orgClusterName = clusterName
		}
	} else {
		// some other workspace, return org cluster name
		orgClusterName, err = tenancyhelpers.ParentClusterName(clusterName)
		if err != nil {
			// should never happen
			return nil, fmt.Errorf("unable to derive parent cluster name for %s", clusterName)
		}
	}

	// construct virtual workspace URL. This might redirect to another server if the virtual workspace apiserver is running standalone.
	serverURL.Path = path.Join(basePath, virtualcommandoptions.DefaultRootPathPrefix, "workspaces", orgClusterName, kc.scope)
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
	workspaceDirectoryAwareConfig.Contexts[kcpVirtualWorkspaceInternalContextName] = workspaceDirectoryContext
	workspaceDirectoryAwareConfig.CurrentContext = kcpVirtualWorkspaceInternalContextName

	return workspaceDirectoryAwareConfig, nil
}

// workspaceDirectoryRestConfig returns the rest.Config to access the workspace directory
// virtual workspace based on the stored config and CLI overrides
func (kc *KubeConfig) workspaceDirectoryRestConfig(options *Options, parent bool) (*rest.Config, error) {
	workpaceDirectoryAwareConfig, err := kc.ensureWorkspaceDirectoryContextExists(options, parent)
	if err != nil {
		return nil, err
	}

	currentContextAuthInfo := &api.AuthInfo{}
	currentContext := workpaceDirectoryAwareConfig.Contexts[kc.startingConfig.CurrentContext]
	if currentContext != nil {
		currentContextAuthInfo = workpaceDirectoryAwareConfig.AuthInfos[currentContext.AuthInfo]
	}

	kubectlOverrides := options.KubectlOverrides
	_, authInfo := prioritizedAuthInfo(&kubectlOverrides.AuthInfo, currentContextAuthInfo)
	overrides := &clientcmd.ConfigOverrides{
		AuthInfo: *authInfo,
	}
	return clientcmd.NewDefaultClientConfig(*workpaceDirectoryAwareConfig, overrides).ClientConfig()
}

// UseWorkspace switch the current workspace to the given workspace.
// To do so it retrieves the ClusterWorkspace minimal KubeConfig (mainly cluster infos)
// from the `workspaces` virtual workspace `workspaces/kubeconfig` sube-resources,
// and adds it (along with the Auth info that is currently used) to the Kubeconfig.
// Then it make this new context the current context.
func (kc *KubeConfig) UseWorkspace(ctx context.Context, opts *Options, workspaceName string) error {

	workspaceDirectoryRestConfig, err := kc.workspaceDirectoryRestConfig(opts, false)
	if err != nil {
		return err
	}

	tenancyClient, err := tenancyclient.NewForConfig(workspaceDirectoryRestConfig)
	if err != nil {
		return err
	}

	if workspaceName == "-" {
		if _, previousWorkspaceExists := kc.startingConfig.Contexts[kcpPreviousWorkspaceContextKey]; !previousWorkspaceExists {
			return errors.New("No previous workspace exists !")
		}
		_, workspaceName = extractScopeAndName(string(kc.startingConfig.Contexts[kcpPreviousWorkspaceContextKey].Cluster))
		delete(kc.startingConfig.Contexts, kcpPreviousWorkspaceContextKey)
	}

	currentWorkspaceScope, currentWorkspaceName, err := kc.getCurrentWorkspace(opts)
	if err == nil && currentWorkspaceName != "" {
		kc.startingConfig.Contexts[kcpPreviousWorkspaceContextKey] = &api.Context{
			Cluster: currentWorkspaceScope + "/" + currentWorkspaceName,
		}
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
	workspaceContextName := kcpWorkspaceContextNamePrefix + workspaceConfigCurrentContext

	kubectlOverrides := opts.KubectlOverrides
	currentContextName := kc.startingConfig.CurrentContext
	if kubectlOverrides.CurrentContext != "" {
		currentContextName = kubectlOverrides.CurrentContext
	}

	currentContext := kc.startingConfig.Contexts[currentContextName]
	var currentContextAuthInfo *api.AuthInfo
	if currentContext != nil {
		currentContextAuthInfo = kc.startingConfig.AuthInfos[currentContext.AuthInfo]
	}
	kc.startingConfig.Clusters[workspaceContextName] = workspaceConfig.Clusters[workspaceConfigCurrentContext]

	i, workspaceAuthInfo := prioritizedAuthInfo(&kubectlOverrides.AuthInfo, currentContextAuthInfo)

	authInfoName := workspaceContextName

	// Only add override AuthInfo to kubeconfig
	if i == 0 { // The first item was chosen, which is the override
		kc.startingConfig.AuthInfos[workspaceContextName] = workspaceAuthInfo
	} else {
		authInfoName = kc.startingConfig.Contexts[kc.startingConfig.CurrentContext].AuthInfo
	}

	kc.startingConfig.Contexts[workspaceContextName] = &api.Context{
		Cluster:  workspaceContextName,
		AuthInfo: authInfoName,
	}

	kc.startingConfig.CurrentContext = workspaceContextName

	if err := write(opts, fmt.Sprintf("Current workspace is %q.\n", workspaceName)); err != nil {
		return err
	}
	return clientcmd.ModifyConfig(kc.configAccess, *kc.startingConfig, true)
}

// getCurrentWorkspace gets the current workspace from the kubeconfig.
func (kc *KubeConfig) getCurrentWorkspace(opts *Options) (scope string, name string, err error) {
	currentContextName := kc.startingConfig.CurrentContext

	kubectlOverrides := opts.KubectlOverrides
	if kubectlOverrides.CurrentContext != "" {
		currentContextName = kubectlOverrides.CurrentContext
	}

	if currentContextName == "" {
		return "", "", errors.New("no current context")
	}

	if !strings.HasPrefix(currentContextName, kcpWorkspaceContextNamePrefix) {
		return "", "", errors.New("the current context is not a KCP workspace")
	}

	scope, workspaceName := extractScopeAndName(strings.TrimPrefix(currentContextName, kcpWorkspaceContextNamePrefix))
	return scope, workspaceName, nil
}

// checkWorkspaceExists checks whether this workspace exsts in the
// user workspace directory, by requesting the `workspaces` virtual workspace.
func checkWorkspaceExists(ctx context.Context, workspaceName string, tenancyClient tenancyclient.Interface) error {
	_, err := tenancyClient.TenancyV1beta1().Workspaces().Get(ctx, workspaceName, metav1.GetOptions{})
	return err
}

// CurrentWorkspace outputs the current workspace.
func (kc *KubeConfig) CurrentWorkspace(ctx context.Context, opts *Options) error {
	workspaceDirectoryRestConfig, err := kc.workspaceDirectoryRestConfig(opts, true)
	if err != nil {
		return err
	}

	tenancyClient, err := tenancyclient.NewForConfig(workspaceDirectoryRestConfig)
	if err != nil {
		return err
	}

	_, workspaceName, err := kc.getCurrentWorkspace(opts)
	if err != nil {
		return err
	}

	outputCurrentWorkspaceMessage := func() error {
		if workspaceName != "" {
			err := write(opts, fmt.Sprintf("Current workspace is %q.\n", workspaceName))
			return err
		}
		return nil
	}

	if err := checkWorkspaceExists(ctx, workspaceName, tenancyClient); err != nil {
		_ = outputCurrentWorkspaceMessage()
		return err
	}

	return outputCurrentWorkspaceMessage()
}

// ListWorkspaces outputs the list of workspaces of the current user
// (kubeconfig user possibly overridden by CLI options).
func (kc *KubeConfig) ListWorkspaces(ctx context.Context, opts *Options) error {
	workspaceDirectoryRestConfig, err := kc.workspaceDirectoryRestConfig(opts, false)
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
	workspaceDirectoryRestConfig, err := kc.workspaceDirectoryRestConfig(opts, false)
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
	workspaceDirectoryRestConfig, err := kc.workspaceDirectoryRestConfig(opts, false)
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
