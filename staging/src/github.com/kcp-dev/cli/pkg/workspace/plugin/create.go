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
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/cli/pkg/base"
	pluginhelpers "github.com/kcp-dev/cli/pkg/helpers"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
)

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

	// CreateContextName is the name of the context to create after workspace creation (if set).
	CreateContextName string

	// for testing - passed to UseWorkspaceOptions
	newKCPClusterClient func(config clientcmd.ClientConfig) (kcpclientset.ClusterInterface, error)
	modifyConfig        func(configAccess clientcmd.ConfigAccess, newConfig *clientcmdapi.Config) error
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
	cmd.Flags().BoolVar(&o.IgnoreExisting, "ignore-existing", o.IgnoreExisting, "Ignore if the workspace already exists. Requires none or absolute type path. Overwrites the context if --create-context is set.")
	cmd.Flags().StringVar(&o.LocationSelector, "location-selector", o.LocationSelector, "A label selector to select the scheduling location of the created workspace.")
	cmd.Flags().StringVar(&o.CreateContextName, "create-context", o.CreateContextName, "Create a kubeconfig context for the new workspace with the given name.")
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

	var structuredWorkspaceType *tenancyv1alpha1.WorkspaceTypeReference
	if o.Type != "" {
		separatorIndex := strings.LastIndex(o.Type, ":")
		switch separatorIndex {
		case -1:
			structuredWorkspaceType = &tenancyv1alpha1.WorkspaceTypeReference{
				Name: tenancyv1alpha1.WorkspaceTypeName(strings.ToLower(o.Type)),
				// path is defaulted through admission
			}
		default:
			structuredWorkspaceType = &tenancyv1alpha1.WorkspaceTypeReference{
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
		if structuredWorkspaceType != nil && (ws.Spec.Type.Name != "" && ws.Spec.Type.Name != structuredWorkspaceType.Name || ws.Spec.Type.Path != structuredWorkspaceType.Path) {
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

	// Create context if --create-context is set
	if o.CreateContextName != "" {
		createContextOptions := NewCreateContextOptions(o.IOStreams)
		createContextOptions.Name = o.CreateContextName
		createContextOptions.ClusterURL = ws.Spec.URL
		createContextOptions.ClientConfig = o.ClientConfig
		createContextOptions.Overwrite = o.IgnoreExisting

		// If --enter is set, switch to the new context
		createContextOptions.KeepCurrent = !o.EnterAfterCreate

		startingConfig, err := o.ClientConfig.RawConfig()
		if err != nil {
			return fmt.Errorf("failed to get raw config: %w", err)
		}
		createContextOptions.startingConfig = &startingConfig

		// only for unit test needs
		if o.modifyConfig != nil {
			createContextOptions.modifyConfig = o.modifyConfig
		}

		if err := createContextOptions.Complete(nil); err != nil {
			return err
		}
		if err := createContextOptions.Validate(); err != nil {
			return err
		}

		if err := createContextOptions.Run(ctx); err != nil {
			return fmt.Errorf("failed to create context: %w", err)
		}

		return nil
	}

	// If --enter is set, enter the newly created workspace
	if o.EnterAfterCreate {
		useOptions := NewUseWorkspaceOptions(o.IOStreams)
		useOptions.Name = ws.Name
		useOptions.ClientConfig = o.ClientConfig

		startingConfig, err := o.ClientConfig.RawConfig()
		if err != nil {
			return fmt.Errorf("error getting rawconfig for use: %w", err)
		}
		useOptions.startingConfig = &startingConfig

		// only for unit test needs
		if o.modifyConfig != nil {
			useOptions.modifyConfig = o.modifyConfig
		}
		if o.newKCPClusterClient != nil {
			useOptions.newKCPClusterClient = o.newKCPClusterClient
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
