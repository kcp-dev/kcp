/*
Copyright 2022 The kcp Authors.

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

	"github.com/spf13/cobra"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/cli/pkg/base"
	pluginhelpers "github.com/kcp-dev/cli/pkg/helpers"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
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
	newKCPClusterClient func(config clientcmd.ClientConfig) (kcpclientset.ClusterInterface, error)
	modifyConfig        func(configAccess clientcmd.ConfigAccess, newConfig *clientcmdapi.Config) error
	getAPIBindings      func(ctx context.Context, kcpClusterClient kcpclientset.ClusterInterface, host string) ([]apisv1alpha2.APIBinding, error)
	userHomeDir         func() (string, error)
}

// NewUseWorkspaceOptions returns a new UseWorkspaceOptions.
func NewUseWorkspaceOptions(streams genericclioptions.IOStreams) *UseWorkspaceOptions {
	return &UseWorkspaceOptions{
		Options: base.NewOptions(streams),

		newKCPClusterClient: newKCPClusterClient,
		modifyConfig: func(configAccess clientcmd.ConfigAccess, newConfig *clientcmdapi.Config) error {
			return clientcmd.ModifyConfig(configAccess, *newConfig, true)
		},
		getAPIBindings: getAPIBindings,
		userHomeDir:    os.UserHomeDir,
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

	if o.startingConfig == nil {
		var err error
		o.startingConfig, err = o.ClientConfig.ConfigAccess().GetStartingConfig()
		if err != nil {
			return err
		}
	}

	kcpClusterClient, err := o.newKCPClusterClient(o.ClientConfig)
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
func (o *UseWorkspaceOptions) Run(ctx context.Context) (err error) {
	name := o.Name

	// preprocess as a string
	home, _ := o.userHomeDir()
	if home != "" && strings.HasPrefix(name, home) {
		name = "~" + strings.TrimPrefix(name, home)
	}
	name = strings.ReplaceAll(name, "/", ":")
	if name == core.RootCluster.String() || strings.HasPrefix(name, core.RootCluster.String()+":") && !o.ShortWorkspaceOutput {
		// LEGACY(mjudeikis): Remove once everybody gets used to this
		name = ":" + name
		fmt.Fprintf(o.ErrOut, "Note: Using 'root:' to define an absolute path is no longer supported. Instead, use ':root' to specify an absolute path.\n")
	}
	if name == "" {
		if !o.ShortWorkspaceOutput {
			defer func() {
				if err == nil {
					_, err = fmt.Fprintf(o.ErrOut, "Note: 'kubectl ws' now matches 'cd' semantics: go to home workspace. 'kubectl ws -' to go back. 'kubectl ws .' to print current workspace.\n")
				}
			}()
		}
		name = "~"
	}

	rawConfig, err := o.ClientConfig.RawConfig()
	if err != nil {
		return err
	}

	// Store the currentContext content for later to set as previous context
	currentContext, found := o.startingConfig.Contexts[rawConfig.CurrentContext]
	if !found {
		return fmt.Errorf("current %q context not found", rawConfig.CurrentContext)
	}

	switch {
	case name == "-":
		newServerHost, err := o.swapContexts(ctx, currentContext)
		if err != nil {
			return err
		}
		return printCurrentWorkspace(o.Out, newServerHost, shortWorkspaceOutput(o.ShortWorkspaceOutput), nil)
	case name == ":":
		u, err := o.currentRootURL()
		if err != nil {
			return err
		}
		return o.commitConfig(ctx, currentContext, u, nil)
	case name == ".":
		cfg, err := o.ClientConfig.ClientConfig()
		if err != nil {
			return err
		}
		return printCurrentWorkspace(o.Out, cfg.Host, shortWorkspaceOutput(o.ShortWorkspaceOutput), nil)
	case name == "~" || strings.HasPrefix(name, "~:"):
		pth, err := o.homePath(ctx)
		if err != nil {
			return err
		}
		name = ":" + pth.String() + strings.TrimPrefix(name, "~")
	}

	u, current, err := o.parseCurrentLogicalCluster()
	if err != nil {
		return err
	}

	// make relative paths absolute
	if name[0] == ':' {
		name = strings.TrimPrefix(name, ":")
	} else {
		name = current.Join(name).String()
	}

	// remove . and ..
	pth, err := resolveDots(name)
	if err != nil {
		return err
	}

	// here we should have a valid absolute path without dots, without : prefix
	if !pth.IsValid() {
		return fmt.Errorf("invalid workspace path: %s", o.Name)
	}

	// first check if the workspace exists via discovery
	groups, err := o.kcpClusterClient.Cluster(pth).Discovery().ServerGroups()
	if err != nil && !apierrors.IsForbidden(err) {
		return err
	}
	denied := apierrors.IsForbidden(err) || len(groups.Groups) == 0

	// first try to get Workspace from parent to potentially get a 404. A 403 in the parent though is
	// not a blocker to enter the workspace. We do discovery as a final check.
	var workspaceType *tenancyv1alpha1.WorkspaceTypeReference
	notFound := false
	if pth == core.RootCluster.Path() {
		workspaceType = &tenancyv1alpha1.WorkspaceTypeReference{
			Name: "root",
		}
	} else {
		parentClusterName, workspaceName := logicalcluster.NewPath(name).Split()
		if workspaceName != "" {
			if ws, err := o.kcpClusterClient.Cluster(parentClusterName).TenancyV1alpha1().Workspaces().Get(ctx, workspaceName, metav1.GetOptions{}); apierrors.IsNotFound(err) {
				notFound = true
			} else if err == nil {
				workspaceType = ws.Spec.Type
			}
		}
	}
	switch {
	case denied && notFound:
		return fmt.Errorf("workspace %q not found", name)
	case denied:
		return fmt.Errorf("access to workspace %q denied", name)
	case notFound:
		// we are good. Somehow we have access, maybe without having access to the parent or there is no parent.
	}

	u.Path = path.Join(u.Path, pth.RequestPath())
	return o.commitConfig(ctx, currentContext, u, workspaceType)
}

func resolveDots(pth string) (logicalcluster.Path, error) {
	var ret logicalcluster.Path
	for _, part := range strings.Split(pth, ":") {
		switch part {
		case ".":
			continue
		case "..":
			if ret.Empty() {
				return logicalcluster.Path{}, errors.New("cannot go up from root")
			}
			ret, _ = ret.Parent()
		default:
			ret = ret.Join(part)
		}
	}
	return ret, nil
}

// swapContexts moves to previous context from the config.
// It will update existing configuration by swapping current & previous configurations.
// This method already commits. Do not use with commitConfig.
func (o *UseWorkspaceOptions) swapContexts(ctx context.Context, currentContext *clientcmdapi.Context) (string, error) {
	currentContext = currentContext.DeepCopy()

	prev, exists := o.startingConfig.Contexts[kcpPreviousWorkspaceContextKey]
	if !exists {
		return "", errors.New("no previous workspace found in kubeconfig")
	}

	newKubeConfig := o.startingConfig.DeepCopy()
	if currentContext.Cluster == kcpCurrentWorkspaceContextKey {
		oldCluster, found := o.startingConfig.Clusters[currentContext.Cluster]
		if !found {
			return "", fmt.Errorf("cluster %q not found in kubeconfig", currentContext.Cluster)
		}
		currentContext = currentContext.DeepCopy()
		currentContext.Cluster = kcpPreviousWorkspaceContextKey
		newKubeConfig.Clusters[kcpPreviousWorkspaceContextKey] = oldCluster
	}
	if prev.Cluster == kcpPreviousWorkspaceContextKey {
		prevCluster, found := o.startingConfig.Clusters[prev.Cluster]
		if !found {
			return "", fmt.Errorf("cluster %q not found in kubeconfig", currentContext.Cluster)
		}
		prev = prev.DeepCopy()
		prev.Cluster = kcpCurrentWorkspaceContextKey
		newKubeConfig.Clusters[kcpCurrentWorkspaceContextKey] = prevCluster
	}
	newKubeConfig.Contexts[kcpCurrentWorkspaceContextKey] = prev
	newKubeConfig.Contexts[kcpPreviousWorkspaceContextKey] = currentContext

	newKubeConfig.CurrentContext = kcpCurrentWorkspaceContextKey

	if err := o.modifyConfig(o.ClientConfig.ConfigAccess(), newKubeConfig); err != nil {
		return "", err
	}

	newServerHost := newKubeConfig.Clusters[newKubeConfig.Contexts[kcpCurrentWorkspaceContextKey].Cluster].Server

	bindings, err := o.getAPIBindings(ctx, o.kcpClusterClient, newServerHost)
	if err != nil {
		// display the error, but don't stop the current workspace from being reported.
		fmt.Fprintf(o.ErrOut, "error checking APIBindings: %v\n", err)
	}
	if err = findUnresolvedPermissionClaims(o.ErrOut, bindings); err != nil {
		// display the error, but don't stop the current workspace from being reported.
		fmt.Fprintf(o.ErrOut, "error checking APIBindings: %v\n", err)
	}

	return newServerHost, nil
}

// commitConfig will take in current config, new host and optional workspaceType and update the kubeconfig.
func (o *UseWorkspaceOptions) commitConfig(ctx context.Context, currentContext *clientcmdapi.Context, u *url.URL, workspaceType *tenancyv1alpha1.WorkspaceTypeReference) error {
	// modify kubeconfig, using the "workspace" context and cluster
	newKubeConfig := o.startingConfig.DeepCopy()
	oldCluster, found := o.startingConfig.Clusters[currentContext.Cluster]
	if !found {
		return fmt.Errorf("cluster %q not found in kubeconfig", currentContext.Cluster)
	}
	newCluster := *oldCluster
	newCluster.Server = u.String()
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

	bindings, err := o.getAPIBindings(ctx, o.kcpClusterClient, u.String())
	if err != nil {
		// display the error, but don't stop the current workspace from being reported.
		fmt.Fprintf(o.ErrOut, "error checking APIBindings: %v\n", err)
	}
	if err := findUnresolvedPermissionClaims(o.ErrOut, bindings); err != nil {
		// display the error, but don't stop the current workspace from being reported.
		fmt.Fprintf(o.ErrOut, "error checking APIBindings: %v\n", err)
	}

	return printCurrentWorkspace(o.Out, u.String(), shortWorkspaceOutput(o.ShortWorkspaceOutput), workspaceType)
}

func (o *UseWorkspaceOptions) homePath(ctx context.Context) (logicalcluster.Path, error) {
	homeWorkspace, err := o.kcpClusterClient.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Get(ctx, "~", metav1.GetOptions{})
	if err != nil {
		return logicalcluster.Path{}, err
	}

	uh, err := url.Parse(homeWorkspace.Spec.URL)
	if err != nil {
		return logicalcluster.Path{}, fmt.Errorf("invalid home workspace URL %q: %w", homeWorkspace.Spec.URL, err)
	}

	return logicalcluster.NewPath(strings.TrimPrefix(uh.Path, "/clusters/")), nil
}

func (o *UseWorkspaceOptions) currentRootURL() (*url.URL, error) {
	u, currentClusterName, err := o.parseCurrentLogicalCluster()
	if err != nil {
		return nil, err
	}

	var hasParent bool
	var root, current logicalcluster.Path
	root = *currentClusterName
	for {
		current = root
		root, hasParent = root.Parent()
		if !hasParent {
			break
		}
	}

	// root workspace
	u.Path = path.Join(u.Path, current.RequestPath())
	return u, nil
}

func (o *UseWorkspaceOptions) parseCurrentLogicalCluster() (*url.URL, *logicalcluster.Path, error) {
	config, err := o.ClientConfig.ClientConfig()
	if err != nil {
		return nil, nil, err
	}
	u, currentClusterName, err := pluginhelpers.ParseClusterURL(config.Host)
	if err != nil {
		return nil, nil, fmt.Errorf("current URL %q does not point to a workspace", config.Host)
	}
	return u, &currentClusterName, nil
}

// getAPIBindings retrieves APIBindings within the workspace.
func getAPIBindings(ctx context.Context, kcpClusterClient kcpclientset.ClusterInterface, host string) ([]apisv1alpha2.APIBinding, error) {
	_, clusterName, err := pluginhelpers.ParseClusterURL(host)
	if err != nil {
		return nil, err
	}

	apiBindings, err := kcpClusterClient.Cluster(clusterName).ApisV1alpha2().APIBindings().List(ctx, metav1.ListOptions{})
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
func findUnresolvedPermissionClaims(out io.Writer, apiBindings []apisv1alpha2.APIBinding) error {
	for _, binding := range apiBindings {
		for _, exportedClaim := range binding.Status.ExportPermissionClaims {
			var found, ack, verbsMatch bool
			var verbsExpected, verbsActual sets.Set[string]
			for _, specClaim := range binding.Spec.PermissionClaims {
				if !exportedClaim.EqualGRI(specClaim.PermissionClaim) {
					continue
				}
				found = true
				ack = (specClaim.State == apisv1alpha2.ClaimAccepted) || specClaim.State == apisv1alpha2.ClaimRejected
				verbsExpected = sets.New(exportedClaim.Verbs...)
				verbsActual = sets.New(specClaim.Verbs...)
				verbsMatch = verbsActual.Difference(verbsExpected).Len() == 0 && verbsExpected.Difference(verbsActual).Len() == 0
			}
			if !found {
				fmt.Fprintf(out, "Warning: claim for %s exported but not specified on APIBinding %s\nAdd this claim to the APIBinding's Spec.\n", exportedClaim.String(), binding.Name)
			}
			if !ack {
				fmt.Fprintf(out, "Warning: claim for %s specified on APIBinding %s but not accepted or rejected.\n", exportedClaim.String(), binding.Name)
			}
			if !verbsMatch {
				fmt.Fprintf(out, "Warning: allowed verbs (%s) on claim for %s on APIBinding %s do not match expected verbs (%s).\n", strings.Join(verbsActual.UnsortedList(), ","), exportedClaim.String(), binding.Name, strings.Join(verbsExpected.UnsortedList(), ","))
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

	return printCurrentWorkspace(o.Out, cfg.Host, shortWorkspaceOutput(o.ShortWorkspaceOutput), nil)
}

type shortWorkspaceOutput bool

func printCurrentWorkspace(out io.Writer, host string, shortWorkspaceOutput shortWorkspaceOutput, workspaceType *tenancyv1alpha1.WorkspaceTypeReference) error {
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

	message := fmt.Sprintf("Current workspace is '%s'", clusterName.String())
	if workspaceType != nil {
		message += fmt.Sprintf(" (type %s)", logicalcluster.NewPath(workspaceType.Path).Join(string(workspaceType.Name)).String())
	}
	_, err = fmt.Fprintln(out, message+".")
	return err
}
