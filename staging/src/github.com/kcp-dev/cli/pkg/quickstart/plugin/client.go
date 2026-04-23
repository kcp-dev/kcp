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

package plugin

import (
	"fmt"
	"net/url"

	"k8s.io/client-go/rest"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
)

func defaultKCPClusterClient(config *rest.Config) (kcpclientset.ClusterInterface, error) {
	c, err := baseConfig(config)
	if err != nil {
		return nil, err
	}

	return kcpclientset.NewForConfig(c)
}

func defaultKCPDynamicClient(config *rest.Config) (kcpdynamic.ClusterInterface, error) {
	c, err := baseConfig(config)
	if err != nil {
		return nil, err
	}

	return kcpdynamic.NewForConfig(c)
}

// baseConfig clears the host path so kcp can route each call
// to the right workspace via .Cluster().
func baseConfig(config *rest.Config) (*rest.Config, error) {
	u, err := url.Parse(config.Host)
	if err != nil {
		return nil, fmt.Errorf("invalid server URL %q: %w", config.Host, err)
	}

	c := rest.CopyConfig(config)
	u.Path = ""
	c.Host = u.String()

	return c, nil
}
