/*
Copyright 2023 The KCP Authors.

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

package manager

import (
	"context"

	"k8s.io/client-go/rest"

	proxyclusterclientset "github.com/kcp-dev/kcp/proxy/client/clientset/versioned/cluster"
	proxycontroller "github.com/kcp-dev/kcp/proxy/reconciler/proxy"
	kcpclusterclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
)

func (m *Manager) installWorkspaceProxyController(ctx context.Context) error {
	config := rest.CopyConfig(m.clientConfig)
	config = rest.AddUserAgent(config, proxycontroller.ControllerName)
	kcpClusterClient, err := kcpclusterclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	proxyClusterClient, err := proxyclusterclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c := proxycontroller.NewController(
		kcpClusterClient,
		proxyClusterClient,
		m.proxySharedInformerFactory.Proxy().V1alpha1().WorkspaceProxies(),
		m.kcpSharedInformerFactory.Core().V1alpha1().Shards(),
	//	m.cacheKcpSharedInformerFactory.Core().V1alpha1().Shards(),
	)
	if err != nil {
		return err
	}

	go func() {
		// Wait for shared informer factories to by synced.
		<-m.syncedCh
		c.Start(ctx, 2)
	}()

	return nil
}
