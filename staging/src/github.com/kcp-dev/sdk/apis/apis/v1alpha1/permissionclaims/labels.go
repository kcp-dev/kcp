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

package permissionclaims

import (
	"encoding/json"

	kcpcrypto "github.com/kcp-dev/apimachinery/v2/pkg/util/crypto"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
)

// ToLabelKeyAndValue creates a safe key and value for labeling a resource to grant access
// based on the permissionClaim.
func ToLabelKeyAndValue(exportClusterName logicalcluster.Name, exportName string, permissionClaim apisv1alpha1.PermissionClaim) (string, string, error) {
	bytes, err := json.Marshal(permissionClaim)
	if err != nil {
		return "", "", err
	}
	claimHash := kcpcrypto.Base62Sha224.Bytes(bytes)
	exportHash := kcpcrypto.Base62Sha224.String(exportClusterName.Path().Join(exportName).String())

	return apisv1alpha1.APIExportPermissionClaimLabelPrefix + exportHash, claimHash, nil
}

// ToReflexiveAPIBindingLabelKeyAndValue returns label key and value that is set (as fallback for filtering)
// on APIBindings that point to the given APIExport and the binding has not accepted a claim to it.
func ToReflexiveAPIBindingLabelKeyAndValue(exportClusterName logicalcluster.Name, exportName string) (string, string) {
	claimHash := kcpcrypto.Base62.Bytes([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 0, 1, 2, 3, 4, 5, 6, 7})
	exportHash := kcpcrypto.Base62Sha224.String(exportClusterName.Path().Join(exportName).String())
	return apisv1alpha1.APIExportPermissionClaimLabelPrefix + exportHash, claimHash
}

// ToAPIBindingExportLabelValue returns the label value for the internal.apis.kcp.io/export label
// on APIBindings to filter them by export.
func ToAPIBindingExportLabelValue(clusterName logicalcluster.Name, exportName string) string {
	return kcpcrypto.Base62Sha224.String(clusterName.Path().Join(exportName).String())
}
