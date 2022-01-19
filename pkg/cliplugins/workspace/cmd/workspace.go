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

package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	tenancyclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/tenancy/v1alpha1"
)

var (
	workspaceExample = `
	# Shows the workspace you are currently using
	%[1]s workspace current

	# use a given workspace (this will change the current-context of your current KUBECONFIG)
	%[1]s workspace use

	# list all your personal workspaces
	%[1]s workspace list
`

	errNoContext = fmt.Errorf("no context is currently set, use %q to select a new one", "kubectl config use-context <context>")
)

const (
	kcpWorkspaceContextNamePrefix string = "workspace.kcp.dev/"
)

// WorkspaceOptions provides information required to update
// the current context on a user's KUBECONFIG
type WorkspaceOptions struct {
	workspaceDirectoryOverrides *clientcmd.ConfigOverrides
	kubectlOverrides            *clientcmd.ConfigOverrides

	configAccess   clientcmd.ConfigAccess
	startingConfig *api.Config

	genericclioptions.IOStreams
}

// NewWorkspaceOptions provides an instance of WorkspaceOptions with default values
func NewWorkspaceOptions(streams genericclioptions.IOStreams) *WorkspaceOptions {
	return &WorkspaceOptions{
		workspaceDirectoryOverrides: &clientcmd.ConfigOverrides{},
		kubectlOverrides:            &clientcmd.ConfigOverrides{},

		IOStreams: streams,
	}
}

// NewCmdWorkspace provides a cobra command wrapping WorkspaceOptions
func NewCmdWorkspace(streams genericclioptions.IOStreams) (*cobra.Command, error) {
	o := NewWorkspaceOptions(streams)

	cmd := &cobra.Command{
		Use:              "workspace [--workspaces-server-url=] <current|use|list>",
		Short:            "Manages KCP workspaces",
		Example:          fmt.Sprintf(workspaceExample, "kubectl kcp"),
		SilenceUsage:     true,
		TraverseChildren: true,
	}

	if err := o.Complete(cmd); err != nil {
		return nil, err
	}

	useCmd := &cobra.Command{
		Use:          "use < workspace name | - >",
		Short:        "Uses the given workspace as the current workspace. Using - means previous workspace",
		Example:      "kcp workspace use my-worspace",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("The workspace name (or -) should be given")
			}

			if err := o.Validate(); err != nil {
				return err
			}

			if err := o.RunUse(args[0]); err != nil {
				return err
			}
			return nil
		},
	}

	currentCmd := &cobra.Command{
		Use:          "current",
		Short:        "Returns the name of the current workspace",
		Example:      "kcp workspace current",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}

			if err := o.RunCurrent(); err != nil {
				return err
			}
			return nil
		},
	}

	cmd.AddCommand(useCmd)
	cmd.AddCommand(currentCmd)
	return cmd, nil
}

