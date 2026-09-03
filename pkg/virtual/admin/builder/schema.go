/*
Copyright 2026 The kcp Authors.

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

package builder

import (
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"

	configcrds "github.com/kcp-dev/kcp/config/crds"
)

// ShardsSchema is the APIResourceSchema for core.kcp.io/v1alpha1 Shards,
// derived from the embedded CRD manifest.
var ShardsSchema *apisv1alpha1.APIResourceSchema

func init() {
	crd := apiextensionsv1.CustomResourceDefinition{}
	if err := configcrds.Unmarshal("core.kcp.io_shards.yaml", &crd); err != nil {
		panic(fmt.Sprintf("failed to unmarshal shards CRD: %v", err))
	}
	schema, err := apisv1alpha1.CRDToAPIResourceSchema(&crd, "crd")
	if err != nil {
		panic(fmt.Sprintf("failed to convert CRD %s.%s to APIResourceSchema: %v", crd.Spec.Names.Plural, crd.Spec.Group, err))
	}
	ShardsSchema = schema
}
