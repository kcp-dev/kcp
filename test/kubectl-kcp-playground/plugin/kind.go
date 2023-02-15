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
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

type kindConfig struct {
	Name           string
	KubeConfigPath string
}

func kindCluster(t *testing.T, cfg kindConfig) *runningKindCluster {
	commandLine := []string{
		"kind",
		"create",
		"cluster",
		fmt.Sprintf("--name=%s", cfg.Name),
		"-q",
	}

	if cfg.KubeConfigPath != "" {
		commandLine = append(commandLine, fmt.Sprintf("--kubeconfig=%s", cfg.KubeConfigPath))
	}

	_, err := runCmd(commandLine)
	if err != nil {
		t.Fatalf("failed to create '%s' kind cluster: %v", cfg.Name, err)
	}

	t.Cleanup(func() {
		commandLine := []string{
			"kind",
			"delete",
			"cluster",
			fmt.Sprintf("--name=%s", cfg.Name),
		}

		_, err := runCmd(commandLine)
		if err != nil {
			t.Errorf("failed to delete '%s' kind cluster: %v", cfg.Name, err)
		}
	})

	return &runningKindCluster{
		name:           cfg.Name,
		kubeconfigPath: cfg.KubeConfigPath,
	}
}

type runningKindCluster struct {
	name           string
	kubeconfigPath string
	syncerImage    string
}

func (k *runningKindCluster) Name() string {
	return k.name
}

func (k *runningKindCluster) Type() PClusterType {
	return KindPClusterType
}

func (k *runningKindCluster) KubeconfigPath() string {
	return k.kubeconfigPath
}

func (k *runningKindCluster) SyncerImage() string {
	return k.syncerImage
}

func (k *runningKindCluster) SetSyncerImage(image string) {
	k.syncerImage = image
}

func (k *runningKindCluster) RawConfig() (clientcmdapi.Config, error) {
	config, err := clientcmd.LoadFromFile(k.kubeconfigPath)
	if err != nil {
		return clientcmdapi.Config{}, fmt.Errorf("failed to load kubeconfig '%s' for the '%s' kind cluster: %v", k.kubeconfigPath, k.name, err)
	}
	return *config, nil
}
