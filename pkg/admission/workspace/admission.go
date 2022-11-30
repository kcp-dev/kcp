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

package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/authorization"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

// Validate ClusterWorkspace creation and updates for
// - immutability of fields like type
// - valid phase transitions fulfilling pre-conditions
// - status.location.current and status.baseURL cannot be unset.

// Mutate ClusterWorkspace creation and updates for
// - initializers are short enough to be put into a label
// - consistency of phase and initializers with labels

const (
	PluginName = "tenancy.kcp.dev/Workspace"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &workspace{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}, nil
		})
}

type workspace struct {
	*admission.Handler

	thisWorkspaceLister tenancyv1alpha1listers.ThisWorkspaceClusterLister
}

// Ensure that the required admission interfaces are implemented.
var _ admission.MutationInterface = &workspace{}
var _ admission.ValidationInterface = &workspace{}
var _ = admission.InitializationValidator(&workspace{})
var _ = kcpinitializers.WantsKcpInformers(&workspace{})

// Admit ensures that
// - the user is recorded in annotations on create
func (o *workspace) Admit(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	if a.GetResource().GroupResource() != tenancyv1beta1.Resource("workspaces") {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	cw := &tenancyv1beta1.Workspace{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, cw); err != nil {
		return fmt.Errorf("failed to convert unstructured to ClusterWorkspace: %w", err)
	}

	if a.GetOperation() == admission.Create {
		isSystemMaster := sets.NewString(a.GetUserInfo().GetGroups()...).Has(kuser.SystemPrivilegedGroup)

		// create owner anntoation
		if !isSystemMaster {
			userInfo, err := WorkspaceOwnerAnnotationValue(a.GetUserInfo())
			if err != nil {
				return admission.NewForbidden(a, err)
			}
			if cw.Annotations == nil {
				cw.Annotations = map[string]string{}
			}
			cw.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey] = userInfo
		}

		// copy required groups from ThisWorkspace to new child-Worksapce
		if _, found := cw.Annotations[authorization.RequiredGroupsAnnotationKey]; !found || !isSystemMaster {
			this, err := o.thisWorkspaceLister.Cluster(clusterName).Get(tenancyv1alpha1.ThisWorkspaceName)
			if err != nil {
				return admission.NewForbidden(a, err)
			}
			if thisValue, found := this.Annotations[authorization.RequiredGroupsAnnotationKey]; found {
				if cw.Annotations == nil {
					cw.Annotations = map[string]string{}
				}
				cw.Annotations[authorization.RequiredGroupsAnnotationKey] = thisValue
			} else {
				delete(cw.Annotations, authorization.RequiredGroupsAnnotationKey)
			}
		}
	}

	return updateUnstructured(u, cw)
}

// Validate ensures that
// - the workspace only does a valid phase transition
// - has a valid type
// - has valid initializers when transitioning to initializing
// - the user is recorded in annotations on create
func (o *workspace) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	if a.GetResource().GroupResource() != tenancyv1beta1.Resource("workspaces") {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	cw := &tenancyv1beta1.Workspace{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, cw); err != nil {
		return fmt.Errorf("failed to convert unstructured to ClusterWorkspace: %w", err)
	}

	switch a.GetOperation() {
	case admission.Update:
		u, ok = a.GetOldObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetOldObject())
		}
		old := &tenancyv1beta1.Workspace{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, old); err != nil {
			return fmt.Errorf("failed to convert unstructured to ClusterWorkspace: %w", err)
		}

		if errs := validation.ValidateImmutableField(cw.Spec.Type, old.Spec.Type, field.NewPath("spec", "type")); len(errs) > 0 {
			return admission.NewForbidden(a, errs.ToAggregate())
		}
		if old.Spec.Type.Path != cw.Spec.Type.Path || old.Spec.Type.Name != cw.Spec.Type.Name {
			return admission.NewForbidden(a, errors.New("spec.type is immutable"))
		}

		if old.Status.Cluster != "" && cw.Status.Cluster == "" {
			return admission.NewForbidden(a, errors.New("status.cluster cannot be unset"))
		}

		if cw.Status.Phase != tenancyv1alpha1.WorkspacePhaseScheduling {
			if cw.Status.Cluster == "" {
				return admission.NewForbidden(a, fmt.Errorf("status.cluster must be set for phase %s", cw.Status.Phase))
			}
			if cw.Status.URL == "" {
				return admission.NewForbidden(a, fmt.Errorf("status.URL must be set for phase %s", cw.Status.Phase))
			}
		}
	case admission.Create:
		isSystemMaster := sets.NewString(a.GetUserInfo().GetGroups()...).Has(kuser.SystemPrivilegedGroup)

		if !isSystemMaster {
			userInfo, err := WorkspaceOwnerAnnotationValue(a.GetUserInfo())
			if err != nil {
				return admission.NewForbidden(a, err)
			}
			if cw.Annotations == nil {
				cw.Annotations = map[string]string{}
			}
			if got := cw.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey]; got != userInfo {
				return admission.NewForbidden(a, fmt.Errorf("expected user annotation %s=%s", tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey, userInfo))
			}
		}

		// check that required groups match with ThisWorkspace
		if !isSystemMaster {
			this, err := o.thisWorkspaceLister.Cluster(clusterName).Get(tenancyv1alpha1.ThisWorkspaceName)
			if err != nil {
				return admission.NewForbidden(a, err)
			}
			expected := this.Annotations[authorization.RequiredGroupsAnnotationKey]
			if cw.Annotations[authorization.RequiredGroupsAnnotationKey] != expected {
				return admission.NewForbidden(a, fmt.Errorf("missing required groups annotation %s=%s", authorization.RequiredGroupsAnnotationKey, expected))
			}
		}
	}

	return nil
}

func (o *workspace) ValidateInitialization() error {
	if o.thisWorkspaceLister == nil {
		return fmt.Errorf(PluginName + " plugin needs an ThisWorkspace lister")
	}
	return nil
}

func (o *workspace) SetKcpInformers(informers kcpinformers.SharedInformerFactory) {
	thisWorkspacesReady := informers.Tenancy().V1alpha1().ThisWorkspaces().Informer().HasSynced
	o.SetReadyFunc(func() bool {
		return thisWorkspacesReady()
	})
	o.thisWorkspaceLister = informers.Tenancy().V1alpha1().ThisWorkspaces().Lister()
}

// updateUnstructured updates the given unstructured object to match the given cluster workspace.
func updateUnstructured(u *unstructured.Unstructured, cw *tenancyv1beta1.Workspace) error {
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cw)
	if err != nil {
		return err
	}
	u.Object = raw
	return nil
}

// WorkspaceOwnerAnnotationValue returns the value of the ExperimentalClusterWorkspaceOwnerAnnotationKey annotation.
func WorkspaceOwnerAnnotationValue(user kuser.Info) (string, error) {
	info := &authenticationv1.UserInfo{
		Username: user.GetName(),
		UID:      user.GetUID(),
		Groups:   user.GetGroups(),
	}
	extra := map[string]authenticationv1.ExtraValue{}
	for k, v := range user.GetExtra() {
		extra[k] = v
	}
	info.Extra = extra
	rawInfo, err := json.Marshal(info)
	if err != nil {
		return "", fmt.Errorf("failed to marshal user info: %w", err)
	}

	return string(rawInfo), nil
}
