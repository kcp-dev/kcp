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

package root

import (
	"context"
	"embed"
	"encoding/base64"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	confighelpers "github.com/kcp-dev/kcp/config/helpers"
)

//go:embed *.yaml
var fs embed.FS

// Bootstrap creates resources in this package by continuously retrying the list.
// This is blocking, i.e. it only returns (with error) when the context is closed or with nil when
// the bootstrapping is successfully completed.
func Bootstrap(ctx context.Context, rootDiscoveryClient discovery.DiscoveryInterface, rootDynamicClient dynamic.Interface, shardName string, shardVirtualWorkspaceURL string, kubeconfig clientcmdapi.Config, homePrefix string, homeWorkspaceCreatorGroups []string) error {
	kubeconfigRaw, err := clientcmd.Write(kubeconfig)
	if err != nil {
		return err
	}

	homeWorkspaceCreatorGroupReplacement := ""
	for _, group := range homeWorkspaceCreatorGroups {
		homeWorkspaceCreatorGroupReplacement += `
- apiGroup: rbac.authorization.k8s.io
  kind: Group
  name: ` + group
	}
	if homeWorkspaceCreatorGroupReplacement == "" {
		homeWorkspaceCreatorGroupReplacement = "[]"
	}

	return confighelpers.Bootstrap(ctx, rootDiscoveryClient, rootDynamicClient, fs, confighelpers.ReplaceOption(
		"SHARD_NAME", shardName,
		"SHARD_VIRTUAL_WORKSPACE_URL", shardVirtualWorkspaceURL,
		"SHARD_KUBECONFIG", base64.StdEncoding.EncodeToString(kubeconfigRaw),
		"HOME_CREATOR_GROUPS", homeWorkspaceCreatorGroupReplacement,
		"HOMEPREFIX", homePrefix,
	))
}
