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

package cache

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apimachineryerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	kubernetesscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	cacheserver "github.com/kcp-dev/kcp/pkg/cache/server"
	cacheopitons "github.com/kcp-dev/kcp/pkg/cache/server/options"
	"github.com/kcp-dev/kcp/pkg/embeddedetcd"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// StartStandaloneCacheServer runs the cache server as a separate process
// and returns a path to kubeconfig that can be used to communicate with the server.
func StartStandaloneCacheServer(ctx context.Context, t *testing.T, dataDir string) string {
	t.Helper()

	cacheServerPortStr, err := framework.GetFreePort(t)
	require.NoError(t, err)
	cacheServerPort, err := strconv.Atoi(cacheServerPortStr)
	require.NoError(t, err)
	cacheServerOptions := cacheopitons.NewOptions(path.Join(dataDir, "cache"))
	cacheServerOptions.SecureServing.BindPort = cacheServerPort
	cacheServerEmbeddedEtcdClientPort, err := framework.GetFreePort(t)
	require.NoError(t, err)
	cacheServerEmbeddedEtcdPeerPort, err := framework.GetFreePort(t)
	require.NoError(t, err)
	cacheServerOptions.EmbeddedEtcd.ClientPort = cacheServerEmbeddedEtcdClientPort
	cacheServerOptions.EmbeddedEtcd.PeerPort = cacheServerEmbeddedEtcdPeerPort
	cacheServerCompletedOptions, err := cacheServerOptions.Complete()
	require.NoError(t, err)
	if errs := cacheServerCompletedOptions.Validate(); len(errs) > 0 {
		require.NoError(t, apimachineryerrors.NewAggregate(errs))
	}
	cacheServerConfig, err := cacheserver.NewConfig(cacheServerCompletedOptions, nil)
	require.NoError(t, err)
	cacheServerCompletedConfig, err := cacheServerConfig.Complete()
	require.NoError(t, err)

	if cacheServerCompletedConfig.EmbeddedEtcd.Config != nil {
		t.Logf("Starting embedded etcd for the cache server")
		require.NoError(t, embeddedetcd.NewServer(cacheServerCompletedConfig.EmbeddedEtcd).Run(ctx))
	}
	cacheServer, err := cacheserver.NewServer(cacheServerCompletedConfig)
	require.NoError(t, err)
	preparedCachedServer, err := cacheServer.PrepareRun(ctx)
	require.NoError(t, err)
	start := time.Now()
	t.Logf("Starting the cache server")
	go func() {
		require.NoError(t, preparedCachedServer.Run(ctx))
	}()

	cacheServerCertificatePath := path.Join(dataDir, "cache", "apiserver.crt")
	framework.Eventually(t, func() (bool, string) {
		if _, err = os.Stat(cacheServerCertificatePath); os.IsNotExist(err) {
			return false, "Failed to read the cache server's certificate, the file hasn't been created"
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "Waiting for the cache server's certificate file at %s", cacheServerCertificatePath)

	t.Logf("Creating kubeconfig for the cache server at %s", dataDir)
	cacheServerCert, err := os.ReadFile(cacheServerCertificatePath)
	require.NoError(t, err)
	cacheServerKubeConfig := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"cache": {
				Server:                   fmt.Sprintf("https://localhost:%s", cacheServerPortStr),
				CertificateAuthorityData: cacheServerCert,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"cache": {
				Cluster: "cache",
			},
		},
		CurrentContext: "cache",
	}
	cacheKubeconfigPath := filepath.Join(dataDir, "cache", "cache.kubeconfig")
	err = clientcmd.WriteToFile(cacheServerKubeConfig, cacheKubeconfigPath)
	require.NoError(t, err)

	cacheClientConfig := clientcmd.NewNonInteractiveClientConfig(cacheServerKubeConfig, "cache", nil, nil)
	cacheClientRestConfig, err := cacheClientConfig.ClientConfig()
	require.NoError(t, err)
	t.Logf("Waiting for the cache server at %v to become ready", cacheClientRestConfig.Host)
	waitUntilCacheServerIsReady(ctx, t, cacheClientRestConfig)

	if t.Failed() {
		t.Fatal("Fixture setup failed: cache server did not become ready")
	}

	t.Logf("Started cache server after %s", time.Since(start))

	return cacheKubeconfigPath
}

func ClientRoundTrippersFor(cfg *rest.Config) *rest.Config {
	cacheClientRT := cacheclient.WithCacheServiceRoundTripper(rest.CopyConfig(cfg))
	cacheClientRT = cacheclient.WithShardNameFromContextRoundTripper(cacheClientRT)
	cacheClientRT = cacheclient.WithDefaultShardRoundTripper(cacheClientRT, shard.Wildcard)
	cacheClientRT.ContentConfig.ContentType = "application/json"
	return cacheClientRT
}

func waitUntilCacheServerIsReady(ctx context.Context, t *testing.T, cacheClientRT *rest.Config) {
	t.Helper()
	cacheClientRT = rest.CopyConfig(cacheClientRT)
	if cacheClientRT.NegotiatedSerializer == nil {
		cacheClientRT.NegotiatedSerializer = kubernetesscheme.Codecs.WithoutConversion()
	}
	client, err := rest.UnversionedRESTClientFor(cacheClientRT)
	if err != nil {
		t.Fatalf("failed to create unversioned client: %v", err)
	}

	wg := sync.WaitGroup{}
	wg.Add(2)
	for _, endpoint := range []string{"/livez", "/readyz"} {
		go func(endpoint string) {
			defer wg.Done()
			waitForEndpoint(ctx, t, client, endpoint)
		}(endpoint)
	}
	wg.Wait()
}

func waitForEndpoint(ctx context.Context, t *testing.T, client *rest.RESTClient, endpoint string) {
	t.Helper()
	var lastError error
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, time.Minute, true, func(ctx context.Context) (bool, error) {
		req := rest.NewRequest(client).RequestURI(endpoint)
		_, err := req.Do(ctx).Raw()
		if err != nil {
			lastError = fmt.Errorf("error contacting %s: failed components: %v", req.URL(), unreadyComponentsFromError(err))
			return false, nil
		}

		t.Logf("success contacting %s", req.URL())
		return true, nil
	}); err != nil && lastError != nil {
		t.Error(lastError)
	}
}

// there doesn't seem to be any simple way to get a metav1.Status from the Go client, so we get
// the content in a string-formatted error, unfortunately.
func unreadyComponentsFromError(err error) string {
	innerErr := strings.TrimPrefix(strings.TrimSuffix(err.Error(), `") has prevented the request from succeeding`), `an error on the server ("`)
	var unreadyComponents []string
	for _, line := range strings.Split(innerErr, `\n`) {
		if name := strings.TrimPrefix(strings.TrimSuffix(line, ` failed: reason withheld`), `[-]`); name != line {
			// NB: sometimes the error we get is truncated (server-side?) to something like: `\n[-]poststar") has prevented the request from succeeding`
			// In those cases, the `name` here is also truncated, but nothing we can do about that. For that reason, we don't expose a list of components
			// from this function or else we'd need to handle more edge cases.
			unreadyComponents = append(unreadyComponents, name)
		}
	}
	return strings.Join(unreadyComponents, ", ")
}
