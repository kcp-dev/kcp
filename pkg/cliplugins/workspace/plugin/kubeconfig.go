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
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1beta1 "k8s.io/apimachinery/pkg/apis/meta/v1beta1"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	tenancyclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	workspacecmd "github.com/kcp-dev/kcp/pkg/virtual/framework/cmd"
	workspacebuilder "github.com/kcp-dev/kcp/pkg/virtual/workspaces/builder"
	workspaceregistry "github.com/kcp-dev/kcp/pkg/virtual/workspaces/registry"
)

const (
	kcpWorkspaceContextNamePrefix  string = "workspace.kcp.dev/"
	kcpPreviousWorkspaceContextKey string = "workspace.kcp.dev/-"
)

var defaultWorkspaceDirectoryApiServerPath = workspacebuilder.DefaultRootPathPrefix + "/" + workspaceregistry.PersonalScope

// KubeConfig contains a config loaded from a Kubeconfig
// and allows modifications on it through workspace-related
// actions
type KubeConfig struct {
	configAccess   clientcmd.ConfigAccess
	startingConfig *api.Config
}

// NewKubeConfig load a kubeconfig with default config access
func NewKubeConfig(overridingOptions *Options) (*KubeConfig, error) {
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

// ensureWorkspaceDirectoryContextExists tries to find a context in the kubeconfig
// that corresponds to the expected workspace directory context
// (thus pointing to the `workspaces` virtual workspace).
// If none is found produce one, based on the kubeconfig current context, overridden by
// by the workspace directory overrides.
// No Auth info is added in this workspace directory new context.
// The current kubeconfig is not modified though, but a copy of it is returned
func (kc *KubeConfig) ensureWorkspaceDirectoryContextExists(options *Options) (*api.Config, error) {
	workspaceDirectoryAwareConfig := kc.startingConfig.DeepCopy()
	currentContextName := workspaceDirectoryAwareConfig.CurrentContext
	if options.KubectlOverrides.CurrentContext != "" {
		currentContextName = options.KubectlOverrides.CurrentContext
	}
	currentContext := workspaceDirectoryAwareConfig.Contexts[currentContextName]

	workspaceDirectoryOverrides := options.WorkspaceDirectoryOverrides

	workspaceDirectoryContext := workspaceDirectoryAwareConfig.Contexts[workspaceDirectoryOverrides.CurrentContext]
	workspaceDirectoryCluster := workspaceDirectoryAwareConfig.Clusters[workspaceDirectoryOverrides.CurrentContext]
	if workspaceDirectoryContext == nil || workspaceDirectoryCluster == nil {

		if currentContext != nil {
			workspaceDirectoryCluster = workspaceDirectoryAwareConfig.Clusters[currentContext.Cluster].DeepCopy()
			workspaceDirectoryContext = &api.Context{
				Cluster: workspaceDirectoryOverrides.CurrentContext,
			}
		} else {
			workspaceDirectoryCluster = &api.Cluster{}
			workspaceDirectoryContext = &api.Context{
				Cluster: workspaceDirectoryOverrides.CurrentContext,
			}
		}
		if workspaceDirectoryOverrides.ClusterInfo.Server != "" {
			workspaceDirectoryCluster.Server = workspaceDirectoryOverrides.ClusterInfo.Server
		} else {
			currentServerURL, err := url.Parse(workspaceDirectoryCluster.Server)
			if err != nil {
				return nil, err
			}
			workspaceDirectoryCluster.Server = fmt.Sprintf("%s://%s:%d%s", currentServerURL.Scheme, currentServerURL.Hostname(), workspacecmd.SecurePortDefault, defaultWorkspaceDirectoryApiServerPath)
		}
		if workspaceDirectoryOverrides.ClusterInfo.CertificateAuthority != "" {
			workspaceDirectoryCluster.CertificateAuthority = workspaceDirectoryOverrides.ClusterInfo.CertificateAuthority
		}
		if workspaceDirectoryOverrides.ClusterInfo.TLSServerName != "" {
			workspaceDirectoryCluster.TLSServerName = workspaceDirectoryOverrides.ClusterInfo.TLSServerName
		}
		if workspaceDirectoryOverrides.ClusterInfo.InsecureSkipTLSVerify {
			workspaceDirectoryCluster.InsecureSkipTLSVerify = workspaceDirectoryOverrides.ClusterInfo.InsecureSkipTLSVerify
		}
		workspaceDirectoryAwareConfig.Clusters[workspaceDirectoryOverrides.CurrentContext] = workspaceDirectoryCluster
		workspaceDirectoryAwareConfig.Contexts[workspaceDirectoryOverrides.CurrentContext] = workspaceDirectoryContext
	}
	return workspaceDirectoryAwareConfig, nil
}

// workspaceDirectoryRestConfig returns the rest.Config to access the workspace directory
// virtual workspace based on the stored config and CLI overrides
func (kc *KubeConfig) workspaceDirectoryRestConfig(options *Options) (*rest.Config, error) {
	workpaceDirectoryAwareConfig, err := kc.ensureWorkspaceDirectoryContextExists(options)
	if err != nil {
		return nil, err
	}

	currentContextAuthInfo := &api.AuthInfo{}
	currentContext := workpaceDirectoryAwareConfig.Contexts[workpaceDirectoryAwareConfig.CurrentContext]
	if currentContext != nil {
		currentContextAuthInfo = workpaceDirectoryAwareConfig.AuthInfos[currentContext.AuthInfo]
	}

	workspaceDirectoryOverrides := options.WorkspaceDirectoryOverrides
	kubectlOverrides := options.KubectlOverrides

	overrides := &clientcmd.ConfigOverrides{
		AuthInfo:       *prioritizedAuthInfo(&workspaceDirectoryOverrides.AuthInfo, &kubectlOverrides.AuthInfo, currentContextAuthInfo),
		ClusterInfo:    workspaceDirectoryOverrides.ClusterInfo,
		CurrentContext: workspaceDirectoryOverrides.CurrentContext,
	}
	return clientcmd.NewDefaultClientConfig(*workpaceDirectoryAwareConfig, overrides).ClientConfig()
}

// UseWorkspace switch the current workspace to the given workspace.
// To do so it retrieves the ClusterWorkspace minimal KubeConfig (mainly cluster infos)
// from the `workspaces` virtual workspace `workspaces/kubeconfig` sube-resources,
// and adds it (along with the Auth info that is currently used) to the Kubeconfig.
// Then it make this new context the current context.
func (kc *KubeConfig) UseWorkspace(ctx context.Context, opts *Options, workspaceName string) error {

	workspaceDirectoryRestConfig, err := kc.workspaceDirectoryRestConfig(opts)
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

	workspaceKubeConfigBytes, err := tenancyClient.RESTClient().Get().Resource("workspaces").SubResource("kubeconfig").Name(workspaceName).Do(ctx).Raw()
	if err != nil {
		return err
	}

	workspaceConfig, err := clientcmd.Load(workspaceKubeConfigBytes)
	if err != nil {
		return err
	}

	workspaceConfigCurrentContext := workspaceConfig.CurrentContext
	scope, _ := extractScopeAndName(workspaceConfigCurrentContext)
	workspaceContextName := kcpWorkspaceContextNamePrefix + workspaceConfigCurrentContext

	currentContextName := kc.startingConfig.CurrentContext
	if opts.KubectlOverrides.CurrentContext != "" {
		currentContextName = opts.KubectlOverrides.CurrentContext
	}

	currentContext := kc.startingConfig.Contexts[currentContextName]
	var currentContextAuthInfo *api.AuthInfo
	if currentContext != nil {
		currentContextAuthInfo = kc.startingConfig.AuthInfos[currentContext.AuthInfo]
	}
	kc.startingConfig.Clusters[workspaceContextName] = workspaceConfig.Clusters[workspaceConfigCurrentContext]

	kubectlOverrides := opts.KubectlOverrides

	workspaceAuthInfo := prioritizedAuthInfo(&kubectlOverrides.AuthInfo, currentContextAuthInfo)
	kc.startingConfig.AuthInfos[workspaceContextName] = workspaceAuthInfo

	kc.startingConfig.Contexts[workspaceContextName] = &api.Context{
		Cluster:  workspaceContextName,
		AuthInfo: workspaceContextName,
	}

	kc.startingConfig.CurrentContext = workspaceContextName

	if err := write(opts, fmt.Sprintf("Current %s workspace is \"%s\".\n", scope, workspaceName)); err != nil {
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
		return "", "", errors.New("No current context !")
	}

	if !strings.HasPrefix(currentContextName, kcpWorkspaceContextNamePrefix) {
		return "", "", errors.New("The current context is not a KCP workspace !")
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
	workspaceDirectoryRestConfig, err := kc.workspaceDirectoryRestConfig(opts)
	if err != nil {
		return err
	}

	tenancyClient, err := tenancyclient.NewForConfig(workspaceDirectoryRestConfig)
	if err != nil {
		return err
	}

	scope, workspaceName, err := kc.getCurrentWorkspace(opts)
	if err != nil {
		return err
	}

	outputCurrentWorkspaceMessage := func() error {
		if workspaceName != "" {
			err := write(opts, fmt.Sprintf("Current %s workspace is \"%s\".\n", scope, workspaceName))
			return err
		}
		return nil
	}

	// Check that the scope is consistent with the workspace-directory config
	if !strings.HasSuffix(workspaceDirectoryRestConfig.Host, "/"+scope) {
		_ = outputCurrentWorkspaceMessage()
		return fmt.Errorf("Scope of the workspace-directory ('%s') doesn't match the scope of the workspace-directory server ('%s').\nCannot check the current workspace existence.", scope, workspaceDirectoryRestConfig.Host)
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
	workspaceDirectoryRestConfig, err := kc.workspaceDirectoryRestConfig(opts)
	if err != nil {
		return err
	}

	tenancyClient, err := tenancyclient.NewForConfig(workspaceDirectoryRestConfig)
	if err != nil {
		return err
	}

	result := tenancyClient.RESTClient().Get().Resource("workspaces").SetHeader("Accept", strings.Join([]string{
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
	workspaceDirectoryRestConfig, err := kc.workspaceDirectoryRestConfig(opts)
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
	workspaceDirectoryRestConfig, err := kc.workspaceDirectoryRestConfig(opts)
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
