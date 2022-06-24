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
)

const (
	PluginName = "apis.kcp.dev/ReservedMetadata"
)

var (
	annotationAllowList = []string{}

	labelAllowList = []string{
		"experimental.workload.kcp.dev/scheduling-disabled",
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
	// nolint: nilerr
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

	if err := assertViolation(newMeta.GetAnnotations(), oldMeta.GetAnnotations(), annotationAllowList); err != nil {
		return admission.NewForbidden(a, fmt.Errorf("modification of reserved annotation: %w", err))
	}

	if err := assertViolation(newMeta.GetLabels(), oldMeta.GetLabels(), labelAllowList); err != nil {
		return admission.NewForbidden(a, fmt.Errorf("modification of reserved label: %w", err))
	}

	return nil
}

// diff computes the difference between values of left and right.
// It returns all keys from left and right, where:
//
// - left has a key not present in right
// - right has a key not present in left
// - the values differ between left and right for the same key
func diff(left, right map[string]string) []string {
	var (
		result      = make([]string, 0, len(left)+len(right))
		onlyOnRight = make(map[string]string, len(right))
	)

	for k, v := range right {
		onlyOnRight[k] = v
	}

	for kLeft, vLeft := range left {
		vRight, inRight := right[kLeft]

		if inRight {
			if vLeft != vRight {
				result = append(result, kLeft)
			}
			delete(onlyOnRight, kLeft)
		} else {
			result = append(result, kLeft)
		}
	}

	for k := range onlyOnRight {
		result = append(result, k)
	}

	return result
}

func assertViolation(new, old map[string]string, allowList []string) error {
	for _, key := range diff(new, old) {
		for i := range allowList {
			if strings.HasSuffix(allowList[i], "*") && strings.HasPrefix(key, allowList[i][:len(allowList[i])-1]) {
				return nil
			} else if allowList[i] == key {
				return nil
			}
		}

		if strings.HasSuffix(key, "kcp.dev") {
			return fmt.Errorf("%q", key)
		}
	}

	return nil
}
