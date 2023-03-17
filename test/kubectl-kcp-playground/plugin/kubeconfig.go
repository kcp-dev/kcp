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

package plugin

import (
	"fmt"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func setCurrentContext(path string, newContext string) error {
	config, err := loadConfig(path)
	if err != nil {
		return err
	}

	found := false
	for c := range config.Contexts {
		if c == newContext {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("kubeconfig '%s' dosn't have the '%s' context", path, newContext)
	}
	config.CurrentContext = newContext

	// NOTE: dropping workspace.kcp.io/current and workspace.kcp.io/previous when changing
	// context between shards or back and forth from pclusters.

	delete(config.Contexts, "workspace.kcp.io/current")
	delete(config.Clusters, "workspace.kcp.io/current")
	delete(config.AuthInfos, "workspace.kcp.io/current")

	delete(config.Contexts, "workspace.kcp.io/previous")
	delete(config.Clusters, "workspace.kcp.io/previous")
	delete(config.AuthInfos, "workspace.kcp.io/previous")

	return clientcmd.WriteToFile(*config, path)
}

func loadConfig(path string) (*clientcmdapi.Config, error) {
	// lock config file the same as client-go
	if err := createLockFor(path); err != nil {
		return nil, fmt.Errorf("failed to lock kubeconfig '%s': %v", path, err)
	}
	defer func() {
		_ = deleteLockFor(path)
	}()

	config, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig '%s': %v", path, err)
	}
	return config, nil
}

func mergeFromShard(runningShard framework.RunningServer, dst *clientcmdapi.Config) error {
	prefix := fmt.Sprintf("shard-%s-", runningShard.Name())
	src, err := runningShard.RawConfig()
	if err != nil {
		return err
	}

	if dst.Contexts == nil {
		dst.Contexts = map[string]*clientcmdapi.Context{}
	}
	if dst.Clusters == nil {
		dst.Clusters = map[string]*clientcmdapi.Cluster{}
	}
	if dst.AuthInfos == nil {
		dst.AuthInfos = map[string]*clientcmdapi.AuthInfo{}
	}

	for contextName, context := range src.Contexts {
		cluster, ok := src.Clusters[context.Cluster]
		if !ok {
			return fmt.Errorf("failed to get cluster '%s' config for the '%s' shard", context.Cluster, runningShard.Name())
		}
		dst.Clusters[prefix+context.Cluster] = cluster

		authInfo, ok := src.AuthInfos[context.AuthInfo]
		if !ok {
			return fmt.Errorf("failed to get authInfo '%s' config for the '%s' shard", context.AuthInfo, runningShard.Name())
		}
		dst.AuthInfos[prefix+context.AuthInfo] = authInfo

		dst.Contexts[prefix+contextName] = &clientcmdapi.Context{
			Cluster:  prefix + context.Cluster,
			AuthInfo: prefix + context.AuthInfo,
		}
	}
	return nil
}

func contextForShard(name string) string {
	return fmt.Sprintf("shard-%s-root", name)
}

func mergeFromKind(runningKind *runningKindCluster, dst *clientcmdapi.Config) error {
	prefix := fmt.Sprintf("pcluster-%s-", runningKind.Name())
	src, err := runningKind.RawConfig()
	if err != nil {
		return err
	}

	if dst.Contexts == nil {
		dst.Contexts = map[string]*clientcmdapi.Context{}
	}
	if dst.Clusters == nil {
		dst.Clusters = map[string]*clientcmdapi.Cluster{}
	}
	if dst.AuthInfos == nil {
		dst.AuthInfos = map[string]*clientcmdapi.AuthInfo{}
	}

	context, ok := src.Contexts[src.CurrentContext]
	if !ok {
		return fmt.Errorf("failed to get current context for the '%s' kind cluster", runningKind.Name())
	}

	cluster, ok := src.Clusters[context.Cluster]
	if !ok {
		return fmt.Errorf("failed to get current cluster for the '%s' kind cluster", runningKind.Name())
	}
	dst.Clusters[prefix+"cluster"] = cluster

	authInfo, ok := src.AuthInfos[context.AuthInfo]
	if !ok {
		return fmt.Errorf("failed to get current user for the '%s' kind cluster", runningKind.Name())
	}
	dst.AuthInfos[prefix+"admin"] = authInfo

	dst.Contexts[prefix+"admin"] = &clientcmdapi.Context{
		Cluster:  prefix + "cluster",
		AuthInfo: prefix + "admin",
	}
	return nil
}

func mergeFromFake(runningFake *fakePCluster, dst *clientcmdapi.Config) error {
	prefix := fmt.Sprintf("pcluster-%s-", runningFake.Name())
	src, err := runningFake.RawConfig()
	if err != nil {
		return err
	}

	if dst.Contexts == nil {
		dst.Contexts = map[string]*clientcmdapi.Context{}
	}
	if dst.Clusters == nil {
		dst.Clusters = map[string]*clientcmdapi.Cluster{}
	}
	if dst.AuthInfos == nil {
		dst.AuthInfos = map[string]*clientcmdapi.AuthInfo{}
	}

	context, ok := src.Contexts["base"]
	if !ok {
		return fmt.Errorf("failed to get current context for the '%s' fake cluster", runningFake.Name())
	}

	cluster, ok := src.Clusters[context.Cluster]
	if !ok {
		return fmt.Errorf("failed to get current cluster for the '%s' fake cluster", runningFake.Name())
	}
	dst.Clusters[prefix+"cluster"] = cluster

	authInfo, ok := src.AuthInfos[context.AuthInfo]
	if !ok {
		return fmt.Errorf("failed to get current user for the '%s' fake cluster", runningFake.Name())
	}
	dst.AuthInfos[prefix+"admin"] = authInfo

	dst.Contexts[prefix+"admin"] = &clientcmdapi.Context{
		Cluster:  prefix + "cluster",
		AuthInfo: prefix + "admin",
	}
	return nil
}

func contextForPCluster(name string) string {
	return fmt.Sprintf("pcluster-%s-admin", name)
}