func (o *WorkspaceOptions) Complete(cmd *cobra.Command) error {
	kubectlConfigOverrideFlags := clientcmd.RecommendedConfigOverrideFlags("")
	kubectlConfigOverrideFlags.AuthOverrideFlags.ClientCertificate.LongName = ""
	kubectlConfigOverrideFlags.AuthOverrideFlags.ClientKey.LongName = ""
	kubectlConfigOverrideFlags.AuthOverrideFlags.Impersonate.LongName = ""
	kubectlConfigOverrideFlags.AuthOverrideFlags.ImpersonateGroups.LongName = ""
	kubectlConfigOverrideFlags.ContextOverrideFlags.AuthInfoName.LongName = ""
	kubectlConfigOverrideFlags.ContextOverrideFlags.ClusterName.LongName = ""
	kubectlConfigOverrideFlags.ContextOverrideFlags.Namespace.LongName = ""
	kubectlConfigOverrideFlags.Timeout.LongName = ""

	clientcmd.BindOverrideFlags(o.kubectlOverrides, cmd.Flags(), kubectlConfigOverrideFlags)

	descriptionSuffix := " for workspace directory context"
	workspaceDirectoryConfigOverrideFlags := clientcmd.RecommendedConfigOverrideFlags("workspace-directory-")

	workspaceDirectoryConfigOverrideFlags.AuthOverrideFlags.ClientCertificate.LongName = ""
	workspaceDirectoryConfigOverrideFlags.AuthOverrideFlags.ClientKey.LongName = ""
	workspaceDirectoryConfigOverrideFlags.AuthOverrideFlags.Impersonate.LongName = ""
	workspaceDirectoryConfigOverrideFlags.AuthOverrideFlags.ImpersonateGroups.LongName = ""
	workspaceDirectoryConfigOverrideFlags.AuthOverrideFlags.Password.Description += descriptionSuffix
	workspaceDirectoryConfigOverrideFlags.AuthOverrideFlags.Token.Description += descriptionSuffix
	workspaceDirectoryConfigOverrideFlags.AuthOverrideFlags.Username.Description += descriptionSuffix

	workspaceDirectoryConfigOverrideFlags.ContextOverrideFlags.AuthInfoName.LongName = ""
	workspaceDirectoryConfigOverrideFlags.ContextOverrideFlags.ClusterName.LongName = ""
	workspaceDirectoryConfigOverrideFlags.ContextOverrideFlags.Namespace.LongName = ""

	workspaceDirectoryConfigOverrideFlags.ClusterOverrideFlags.APIVersion.LongName = ""
	workspaceDirectoryConfigOverrideFlags.ClusterOverrideFlags.APIServer.Description += descriptionSuffix
	workspaceDirectoryConfigOverrideFlags.ClusterOverrideFlags.APIServer.Default = "https://127.0.0.1:6444/services/applications/personal"
	workspaceDirectoryConfigOverrideFlags.ClusterOverrideFlags.CertificateAuthority.Description += descriptionSuffix
	workspaceDirectoryConfigOverrideFlags.ClusterOverrideFlags.InsecureSkipTLSVerify.Description += descriptionSuffix
	workspaceDirectoryConfigOverrideFlags.ClusterOverrideFlags.TLSServerName.Description += descriptionSuffix

	workspaceDirectoryConfigOverrideFlags.CurrentContext.Description += descriptionSuffix
	workspaceDirectoryConfigOverrideFlags.CurrentContext.Default = "workspace-directory"
	workspaceDirectoryConfigOverrideFlags.Timeout.LongName = ""

	clientcmd.BindOverrideFlags(o.workspaceDirectoryOverrides, cmd.Flags(), workspaceDirectoryConfigOverrideFlags)
	o.configAccess = clientcmd.NewDefaultClientConfigLoadingRules()

	var err error
	o.startingConfig, err = o.configAccess.GetStartingConfig()
	if err != nil {
		return err
	}
	return nil
}

// Validate ensures that all required arguments and flag values are provided
func (o *WorkspaceOptions) Validate() error {
	return nil
}

func (o *WorkspaceOptions) kubectlClient() (*rest.Config, error) {
	return clientcmd.NewDefaultClientConfig(*o.startingConfig, o.kubectlOverrides).ClientConfig()
}

func (o *WorkspaceOptions) ensureWorkspaceDirectoryContextExists() error {
	currentContext := o.startingConfig.Contexts[o.startingConfig.CurrentContext]

	workspaceDirectoryContext := o.startingConfig.Contexts[o.workspaceDirectoryOverrides.CurrentContext]
	workspaceDirectoryCluster := o.startingConfig.Clusters[o.workspaceDirectoryOverrides.CurrentContext]
	if workspaceDirectoryContext == nil || workspaceDirectoryCluster == nil {
		if currentContext != nil {
			workspaceDirectoryCluster = o.startingConfig.Clusters[currentContext.Cluster].DeepCopy()
			workspaceDirectoryContext = &api.Context{
				Cluster: o.workspaceDirectoryOverrides.CurrentContext,
			}
		} else {
			workspaceDirectoryCluster = &api.Cluster{}
			workspaceDirectoryContext = &api.Context{
				Cluster: o.workspaceDirectoryOverrides.CurrentContext,
			}
		}
		workspaceDirectoryCluster.Server = o.workspaceDirectoryOverrides.ClusterInfo.Server
		if o.workspaceDirectoryOverrides.ClusterInfo.CertificateAuthority != "" {
			workspaceDirectoryCluster.CertificateAuthority = o.workspaceDirectoryOverrides.ClusterInfo.CertificateAuthority
		}
		if o.workspaceDirectoryOverrides.ClusterInfo.TLSServerName != "" {
			workspaceDirectoryCluster.TLSServerName = o.workspaceDirectoryOverrides.ClusterInfo.TLSServerName
		}
		if o.workspaceDirectoryOverrides.ClusterInfo.InsecureSkipTLSVerify == true {
			workspaceDirectoryCluster.InsecureSkipTLSVerify = o.workspaceDirectoryOverrides.ClusterInfo.InsecureSkipTLSVerify
		}
		o.startingConfig.Clusters[o.workspaceDirectoryOverrides.CurrentContext] = workspaceDirectoryCluster
		o.startingConfig.Contexts[o.workspaceDirectoryOverrides.CurrentContext] = workspaceDirectoryContext

		return clientcmd.ModifyConfig(o.configAccess, *o.startingConfig, true)
	}
	return nil
}

