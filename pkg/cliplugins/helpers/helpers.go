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

package helpers

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

	virtualcommandoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func ParseClusterURL(host string) (*url.URL, logicalcluster.LogicalCluster, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, logicalcluster.LogicalCluster{}, err
	}
	ret := *u
	var clusterName logicalcluster.LogicalCluster
	for _, prefix := range []string{
		"/clusters/",
		path.Join(virtualcommandoptions.DefaultRootPathPrefix, "workspaces") + "/",
	} {
		if clusterIndex := strings.Index(u.Path, prefix); clusterIndex >= 0 {
			clusterName = logicalcluster.New(strings.SplitN(ret.Path[clusterIndex+len(prefix):], "/", 2)[0])
			ret.Path = ret.Path[:clusterIndex]
			break
		}
	}
	if clusterName.Empty() || !IsValid(clusterName) {
		return nil, logicalcluster.LogicalCluster{}, fmt.Errorf("current cluster URL %s is not pointing to a cluster workspace", u)
	}

	return &ret, clusterName, nil
}

var lclusterRegExp = regexp.MustCompile(`^[a-z][a-z0-9-]*[a-z0-9](:[a-z][a-z0-9-]*[a-z0-9])*$`)

func IsValid(cluster logicalcluster.LogicalCluster) bool {
	if !lclusterRegExp.MatchString(cluster.String()) {
		return false
	}

	return cluster.HasPrefix(v1alpha1.RootCluster) || cluster.HasPrefix(logicalcluster.New("system"))
}
