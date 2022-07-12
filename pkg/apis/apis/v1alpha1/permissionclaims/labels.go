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

package permissionclaims

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

// ToLabelKeyAndValue will create a safe key and value for labeling a resource to grant access
// based on the permissionClaim.
func ToLabelKeyAndValue(permissionClaim apisv1alpha1.PermissionClaim) (string, string, error) {
	bytes, err := json.Marshal(permissionClaim)
	if err != nil {
		return "", "", err
	}
	hash := fmt.Sprintf("%x", sha256.Sum224(bytes))
	labelKeyHashLength := validation.LabelValueMaxLength - len(apisv1alpha1.APIExportPermissionClaimLabelPrefix)
	return apisv1alpha1.APIExportPermissionClaimLabelPrefix + hash[0:labelKeyHashLength], hash, nil
}
