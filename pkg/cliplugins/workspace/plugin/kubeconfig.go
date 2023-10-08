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
	"io"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/spf13/cobra"
	"github.com/xlab/treeprint"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/kcp/pkg/cliplugins/base"
	pluginhelpers "github.com/kcp-dev/kcp/pkg/cliplugins/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
)

const (
	kcpPreviousWorkspaceContextKey string = "workspace.kcp.io/previous"
	kcpCurrentWorkspaceContextKey  string = "workspace.kcp.io/current"
)

// UseWorkspaceOptions contains options for manipulating or showing the current workspace.
type UseWorkspaceOptions struct {
	*base.Options

	// Name is the name of the workspace to switch to.
	Name string
	// ShortWorkspaceOutput indicates only the workspace name should be printed.
	ShortWorkspaceOutput bool

	kcpClusterClient kcpclientset.ClusterInterface
	startingConfig   *clientcmdapi.Config

	// for testing
	modifyConfig   func(configAccess clientcmd.ConfigAccess, newConfig *clientcmdapi.Config) error
	getAPIBindings func(ctx context.Context, kcpClusterClient kcpclientset.ClusterInterface, host string) ([]apisv1alpha1.APIBinding, error)
}

// NewUseWorkspaceOptions returns a new UseWorkspaceOptions.
func NewUseWorkspaceOptions(streams genericclioptions.IOStreams) *UseWorkspaceOptions {
	return &UseWorkspaceOptions{
		Options: base.NewOptions(streams),

		modifyConfig: func(configAccess clientcmd.ConfigAccess, newConfig *clientcmdapi.Config) error {
			return clientcmd.ModifyConfig(configAccess, *newConfig, true)
		},
		getAPIBindings: getAPIBindings,
	}
}

// Complete ensures all dynamically populated fields are initialized.
func (o *UseWorkspaceOptions) Complete(args []string) error {
	if err := o.Options.Complete(); err != nil {
		return err
	}

	if o.Name == "" && len(args) > 0 {
		o.Name = args[0]
	}

	var err error
	o.startingConfig, err = o.ClientConfig.ConfigAccess().GetStartingConfig()
	if err != nil {
		return err
	}

	kcpClusterClient, err := newKCPClusterClient(o.ClientConfig)
	if err != nil {
		return err
	}
	o.kcpClusterClient = kcpClusterClient

	return nil
}

// Validate validates the UseWorkspaceOptions are complete and usable.
func (o *UseWorkspaceOptions) Validate() error {
	return o.Options.Validate()
}

// BindFlags binds fields to cmd's flagset.
func (o *UseWorkspaceOptions) BindFlags(cmd *cobra.Command) {
	o.Options.BindFlags(cmd)
	cmd.Flags().BoolVar(&o.ShortWorkspaceOutput, "short", o.ShortWorkspaceOutput, "Print only the name of the workspace, e.g. for integration into the shell prompt")
}

