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
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubernetesscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/yaml"

	kcpscheme "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/scheme"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

type existingKcpServer struct {
	name           string
	kubeconfigPath string
	artifacts      string

	config *clientcmdapi.Config
}

func (e *existingKcpServer) Name() string {
	return e.name
}

func (e *existingKcpServer) KubeconfigPath() string {
	return e.kubeconfigPath
}

func (e *existingKcpServer) RawConfig() (clientcmdapi.Config, error) {
	if e.config != nil {
		return *e.config, nil
	}

	config, err := loadConfig(e.kubeconfigPath)
	if err != nil {
		return clientcmdapi.Config{}, err
	}
	e.config = config
	return *e.config, nil
}

func (e *existingKcpServer) BaseConfig(t *testing.T) *rest.Config {
	if e.config == nil {
		_, err := e.RawConfig()
		if err != nil {
			panic(err)
		}
	}

	config := clientcmd.NewNonInteractiveClientConfig(*e.config, "base", nil, nil)
	restConfig, err := config.ClientConfig()
	if err != nil {
		panic(err)
	}
	restConfig.QPS = -1

	return restConfig
}

func (e *existingKcpServer) RootShardSystemMasterBaseConfig(t *testing.T) *rest.Config {
	panic("implement me")
}

func (e *existingKcpServer) ShardSystemMasterBaseConfig(t *testing.T, shard string) *rest.Config {
	panic("implement me")
}

func (e *existingKcpServer) Artifact(t *testing.T, producer func() (runtime.Object, error)) {
	t.Helper()

	subDir := filepath.Join("artifacts", "kcp", e.Name())
	artifactDir, err := framework.CreateTempDirForTest(t, subDir)
	require.NoError(t, err, "could not create artifacts dir")
	// Using t.Cleanup ensures that artifact collection is local to
	// the test requesting retention regardless of server's scope.
	t.Cleanup(func() {
		data, err := producer()
		require.NoError(t, err, "error fetching artifact")

		accessor, ok := data.(metav1.Object)
		require.True(t, ok, "artifact has no object meta: %#v", data)

		dir := path.Join(artifactDir, logicalcluster.From(accessor).String())
		dir = strings.ReplaceAll(dir, ":", "_") // github actions don't like colon because NTFS is unhappy with it in path names
		if accessor.GetNamespace() != "" {
			dir = path.Join(dir, accessor.GetNamespace())
		}
		err = os.MkdirAll(dir, 0755)
		require.NoError(t, err, "could not create dir")

		gvks, _, err := kubernetesscheme.Scheme.ObjectKinds(data)
		if err != nil {
			gvks, _, err = kcpscheme.Scheme.ObjectKinds(data)
		}
		require.NoError(t, err, "error finding gvk for artifact")
		require.NotEmpty(t, gvks, "found no gvk for artifact: %T", data)
		gvk := gvks[0]
		data.GetObjectKind().SetGroupVersionKind(gvk)

		group := gvk.Group
		if group == "" {
			group = "core"
		}

		gvkForFilename := fmt.Sprintf("%s_%s", group, gvk.Kind)

		file := path.Join(dir, fmt.Sprintf("%s-%s.yaml", gvkForFilename, accessor.GetName()))
		file = strings.ReplaceAll(file, ":", "_") // github actions don't like colon because NTFS is unhappy with it in path names

		bs, err := yaml.Marshal(data)
		require.NoError(t, err, "error marshalling artifact")

		err = os.WriteFile(file, bs, 0644)
		require.NoError(t, err, "error writing artifact")
	})
}

func (e *existingKcpServer) ClientCAUserConfig(t *testing.T, config *rest.Config, name string, groups ...string) *rest.Config {
	panic("implement me")
}

func (e *existingKcpServer) CADirectory() string {
	panic("implement me")
}
