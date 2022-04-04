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

package framework

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/kind/pkg/apis/config/v1alpha4"
	"sigs.k8s.io/kind/pkg/cluster"

	"github.com/kcp-dev/kcp/pkg/client/clientset/versioned/scheme"
)

type kindCluster struct {
	name            string
	clusterProvider *cluster.Provider
	clusterConfig   v1alpha4.Cluster
	ctx             context.Context
	dataDir         string
	artifactDir     string
	Running         bool
	lock            *sync.Mutex
	cfg             clientcmd.ClientConfig
	kubeconfigPath  string

	t *testing.T
}

func NewKindCluster(t *testing.T, cfg KindConfig, artifactDir, dataDir string) (*kindCluster, error) {
	t.Helper()

	// Default configuration, 1 single node cluster
	clusterConfig := v1alpha4.Cluster{
		TypeMeta: v1alpha4.TypeMeta{},
		Name:     strings.ToLower(cfg.Name),
		Nodes: []v1alpha4.Node{
			{
				Role: "control-plane",
			},
		},
	}

	artifactDir = filepath.Join(artifactDir, "kind", cfg.Name)
	if err := os.MkdirAll(artifactDir, 0755); err != nil {
		return nil, fmt.Errorf("could not create artifact dir: %w", err)
	}
	dataDir = filepath.Join(dataDir, "kind", cfg.Name)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("could not create data dir: %w", err)
	}

	return &kindCluster{
		name:            cfg.Name,
		clusterProvider: cluster.NewProvider(),
		clusterConfig:   clusterConfig,
		ctx:             nil,
		dataDir:         dataDir,
		artifactDir:     artifactDir,
		lock:            &sync.Mutex{},
		cfg:             nil,
		kubeconfigPath:  "",
		t:               t,
	}, nil
}

func (kc *kindCluster) Run(t *testing.T) error {
	t.Helper()
	kc.lock.Lock()
	defer kc.lock.Unlock()

	kc.t = t

	kc.ctx = context.Background()
	ctx, cleanupCancel := context.WithCancel(context.Background())
	kc.t.Cleanup(func() {
		kc.t.Logf("cleanup: cleaning kind server %s", kc.name)
		err := kc.clusterProvider.CollectLogs(kc.clusterConfig.Name, kc.artifactDir)
		if err != nil {
			kc.t.Log("cleanup: could not collect logs")
		}
		err = kc.clusterProvider.Delete(kc.clusterConfig.Name, kc.kubeconfigPath)
		if err != nil {
			kc.t.Log("cleanup: could not delete cluster")
		}
		cleanupCancel()
		<-ctx.Done()
	})
	kc.ctx = ctx

	err := kc.clusterProvider.Create(kc.clusterConfig.Name, cluster.CreateWithV1Alpha4Config(&kc.clusterConfig))
	if err != nil {
		cleanupCancel()
		return fmt.Errorf("could not create kind cluster: %w", err)
	}

	kc.kubeconfigPath = filepath.Join(kc.dataDir, fmt.Sprintf("kubeconfig-%s", kc.name))

	err = kc.clusterProvider.ExportKubeConfig(kc.clusterConfig.Name, kc.kubeconfigPath, false)
	if err != nil {
		cleanupCancel()
		return fmt.Errorf("could not export kubeconfig: %w", err)
	}

	kc.t.Logf("kind server %s is running", kc.name)

	kc.Running = true
	return nil
}

func (kc *kindCluster) Stop(t *testing.T) error {
	t.Helper()
	kc.lock.Lock()
	defer kc.lock.Unlock()

	if !kc.Running {
		t.Logf("can't stop kind server %s, is not running", kc.name)
		return nil
	}

	return kc.clusterProvider.Delete(kc.name, kc.kubeconfigPath)
}

// WaitForReady blocks until the kind cluster is ready or timeouts
func (kc *kindCluster) WaitForReady() error {
	if !kc.Running {
		kc.t.Logf("can't wait for ready state of kind server %s, is not running", kc.name)
		return nil
	}

	conf := kc.DefaultConfig(kc.t)
	if conf.NegotiatedSerializer == nil {
		conf.NegotiatedSerializer = scheme.Codecs
	}
	client, err := rest.UnversionedRESTClientFor(conf)
	require.NoError(kc.t, err, "could not create rest client")

	wg := sync.WaitGroup{}
	wg.Add(2)
	for _, endpoint := range []string{"/livez", "/readyz"} {
		go func(endpoint string) {
			defer wg.Done()
			kc.waitForEndpoint(client, endpoint)
		}(endpoint)
	}
	wg.Wait()

	return nil
}

func (c *kindCluster) waitForEndpoint(client *rest.RESTClient, endpoint string) {
	var lastError error
	if err := wait.PollImmediateWithContext(c.ctx, 100*time.Millisecond, time.Minute, func(ctx context.Context) (bool, error) {
		req := rest.NewRequest(client).RequestURI(endpoint)
		_, err := req.Do(ctx).Raw()
		if err != nil {
			lastError = fmt.Errorf("error contacting %s: %w", req.URL(), err)
			return false, nil
		}

		c.t.Logf("success contacting %s", req.URL())
		return true, nil
	}); err != nil && lastError != nil {
		c.t.Error(lastError)
	}
}

func (kc *kindCluster) Name() string {
	return kc.name
}

func (kc *kindCluster) KubeconfigPath() string {
	return kc.kubeconfigPath
}

func (kc *kindCluster) RawConfig() (clientcmdapi.Config, error) {
	return clientcmdapi.Config{}, nil
}

func (kc *kindCluster) DefaultConfig(t *testing.T) *rest.Config {
	restConfig, err := clientcmd.BuildConfigFromFlags("", kc.kubeconfigPath)
	require.NoError(t, err, "could not build rest config")
	return restConfig
}

func (kc *kindCluster) Artifact(t *testing.T, producer func() (runtime.Object, error)) {
	artifact(t, kc, producer)
}
