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

package namespacelifecycle

import (
	"context"
	"fmt"
	"io"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/initializer"
	"k8s.io/apiserver/pkg/admission/plugin/namespace/lifecycle"
	"k8s.io/apiserver/pkg/clientsethack"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/informerfactoryhack"
	"k8s.io/client-go/informers"
	kubernetesclient "k8s.io/client-go/kubernetes"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
)

const (
	// PluginName indicates the name of admission plug-in
	PluginName = "WorkspaceNamespaceLifecycle"
)

// Register registers a plugin
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName, func(config io.Reader) (admission.Interface, error) {
		return newWorkspaceNamespaceLifecycle()
	})
}

// workspaceNamespaceLifecycle is a wrapper of kubernetes namespace lifecycle admission control
// It uses legacy namespace lifecycle admission control by default, and ignores
// immortal namespaces when the workspaces is deleting. This can ensure we can remove all immortal
// namespaces when workspaces is deleting.
type workspaceNamespaceLifecycle struct {
	*admission.Handler

	// legacyNamespaceLifecycle is the kube legacy namespace lifecycle
	legacyNamespaceLifecycle *lifecycle.Lifecycle

	// namespaceLifecycle is used only when workspace is deleting
	namespaceLifecycle *lifecycle.Lifecycle

	getWorkspace func(clusterName logicalcluster.Path, name string) (*tenancyv1beta1.Workspace, error)
}

func newWorkspaceNamespaceLifecycle() (*workspaceNamespaceLifecycle, error) {
	legacyLifecycle, err := lifecycle.NewLifecycle(sets.NewString(metav1.NamespaceDefault, metav1.NamespaceSystem, metav1.NamespacePublic))
	if err != nil {
		return nil, err
	}

	lifecycle, err := lifecycle.NewLifecycle(sets.NewString())
	if err != nil {
		return nil, err
	}

	return &workspaceNamespaceLifecycle{
		Handler:                  admission.NewHandler(admission.Create, admission.Update, admission.Delete),
		legacyNamespaceLifecycle: legacyLifecycle,
		namespaceLifecycle:       lifecycle,
	}, nil
}

var _ = kcpinitializers.WantsKcpInformers(&workspaceNamespaceLifecycle{})
var _ = initializer.WantsExternalKubeInformerFactory(&workspaceNamespaceLifecycle{})
var _ = initializer.WantsExternalKubeClientSet(&workspaceNamespaceLifecycle{})

// Admit makes an admission decision based on the request attributes
func (l *workspaceNamespaceLifecycle) Admit(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces) error {
	// call legacy namespace lifecycle at first
	admissionErr := l.legacyNamespaceLifecycle.Admit(ctx, a, o)

	// when the workspace is being deleted, we want to allow deletion of all namespaces. Hence, after
	// legacyNamespaceLifecycle has forbidden the deletion, we give it a second chance be delegating to
	// namespaceLifecycle which does not protect any namespace beyond normal life-cycle.
	if !apierrors.IsForbidden(admissionErr) {
		return admissionErr
	}

	if a.GetOperation() != admission.Delete || a.GetKind().GroupKind() != corev1.SchemeGroupVersion.WithKind("Namespace").GroupKind() {
		return admissionErr
	}

	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	org, hasParent := clusterName.Parent()
	if !hasParent {
		return admissionErr
	}

	workspace, err := l.getWorkspace(org, clusterName.Base())
	// The shard hosting the workspace could be down,
	// just return error from legacy namespace lifecycle admission in this case
	if err != nil && !apierrors.IsNotFound(err) {
		return admissionErr
	}

	if workspace.DeletionTimestamp.IsZero() {
		return admissionErr
	}

	return l.namespaceLifecycle.Admit(ctx, a, o)
}

// SetExternalKubeInformerFactory implements the WantsExternalKubeInformerFactory interface.
func (l *workspaceNamespaceLifecycle) SetExternalKubeInformerFactory(f informers.SharedInformerFactory) {
	l.legacyNamespaceLifecycle.SetExternalKubeInformerFactory(informerfactoryhack.Unwrap(f))
	l.namespaceLifecycle.SetExternalKubeInformerFactory(informerfactoryhack.Unwrap(f))
}

// SetExternalKubeClientSet implements the WantsExternalKubeClientSet interface.
func (l *workspaceNamespaceLifecycle) SetExternalKubeClientSet(client kubernetesclient.Interface) {
	l.legacyNamespaceLifecycle.SetExternalKubeClientSet(clientsethack.Unwrap(client))
	l.namespaceLifecycle.SetExternalKubeClientSet(clientsethack.Unwrap(client))
}

func (l *workspaceNamespaceLifecycle) SetKcpInformers(informers kcpinformers.SharedInformerFactory) {
	l.SetReadyFunc(informers.Tenancy().V1beta1().Workspaces().Informer().HasSynced)

	l.getWorkspace = func(clusterName logicalcluster.Path, name string) (*tenancyv1beta1.Workspace, error) {
		return informers.Tenancy().V1beta1().Workspaces().Lister().Cluster(clusterName).Get(name)
	}
}

// ValidateInitialization implements the InitializationValidator interface.
func (l *workspaceNamespaceLifecycle) ValidateInitialization() error {
	if err := l.legacyNamespaceLifecycle.ValidateInitialization(); err != nil {
		return err
	}

	if err := l.namespaceLifecycle.ValidateInitialization(); err != nil {
		return err
	}

	if l.getWorkspace == nil {
		return fmt.Errorf("missing getWorkspace")
	}
	return nil
}
