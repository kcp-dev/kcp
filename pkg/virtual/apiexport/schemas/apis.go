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

package schemas

import (
	"encoding/json"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/utils/ptr"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

var ApisKcpDevSchemas = map[string]*apisv1alpha1.APIResourceSchema{}

func init() {
	for _, resource := range []string{"apibindings", "apiresourceschemas", "apiexports"} {
		// get APIBindings resource schema
		crd := apiextensionsv1.CustomResourceDefinition{}
		if err := configcrds.Unmarshal(fmt.Sprintf("apis.kcp.io_%s.yaml", resource), &crd); err != nil {
			panic(fmt.Sprintf("failed to unmarshal apibindings resource: %v", err))
		}
		schema, err := apisv1alpha1.CRDToAPIResourceSchema(&crd, "crd")
		if err != nil {
			panic(fmt.Sprintf("failed to convert CRD %s.%s to APIResourceSchema: %v", crd.Spec.Names.Plural, crd.Spec.Group, err))
		}
		bs, err := json.Marshal(&apiextensionsv1.JSONSchemaProps{
			Type:                   "object",
			XPreserveUnknownFields: ptr.To(true),
		})
		if err != nil {
			panic(fmt.Sprintf("failed to marshal JSONSchemaProps: %v", err))
		}
		for i := range schema.Spec.Versions {
			v := &schema.Spec.Versions[i]
			v.Schema.Raw = bs // wipe schemas. We don't want validation here.
		}

		ApisKcpDevSchemas[resource] = schema
	}
}