func PrioritizedAuthInfo(values ...*api.AuthInfo) *api.AuthInfo {
	for _, value := range values {
		value := *value
		if value.Token != "" || value.TokenFile != "" || value.Password != "" || value.Username != "" {
			return &value
		}
	}
	return api.NewAuthInfo()
}

func (o *WorkspaceOptions) WorkspaceDirectoryRestConfig() (*rest.Config, error) {
	if err := o.ensureWorkspaceDirectoryContextExists(); err != nil {
		return nil, err
	}

	currentContextAuthInfo := &api.AuthInfo{}
	currentContext := o.startingConfig.Contexts[o.startingConfig.CurrentContext]
	if currentContext != nil {
		currentContextAuthInfo = o.startingConfig.AuthInfos[currentContext.AuthInfo]
	}

	overrides := &clientcmd.ConfigOverrides{
		AuthInfo:       *PrioritizedAuthInfo(&o.workspaceDirectoryOverrides.AuthInfo, &o.kubectlOverrides.AuthInfo, currentContextAuthInfo),
		ClusterInfo:    o.workspaceDirectoryOverrides.ClusterInfo,
		CurrentContext: o.workspaceDirectoryOverrides.CurrentContext,
	}
	return clientcmd.NewDefaultClientConfig(*o.startingConfig, overrides).ClientConfig()
}

func (o *WorkspaceOptions) RunUse(workspaceName string) error {

	workspaceDirectoryRestConfig, err := o.WorkspaceDirectoryRestConfig()
	if err != nil {
		return err
	}

	tenancyClient, err := tenancyclient.NewForConfig(workspaceDirectoryRestConfig)
	if err != nil {
		return err
	}

	workspaceKubeConfigBytes, err := tenancyClient.RESTClient().Get().Resource("workspaces").SubResource("kubeconfig").Name(workspaceName).Do(context.TODO()).Raw()
	if err != nil {
		return err
	}

	workspaceConfig, err := clientcmd.Load(workspaceKubeConfigBytes)
	if err != nil {
		return err
	}

	workspaceContextName := kcpWorkspaceContextNamePrefix + workspaceName

	currentContextName := o.startingConfig.CurrentContext
	if o.kubectlOverrides.CurrentContext != "" {
		currentContextName = o.kubectlOverrides.CurrentContext
	}

	currentContext := o.startingConfig.Contexts[currentContextName]
	currentContextAuthInfo := o.startingConfig.AuthInfos[currentContext.AuthInfo]
	o.startingConfig.Clusters[workspaceContextName] = workspaceConfig.Clusters[workspaceName]
	workspaceAuthInfo := PrioritizedAuthInfo(&o.kubectlOverrides.AuthInfo, currentContextAuthInfo)
	o.startingConfig.AuthInfos[workspaceContextName] = workspaceAuthInfo

	o.startingConfig.Contexts[workspaceContextName] = &api.Context{
		Cluster:  workspaceContextName,
		AuthInfo: workspaceContextName,
	}

	o.startingConfig.CurrentContext = workspaceContextName

	o.Out.Write([]byte(fmt.Sprintf("Current workspace is \"%s\".\n", workspaceName)))
	return clientcmd.ModifyConfig(o.configAccess, *o.startingConfig, true)
}

func (o *WorkspaceOptions) RunCurrent() error {
	workspaceDirectoryRestConfig, err := o.WorkspaceDirectoryRestConfig()
	if err != nil {
		return err
	}

	tenancyClient, err := tenancyclient.NewForConfig(workspaceDirectoryRestConfig)
	if err != nil {
		return err
	}

	currentContextName := o.startingConfig.CurrentContext
	if o.kubectlOverrides.CurrentContext != "" {
		currentContextName = o.kubectlOverrides.CurrentContext
	}

	if currentContextName == "" {
		return errors.New("No current context !")
	}

	if !strings.HasPrefix(currentContextName, kcpWorkspaceContextNamePrefix) {
		return errors.New("The current context is not a KCP workspace !")
	}

	workspaceName := strings.TrimPrefix(currentContextName, kcpWorkspaceContextNamePrefix)

	if _, err := tenancyClient.Workspaces().Get(context.TODO(), workspaceName, metav1.GetOptions{}); err != nil {
		o.Out.Write([]byte(fmt.Sprintf("Current workspace is \"%s\".\n", workspaceName)))
		return err
	}

	o.Out.Write([]byte(fmt.Sprintf("Current workspace is \"%s\".\n", workspaceName)))
	return nil
}
