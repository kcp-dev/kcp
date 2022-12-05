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
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"
)

func ParseClusterURL(host string) (*url.URL, logicalcluster.Name, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, logicalcluster.Name{}, err
	}
	ret := *u
	prefix := "/clusters/"
	if clusterIndex := strings.Index(u.Path, prefix); clusterIndex >= 0 {
		clusterName := logicalcluster.New(strings.SplitN(ret.Path[clusterIndex+len(prefix):], "/", 2)[0])
		if !clusterName.IsValid() {
			return nil, logicalcluster.Name{}, fmt.Errorf("invalid cluster name: %q", clusterName)
		}
		ret.Path = ret.Path[:clusterIndex]
		return &ret, clusterName, nil
	}

	return nil, logicalcluster.Name{}, fmt.Errorf("current cluster URL %s is not pointing to a cluster workspace", u)
}
