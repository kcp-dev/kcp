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

package reservedmetadata

import (
	"context"
	"fmt"
	"io"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/utils/strings/slices"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/syncer"
)

const (
	PluginName = "apis.kcp.dev/ReservedMetadata"
)

var (
	annotationAllowList = []string{
		workloadv1alpha1.AnnotationSkipDefaultObjectCreation,
		syncer.AdvancedSchedulingFeatureAnnotation,
		tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey, // protected by workspace admission from non-system:admins
		authorization.RequiredGroupsAnnotationKey,               // protected by workspace admission from non-system:admins
		tenancy.LogicalClusterPathAnnotationKey,                 // protected by pathannoation admission from non-system:admins
	}
	labelAllowList = []string{
		apisv1alpha1.APIExportPermissionClaimLabelPrefix + "*", // protected by the permissionclaim admission plugin
	}
)

// Register registers the reserved metadata plugin for creation and updates.
// Deletion and connect operations are not relevant as not object changes are expected here.
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &reservedMetadata{
				Handler:             admission.NewHandler(admission.Create, admission.Update),
				annotationAllowList: annotationAllowList,
				labelAllowList:      labelAllowList,
			}, nil
		})
}

// reservedMetadata is a validating admission plugin protecting against mutating reserved kcp metadata.
type reservedMetadata struct {
	*admission.Handler

	annotationAllowList []string
	labelAllowList      []string
}

var _ = admission.ValidationInterface(&reservedMetadata{})

// Validate asserts the underlying object for changes in labels and annotations.
// If the user is member of the "system:masters" group, all mutations are allowed.
func (o *reservedMetadata) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	newMeta, err := meta.Accessor(a.GetObject())
	//nolint:nilerr
	if err != nil {
		// The object we are dealing with doesn't have object metadata defined
		// hence it doesn't have annotations to be checked.
		return nil
	}

	oldMeta, err := meta.Accessor(a.GetOldObject())
	if err != nil {
		oldMeta = &metav1.ObjectMeta{}
	}

	if slices.Contains(a.GetUserInfo().GetGroups(), user.SystemPrivilegedGroup) {
		return nil
	}

	if k, ok := hasPrivilegedModification(newMeta.GetAnnotations(), oldMeta.GetAnnotations(), annotationAllowList); ok {
		return admission.NewForbidden(a, fmt.Errorf("modification of reserved annotation: %q", k))
	}

	if k, ok := hasPrivilegedModification(newMeta.GetLabels(), oldMeta.GetLabels(), labelAllowList); ok {
		return admission.NewForbidden(a, fmt.Errorf("modification of reserved label: %q", k))
	}

	return nil
}

func hasPrivilegedModification(new, old map[string]string, allowList []string) (key string, modified bool) {
	hasChanged := func(k, v1, v2 string, v2present bool) bool {
		return (!v2present || v1 != v2) && isPrivileged(k, allowList)
	}

	for k, v1 := range old {
		v2, ok := new[k]

		if hasChanged(k, v1, v2, ok) {
			return k, true
		}
	}

	for k, v1 := range new {
		v2, ok := old[k]

		if hasChanged(k, v1, v2, ok) {
			return k, true
		}
	}

	return "", false
}

func isPrivileged(key string, allowList []string) bool {
	for i := range allowList {
		if strings.HasSuffix(allowList[i], "*") && strings.HasPrefix(key, allowList[i][:len(allowList[i])-1]) {
			return false
		} else if allowList[i] == key {
			return false
		}
	}

	return strings.HasSuffix(key, "kcp.dev")
}
