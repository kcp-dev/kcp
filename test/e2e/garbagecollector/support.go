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

package garbagecollector

import (
	"embed"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kcpapiextensionsv1client "github.com/kcp-dev/client-go/apiextensions/client/typed/apiextensions/v1"
	"github.com/kcp-dev/logicalcluster/v3"

	configcrds "github.com/kcp-dev/kcp/config/crds"
)

//go:embed *.yaml
var testFiles embed.FS

func newClusterScopedCRD(group, name string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s.%s", pluralize(name), group),
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Singular: name,
				Plural:   pluralize(name),
				Kind:     strings.ToTitle(name),
				ListKind: strings.ToTitle(name) + "List",
			},
			Scope: apiextensionsv1.ClusterScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1",
					Served:  true,
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type: "object",
						},
					},
				},
			},
		},
	}
}

func pluralize(name string) string {
	switch string(name[len(name)-1]) {
	case "s":
		return name + "es"
	case "y":
		return strings.TrimSuffix(name, "y") + "ies"
	}

	return name + "s"
}

func bootstrapCRD(
	t *testing.T,
	clusterName logicalcluster.Path,
	client kcpapiextensionsv1client.CustomResourceDefinitionClusterInterface,
	crd *apiextensionsv1.CustomResourceDefinition,
) {
	t.Helper()

	err := configcrds.CreateSingle(t.Context(), client.Cluster(clusterName), crd)
	require.NoError(t, err, "error bootstrapping CRD %s in cluster %s", crd.Name, clusterName)
}