// Run executes the "use workspace" logic based on the supplied options.
func (o *UseWorkspaceOptions) Run(ctx context.Context) error {
	home, _ := os.UserHomeDir()
	rawConfig, err := o.ClientConfig.RawConfig()
	if err != nil {
		return err
	}

	// Store the currentContext content for later to set as previous context
	currentContext, found := o.startingConfig.Contexts[rawConfig.CurrentContext]
	if !found {
		return fmt.Errorf("current %q context not found", rawConfig.CurrentContext)
	}

	var newServerHost string
	var workspaceType *tenancyv1alpha1.WorkspaceTypeReference
	switch o.Name {
	case "-":
		prev, exists := o.startingConfig.Contexts[kcpPreviousWorkspaceContextKey]
		if !exists {
			return errors.New("no previous workspace found in kubeconfig")
		}

		newKubeConfig := o.startingConfig.DeepCopy()
		if currentContext.Cluster == kcpCurrentWorkspaceContextKey {
			oldCluster, found := o.startingConfig.Clusters[currentContext.Cluster]
			if !found {
				return fmt.Errorf("cluster %q not found in kubeconfig", currentContext.Cluster)
			}
			currentContext = currentContext.DeepCopy()
			currentContext.Cluster = kcpPreviousWorkspaceContextKey
			newKubeConfig.Clusters[kcpPreviousWorkspaceContextKey] = oldCluster
		}
		if prev.Cluster == kcpPreviousWorkspaceContextKey {
			prevCluster, found := o.startingConfig.Clusters[prev.Cluster]
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

		if err := o.modifyConfig(o.ClientConfig.ConfigAccess(), newKubeConfig); err != nil {
			return err
		}

		newServerHost = newKubeConfig.Clusters[newKubeConfig.Contexts[kcpCurrentWorkspaceContextKey].Cluster].Server

		bindings, err := o.getAPIBindings(ctx, o.kcpClusterClient, newServerHost)
		if err != nil {
			// display the error, but don't stop the current workspace from being reported.
			fmt.Fprintf(o.ErrOut, "error checking APIBindings: %v\n", err)
		}
		if err = findUnresolvedPermissionClaims(o.Out, bindings); err != nil {
			// display the error, but don't stop the current workspace from being reported.
			fmt.Fprintf(o.ErrOut, "error checking APIBindings: %v\n", err)
		}

		return currentWorkspace(o.Out, newServerHost, shortWorkspaceOutput(o.ShortWorkspaceOutput), nil)

	case "..":
		config, err := o.ClientConfig.ClientConfig()
		if err != nil {
			return err
		}
		u, currentClusterName, err := pluginhelpers.ParseClusterURL(config.Host)
		if err != nil {
			return fmt.Errorf("current URL %q does not point to a workspace", config.Host)
		}
		parentClusterName, hasParent := currentClusterName.Parent()
		if !hasParent {
			if currentClusterName == core.RootCluster.Path() {
				return fmt.Errorf("current workspace is %q", currentClusterName)
			}
			return fmt.Errorf("current workspace %q has no parent", currentClusterName)
		}
		u.Path = path.Join(u.Path, parentClusterName.RequestPath())
		newServerHost = u.String()

	case "":
		defer func() {
			if err == nil {
				_, err = fmt.Fprintf(o.Out, "Note: 'kubectl ws' now matches 'cd' semantics: go to home workspace. 'kubectl ws -' to go back. 'kubectl ws .' to print current workspace.\n")
			}
		}()
		fallthrough

	case "~", home:
		homeWorkspace, err := o.kcpClusterClient.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Get(ctx, "~", metav1.GetOptions{})
		if err != nil {
			return err
		}
		newServerHost = homeWorkspace.Spec.URL

	case ".":
		cfg, err := o.ClientConfig.ClientConfig()
		if err != nil {
			return err
		}
		return currentWorkspace(o.Out, cfg.Host, shortWorkspaceOutput(o.ShortWorkspaceOutput), nil)

	default:
		cluster := logicalcluster.NewPath(o.Name)
		if !cluster.IsValid() {
			return fmt.Errorf("invalid workspace name format: %s", o.Name)
		}
		config, err := o.ClientConfig.ClientConfig()
		if err != nil {
			return err
		}
		u, currentClusterName, err := pluginhelpers.ParseClusterURL(config.Host)
		if err != nil {
			return fmt.Errorf("current URL %q does not point to a workspace", config.Host)
		}

		if strings.Contains(o.Name, ":") && cluster.HasPrefix(logicalcluster.NewPath("system")) {
			// e.g. system:something
			u.Path = path.Join(u.Path, cluster.RequestPath())
			newServerHost = u.String()
		} else if strings.Contains(o.Name, ":") {
			// e.g. root:something:something

			// first try to get Workspace from parent to potentially get a 404. A 403 in the parent though is
			// not a blocker to enter the workspace. We will do discovery as a final check below
			parentClusterName, workspaceName := logicalcluster.NewPath(o.Name).Split()
			if _, err := o.kcpClusterClient.Cluster(parentClusterName).TenancyV1alpha1().Workspaces().Get(ctx, workspaceName, metav1.GetOptions{}); apierrors.IsNotFound(err) {
				return fmt.Errorf("workspace %q not found", o.Name)
			}

			groups, err := o.kcpClusterClient.Cluster(cluster).Discovery().ServerGroups()
			if err != nil && !apierrors.IsForbidden(err) {
				return err
			}
			if apierrors.IsForbidden(err) || len(groups.Groups) == 0 {
				return fmt.Errorf("access to workspace %s denied", o.Name)
			}

			// TODO(sttts): in both the cases of `root` and absolute paths here we assume that the current cluster
			//              client is talking to the right external URL. This obviously not guaranteed, and hence
			//              we silently assume that the front-proxy will route to every workspace.
			//			    We might want to add permanent redirections to the front-proxy if the external
			//              URL does not match the workspace's shard, and then add redirect support here to
			//              use the right front-proxy URL in the kubeconfig.

			u.Path = path.Join(u.Path, cluster.RequestPath())
			newServerHost = u.String()
		} else if o.Name == core.RootCluster.String() {
			// root workspace
			u.Path = path.Join(u.Path, cluster.RequestPath())
			newServerHost = u.String()
		} else {
			// relative logical cluster, get URL from workspace object in current context
			ws, err := o.kcpClusterClient.Cluster(currentClusterName).TenancyV1alpha1().Workspaces().Get(ctx, o.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if ws.Status.Phase != corev1alpha1.LogicalClusterPhaseReady {
				return fmt.Errorf("workspace %q is not ready", o.Name)
			}

			config, err := o.ClientConfig.ClientConfig()
			if err != nil {
				return err
			}
			u, currentClusterName, err := pluginhelpers.ParseClusterURL(config.Host)
			if err != nil {
				return fmt.Errorf("current URL %q does not point to a workspace", config.Host)
			}

			u.Path = path.Join(u.Path, currentClusterName.Join(ws.Name).RequestPath())
			newServerHost = u.String()
			workspaceType = &ws.Spec.Type
		}
	}

	// modify kubeconfig, using the "workspace" context and cluster
	newKubeConfig := o.startingConfig.DeepCopy()
	oldCluster, found := o.startingConfig.Clusters[currentContext.Cluster]
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

	if err := o.modifyConfig(o.ClientConfig.ConfigAccess(), newKubeConfig); err != nil {
		return err
	}

	bindings, err := o.getAPIBindings(ctx, o.kcpClusterClient, newServerHost)
	if err != nil {
		// display the error, but don't stop the current workspace from being reported.
		fmt.Fprintf(o.ErrOut, "error checking APIBindings: %v\n", err)
	}
	if err := findUnresolvedPermissionClaims(o.Out, bindings); err != nil {
		// display the error, but don't stop the current workspace from being reported.
		fmt.Fprintf(o.ErrOut, "error checking APIBindings: %v\n", err)
	}

	return currentWorkspace(o.Out, newServerHost, shortWorkspaceOutput(o.ShortWorkspaceOutput), workspaceType)
}

// getAPIBindings retrieves APIBindings within the workspace.
func getAPIBindings(ctx context.Context, kcpClusterClient kcpclientset.ClusterInterface, host string) ([]apisv1alpha1.APIBinding, error) {
	_, clusterName, err := pluginhelpers.ParseClusterURL(host)
	if err != nil {
		return nil, err
	}

	apiBindings, err := kcpClusterClient.Cluster(clusterName).ApisV1alpha1().APIBindings().List(ctx, metav1.ListOptions{})
	// If the user is not allowed to view APIBindings in the workspace, there's nothing to show.
	if apierrors.IsForbidden(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return apiBindings.Items, nil
}

// findUnresolvedPermissionClaims finds and reports any APIBindings that do not specify permission claims matching those on the target APIExport.
func findUnresolvedPermissionClaims(out io.Writer, apiBindings []apisv1alpha1.APIBinding) error {
	for _, binding := range apiBindings {
		for _, exportedClaim := range binding.Status.ExportPermissionClaims {
			var found, ack bool
			for _, specClaim := range binding.Spec.PermissionClaims {
				if !exportedClaim.Equal(specClaim.PermissionClaim) {
					continue
				}
				found = true
				ack = (specClaim.State == apisv1alpha1.ClaimAccepted) || specClaim.State == apisv1alpha1.ClaimRejected
			}
			if !found {
				fmt.Fprintf(out, "Warning: claim for %s exported but not specified on APIBinding %s\nAdd this claim to the APIBinding's Spec.\n", exportedClaim.String(), binding.Name)
			}
			if !ack {
				fmt.Fprintf(out, "Warning: claim for %s specified on APIBinding %s but not accepted or rejected.\n", exportedClaim.String(), binding.Name)
			}
		}
	}
	return nil
}

// CurrentWorkspaceOptions contains options for displaying the current workspace.
type CurrentWorkspaceOptions struct {
	*base.Options

	// ShortWorkspaceOutput indicates only the workspace name should be printed.
	ShortWorkspaceOutput bool
}

// NewCurrentWorkspaceOptions returns a new CurrentWorkspaceOptions.
func NewCurrentWorkspaceOptions(streams genericclioptions.IOStreams) *CurrentWorkspaceOptions {
	return &CurrentWorkspaceOptions{
		Options: base.NewOptions(streams),
	}
}

// BindFlags binds fields to cmd's flagset.
func (o *CurrentWorkspaceOptions) BindFlags(cmd *cobra.Command) {
	o.Options.BindFlags(cmd)
	cmd.Flags().BoolVar(&o.ShortWorkspaceOutput, "short", o.ShortWorkspaceOutput, "Print only the name of the workspace, e.g. for integration into the shell prompt")
}

// Run outputs the current workspace.
func (o *CurrentWorkspaceOptions) Run(ctx context.Context) error {
	cfg, err := o.ClientConfig.ClientConfig()
	if err != nil {
		return err
	}

	return currentWorkspace(o.Out, cfg.Host, shortWorkspaceOutput(o.ShortWorkspaceOutput), nil)
}

type shortWorkspaceOutput bool

func currentWorkspace(out io.Writer, host string, shortWorkspaceOutput shortWorkspaceOutput, workspaceType *tenancyv1alpha1.WorkspaceTypeReference) error {
	_, clusterName, err := pluginhelpers.ParseClusterURL(host)
	if err != nil {
		if shortWorkspaceOutput {
			return nil
		}
		_, err = fmt.Fprintf(out, "Current workspace is the URL %q.\n", host)
		return err
	}

	if shortWorkspaceOutput {
		_, err = fmt.Fprintf(out, "%s\n", clusterName)
		return err
	}

	message := fmt.Sprintf("Current workspace is %q", clusterName)
	if workspaceType != nil {
		message += fmt.Sprintf(" (type %s)", logicalcluster.NewPath(workspaceType.Path).Join(string(workspaceType.Name)).String())
	}
	_, err = fmt.Fprintln(out, message+".")
	return err
}

// CreateWorkspaceOptions contains options for creating a new workspace.
type CreateWorkspaceOptions struct {
	*base.Options

	// Name is the name of the workspace to create.
	Name string
	// Type is the type of the workspace to create.
	Type string
	// EnterAfterCreate enters the newly created workspace if true.
	EnterAfterCreate bool
	// IgnoreExisting ignores errors if the workspace already exists.
	IgnoreExisting bool
	// ReadyWaitTimeout is how long to wait for the workspace to be ready before returning control to the user.
	ReadyWaitTimeout time.Duration
	// LocationSelector is the location selector to use when creating the workspace to select a matching shard.
	LocationSelector string

	kcpClusterClient kcpclientset.ClusterInterface

	// for testing - passed to UseWorkspaceOptions
	modifyConfig func(configAccess clientcmd.ConfigAccess, newConfig *clientcmdapi.Config) error
}

// NewCreateWorkspaceOptions returns a new CreateWorkspaceOptions.
func NewCreateWorkspaceOptions(streams genericclioptions.IOStreams) *CreateWorkspaceOptions {
	return &CreateWorkspaceOptions{
		Options: base.NewOptions(streams),

		ReadyWaitTimeout: time.Minute,
	}
}

// Complete ensures all dynamically populated fields are initialized.
func (o *CreateWorkspaceOptions) Complete(args []string) error {
	if err := o.Options.Complete(); err != nil {
		return err
	}

	if len(args) > 0 {
		o.Name = args[0]
	}

	kcpClusterClient, err := newKCPClusterClient(o.ClientConfig)
	if err != nil {
		return err
	}
	o.kcpClusterClient = kcpClusterClient

	return nil
}

// Validate validates the CreateWorkspaceOptions are complete and usable.
func (o *CreateWorkspaceOptions) Validate() error {
	if _, err := metav1.ParseToLabelSelector(o.LocationSelector); err != nil {
		return fmt.Errorf("invalid location selector: %w", err)
	}

	return o.Options.Validate()
}

// BindFlags binds fields to cmd's flagset.
func (o *CreateWorkspaceOptions) BindFlags(cmd *cobra.Command) {
	o.Options.BindFlags(cmd)
	cmd.Flags().StringVar(&o.Type, "type", o.Type, "A workspace type. The default type depends on where this child workspace is created.")
	cmd.Flags().BoolVar(&o.EnterAfterCreate, "enter", o.EnterAfterCreate, "Immediately enter the created workspace")
	cmd.Flags().BoolVar(&o.IgnoreExisting, "ignore-existing", o.IgnoreExisting, "Ignore if the workspace already exists. Requires none or absolute type path.")
	cmd.Flags().StringVar(&o.LocationSelector, "location-selector", o.LocationSelector, "A label selector to select the scheduling location of the created workspace.")
}

// Run creates a workspace.
func (o *CreateWorkspaceOptions) Run(ctx context.Context) error {
	config, err := o.ClientConfig.ClientConfig()
	if err != nil {
		return err
	}
	_, currentClusterName, err := pluginhelpers.ParseClusterURL(config.Host)
	if err != nil {
		return fmt.Errorf("current URL %q does not point to a workspace", config.Host)
	}

	if o.IgnoreExisting && o.Type != "" && !logicalcluster.NewPath(o.Type).HasPrefix(core.RootCluster.Path()) {
		return fmt.Errorf("--ignore-existing must not be used with non-absolute type path")
	}

	var structuredWorkspaceType tenancyv1alpha1.WorkspaceTypeReference
	if o.Type != "" {
		separatorIndex := strings.LastIndex(o.Type, ":")
		switch separatorIndex {
		case -1:
			structuredWorkspaceType = tenancyv1alpha1.WorkspaceTypeReference{
				Name: tenancyv1alpha1.WorkspaceTypeName(strings.ToLower(o.Type)),
				// path is defaulted through admission
			}
		default:
			structuredWorkspaceType = tenancyv1alpha1.WorkspaceTypeReference{
				Name: tenancyv1alpha1.WorkspaceTypeName(strings.ToLower(o.Type[separatorIndex+1:])),
				Path: o.Type[:separatorIndex],
			}
		}
	}

	ws := &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name: o.Name,
		},
		Spec: tenancyv1alpha1.WorkspaceSpec{
			Type: structuredWorkspaceType,
		},
	}

	if o.LocationSelector != "" {
		selector, err := metav1.ParseToLabelSelector(o.LocationSelector)
		if err != nil {
			return err
		}

		ws.Spec.Location = &tenancyv1alpha1.WorkspaceLocation{
			Selector: selector,
		}
	}

	preExisting := false
	ws, err = o.kcpClusterClient.Cluster(currentClusterName).TenancyV1alpha1().Workspaces().Create(ctx, ws, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) && o.IgnoreExisting {
		preExisting = true
		ws, err = o.kcpClusterClient.Cluster(currentClusterName).TenancyV1alpha1().Workspaces().Get(ctx, o.Name, metav1.GetOptions{})
	}
	if err != nil {
		return err
	}

	workspaceReference := fmt.Sprintf("Workspace %q (type %s)", o.Name, logicalcluster.NewPath(ws.Spec.Type.Path).Join(string(ws.Spec.Type.Name)).String())
	if preExisting {
		if ws.Spec.Type.Name != "" && ws.Spec.Type.Name != structuredWorkspaceType.Name || ws.Spec.Type.Path != structuredWorkspaceType.Path {
			wsTypeString := logicalcluster.NewPath(ws.Spec.Type.Path).Join(string(ws.Spec.Type.Name)).String()
			structuredWorkspaceTypeString := logicalcluster.NewPath(structuredWorkspaceType.Path).Join(string(structuredWorkspaceType.Name)).String()
			return fmt.Errorf("workspace %q cannot be created with type %s, it already exists with different type %s", o.Name, structuredWorkspaceTypeString, wsTypeString)
		}
		if ws.Status.Phase != corev1alpha1.LogicalClusterPhaseReady && o.ReadyWaitTimeout > 0 {
			if _, err := fmt.Fprintf(o.Out, "%s already exists. Waiting for it to be ready...\n", workspaceReference); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintf(o.Out, "%s already exists.\n", workspaceReference); err != nil {
				return err
			}
		}
	} else if ws.Status.Phase != corev1alpha1.LogicalClusterPhaseReady && o.ReadyWaitTimeout > 0 {
		if _, err := fmt.Fprintf(o.Out, "%s created. Waiting for it to be ready...\n", workspaceReference); err != nil {
			return err
		}
	} else if ws.Status.Phase != corev1alpha1.LogicalClusterPhaseReady {
		return fmt.Errorf("%s created but is not ready to use", workspaceReference)
	}

	// STOP THE BLEEDING: the virtual workspace is still informer based (not good). We have to wait until it shows up.
	if err := wait.PollUntilContextTimeout(ctx, time.Millisecond*100, time.Second*5, true, func(ctx context.Context) (bool, error) {
		if _, err := o.kcpClusterClient.Cluster(currentClusterName).TenancyV1alpha1().Workspaces().Get(ctx, ws.Name, metav1.GetOptions{}); err != nil {
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
	if ws.Status.Phase != corev1alpha1.LogicalClusterPhaseReady {
		if err := wait.PollUntilContextTimeout(ctx, time.Millisecond*500, o.ReadyWaitTimeout, true, func(ctx context.Context) (bool, error) {
			ws, err = o.kcpClusterClient.Cluster(currentClusterName).TenancyV1alpha1().Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			if ws.Status.Phase == corev1alpha1.LogicalClusterPhaseReady {
				return true, nil
			}
			return false, nil
		}); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(o.Out, "%s is ready to use.\n", workspaceReference); err != nil {
		return err
	}

	if o.EnterAfterCreate {
		useOptions := NewUseWorkspaceOptions(o.IOStreams)
		useOptions.Name = ws.Name
		// only for unit test needs
		if o.modifyConfig != nil {
			useOptions.modifyConfig = o.modifyConfig
		}
		if err := useOptions.Complete(nil); err != nil {
			return err
		}
		if err := useOptions.Validate(); err != nil {
			return err
		}
		return useOptions.Run(ctx)
	}

	return nil
}

// CreateContextOptions contains options for creating or updating a kubeconfig context.
type CreateContextOptions struct {
	*base.Options

	// Name is the name of the context to create.
	Name string
	// Overwrite indicates the context should be updated if it already exists. This is required to perform the update.
	Overwrite bool

	startingConfig *clientcmdapi.Config

	// for testing
	modifyConfig func(configAccess clientcmd.ConfigAccess, newConfig *clientcmdapi.Config) error
}

// NewCreateContextOptions returns a new CreateContextOptions.
func NewCreateContextOptions(streams genericclioptions.IOStreams) *CreateContextOptions {
	return &CreateContextOptions{
		Options: base.NewOptions(streams),

		modifyConfig: func(configAccess clientcmd.ConfigAccess, newConfig *clientcmdapi.Config) error {
			return clientcmd.ModifyConfig(configAccess, *newConfig, true)
		},
	}
}

// BindFlags binds fields to cmd's flagset.
func (o *CreateContextOptions) BindFlags(cmd *cobra.Command) {
	o.Options.BindFlags(cmd)
	cmd.Flags().BoolVar(&o.Overwrite, "overwrite", o.Overwrite, "Overwrite the context if it already exists")
}

// Complete ensures all dynamically populated fields are initialized.
func (o *CreateContextOptions) Complete(args []string) error {
	if err := o.Options.Complete(); err != nil {
		return err
	}

	var err error
	o.startingConfig, err = o.ClientConfig.ConfigAccess().GetStartingConfig()
	if err != nil {
		return err
	}

	if o.Name == "" && len(args) > 0 {
		o.Name = args[0]
	}

	return nil
}

// Validate validates the CreateContextOptions are complete and usable.
func (o *CreateContextOptions) Validate() error {
	return o.Options.Validate()
}

// Run creates or updates a kubeconfig context from the current context.
func (o *CreateContextOptions) Run(ctx context.Context) error {
	config, err := o.ClientConfig.RawConfig()
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
		return fmt.Errorf("current URL %q does not point to a workspace", currentCluster.Server)
	}

	if o.Name == "" {
		o.Name = currentClusterName.String()
	}

	_, existedBefore := o.startingConfig.Contexts[o.Name]
	if existedBefore && !o.Overwrite {
		return fmt.Errorf("context %q already exists in kubeconfig, use --overwrite to update it", o.Name)
	}

	newKubeConfig := o.startingConfig.DeepCopy()
	newCluster := *currentCluster
	newKubeConfig.Clusters[o.Name] = &newCluster
	newContext := *currentContext
	newContext.Cluster = o.Name
	newKubeConfig.Contexts[o.Name] = &newContext
	newKubeConfig.CurrentContext = o.Name

	if err := o.modifyConfig(o.ClientConfig.ConfigAccess(), newKubeConfig); err != nil {
		return err
	}

	if existedBefore {
		if o.startingConfig.CurrentContext == o.Name {
			_, err = fmt.Fprintf(o.Out, "Updated context %q.\n", o.Name)
		} else {
			_, err = fmt.Fprintf(o.Out, "Updated context %q and switched to it.\n", o.Name)
		}
	} else {
		_, err = fmt.Fprintf(o.Out, "Created context %q and switched to it.\n", o.Name)
	}

	return err
}

func newKCPClusterClient(clientConfig clientcmd.ClientConfig) (kcpclientset.ClusterInterface, error) {
	config, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, err
	}
	clusterConfig := rest.CopyConfig(config)
	u, err := url.Parse(config.Host)
	if err != nil {
		return nil, err
	}
	u.Path = ""
	clusterConfig.Host = u.String()
	clusterConfig.UserAgent = rest.DefaultKubernetesUserAgent()
	return kcpclientset.NewForConfig(clusterConfig)
}

// TreeOptions contains options for displaying the workspace tree.
type TreeOptions struct {
	*base.Options

	Full bool

	kcpClusterClient kcpclientset.ClusterInterface
}

// NewShowWorkspaceTreeOptions returns a new ShowWorkspaceTreeOptions.
func NewTreeOptions(streams genericclioptions.IOStreams) *TreeOptions {
	return &TreeOptions{
		Options: base.NewOptions(streams),
	}
}

// BindFlags binds fields to cmd's flagset.
func (o *TreeOptions) BindFlags(cmd *cobra.Command) {
	o.Options.BindFlags(cmd)
	cmd.Flags().BoolVarP(&o.Full, "full", "f", o.Full, "Show full workspace names")
}

// Complete ensures all dynamically populated fields are initialized.
func (o *TreeOptions) Complete() error {
	if err := o.Options.Complete(); err != nil {
		return err
	}

	kcpClusterClient, err := newKCPClusterClient(o.ClientConfig)
	if err != nil {
		return err
	}
	o.kcpClusterClient = kcpClusterClient

	return nil
}

// Run outputs the current workspace.
func (o *TreeOptions) Run(ctx context.Context) error {
	config, err := o.ClientConfig.ClientConfig()
	if err != nil {
		return err
	}
	_, current, err := pluginhelpers.ParseClusterURL(config.Host)
	if err != nil {
		return fmt.Errorf("current config context URL %q does not point to workspace", config.Host)
	}

	tree := treeprint.New()
	// NOTE(hasheddan): the cluster URL can be used for only the tree root as
	// the friendly name is used in kubeconfig.
	name := current.String()
	if !o.Full {
		name = name[strings.LastIndex(name, ":")+1:]
	}
	branch := tree.AddBranch(name)
	if err := o.populateBranch(ctx, branch, current, name); err != nil {
		return err
	}

	fmt.Println(tree.String())
	return nil
}

func (o *TreeOptions) populateBranch(ctx context.Context, tree treeprint.Tree, parent logicalcluster.Path, parentName string) error {
	results, err := o.kcpClusterClient.Cluster(parent).TenancyV1alpha1().Workspaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	for _, workspace := range results.Items {
		_, current, err := pluginhelpers.ParseClusterURL(workspace.Spec.URL)
		if err != nil {
			return fmt.Errorf("current config context URL %q does not point to workspace", workspace.Spec.URL)
		}
		// NOTE(hasheddan): the cluster URL from the Workspace does not use the
		// friendly name, so we use the Workspace name instead.
		name := workspace.Name
		if o.Full {
			name = parentName + ":" + name
		}
		branch := tree.AddBranch(name)
		if err := o.populateBranch(ctx, branch, current, name); err != nil {
			return err
		}
	}
	return nil
}
