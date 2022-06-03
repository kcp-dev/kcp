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

package apiexport

import (
	"embed"
	"path"

	"github.com/kcp-dev/logicalcluster"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	virtualoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned"
)

//go:embed *.yaml
var testFiles embed.FS

func groupExists(list *metav1.APIGroupList, group string) bool {
	for _, g := range list.Groups {
		if g.Name == group {
			return true
		}
	}
	return false
}

func resourceExists(list *metav1.APIResourceList, resource string) bool {
	for _, r := range list.APIResources {
		if r.Name == resource {
			return true
		}
	}
	return false
}

type virtualClusterClient struct {
	config *rest.Config
}

func (c *virtualClusterClient) Cluster(cluster logicalcluster.Name, exportName string) (*wildwestclientset.Cluster, error) {
	config := rest.CopyConfig(c.config)
	//	/services/apiexport/root:org:ws/<apiexport-name>/clusters/*/api/v1/configmaps
	config.Host += path.Join(virtualoptions.DefaultRootPathPrefix, "apiexport", cluster.String(), exportName)
	return wildwestclientset.NewClusterForConfig(config)
}

func userConfig(username string, cfg *rest.Config) *rest.Config {
	cfgCopy := rest.CopyConfig(cfg)
	cfgCopy.BearerToken = username + "-token"
	return cfgCopy
}
