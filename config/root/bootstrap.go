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

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	confighelpers "github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/pkg/apis/apiresource"
	"github.com/kcp-dev/kcp/pkg/apis/cluster"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
)

//go:embed *.yaml
var fs embed.FS

// Bootstrap creates CRDs and the resources in this package by continuously retrying the list.
// This is blocking, i.e. it only returns (with error) when the context is closed or with nil when
// the bootstrapping is successfully completed.
func Bootstrap(ctx context.Context, rootCrdClient apiextensionsclient.Interface, rootDynamicClient dynamic.Interface, shardName string, kubeconfig clientcmdapi.Config) error {
	kubeconfigRaw, err := clientcmd.Write(kubeconfig)
	if err != nil {
		return err
	}

	return confighelpers.Bootstrap(ctx, rootCrdClient, rootDynamicClient, fs, []metav1.GroupResource{
		{Group: tenancy.GroupName, Resource: "clusterworkspaces"},
		{Group: tenancy.GroupName, Resource: "clusterworkspacetypes"},
		{Group: tenancy.GroupName, Resource: "workspaceshards"},
		{Group: apiresource.GroupName, Resource: "apiresourceimports"},
		{Group: apiresource.GroupName, Resource: "negotiatedapiresources"},
		{Group: cluster.GroupName, Resource: "clusters"},
	}, confighelpers.ReplaceOption(
		"SHARD_NAME", shardName,
		"SHARD_KUBECONFIG", base64.StdEncoding.EncodeToString(kubeconfigRaw),
	))
}
