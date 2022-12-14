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

package indexers

import (
	"fmt"

	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

const (
	// APIExportByIdentity is the indexer name for retrieving APIExports by identity hash.
	APIExportByIdentity = "APIExportByIdentity"
	// APIExportBySecret is the indexer name for retrieving APIExports by secret.
	APIExportBySecret = "APIExportSecret"
)

// IndexAPIExportByIdentity is an index function that indexes an APIExport by its identity hash.
func IndexAPIExportByIdentity(obj interface{}) ([]string, error) {
	apiExport, ok := obj.(*apisv1alpha1.APIExport)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIExport", obj)
	}

	return []string{apiExport.Status.IdentityHash}, nil
}

// IndexAPIExportBySecret is an index function that indexes an APIExport by its identity secret references. Index values
// are of the form <cluster name>|<secret reference namespace>/<secret reference name> (cache keys).
func IndexAPIExportBySecret(obj interface{}) ([]string, error) {
	apiExport, ok := obj.(*apisv1alpha1.APIExport)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIExport", obj)
	}

	if apiExport.Spec.Identity == nil {
		return []string{}, nil
	}

	ref := apiExport.Spec.Identity.SecretRef
	if ref == nil {
		return []string{}, nil
	}

	if ref.Namespace == "" || ref.Name == "" {
		return []string{}, nil
	}

	return []string{kcpcache.ToClusterAwareKey(logicalcluster.From(apiExport).String(), ref.Namespace, ref.Name)}, nil
}
