/*
Copyright 2021 The KCP Authors.

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

package proxy

import (
	"context"
	"fmt"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/pkg/version"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	proxyv1alpha1 "github.com/kcp-dev/kcp/proxy/apis/proxy/v1alpha1"
	proxylientset "github.com/kcp-dev/kcp/proxy/client/clientset/versioned"
	proxyclusterclientset "github.com/kcp-dev/kcp/proxy/client/clientset/versioned/cluster"
	proxyinformers "github.com/kcp-dev/kcp/proxy/client/informers/externalversions"
	. "github.com/kcp-dev/kcp/proxy/logging"
)

const (
	resyncPeriod = 10 * time.Hour

	// TODO(marun) Coordinate this value with the interval configured for the heartbeat controller.
	heartbeatInterval = 20 * time.Second
)

// ProxyConfig defines the proxy configuration that is guaranteed to
// vary across proxy deployments. Capturing these details in a struct
// simplifies defining these details in test fixture.
type ProxyConfig struct {
	UpstreamConfig   *rest.Config
	DownstreamConfig *rest.Config
	ProxyTargetPath  logicalcluster.Path
	ProxyTargetName  string
	ProxyTargetUID   string
}

func StartProxy(ctx context.Context, cfg *ProxyConfig, numProxyThreads int, proxyNamespace string) error {
	logger := klog.FromContext(ctx)
	logger = logger.WithValues(ProxyTargetWorkspace, cfg.ProxyTargetPath, ProxyTargetName, cfg.ProxyTargetName)

	logger.V(2).Info("starting proxy")

	proxyVersion := version.Get().GitVersion

	bootstrapConfig := rest.CopyConfig(cfg.UpstreamConfig)
	rest.AddUserAgent(bootstrapConfig, "kcp#proxy/"+proxyVersion)

	proxyBootstrapClusterClient, err := proxyclusterclientset.NewForConfig(bootstrapConfig)
	if err != nil {
		return err
	}

	proxyClusterTargetClient := proxyBootstrapClusterClient.Cluster(cfg.ProxyTargetPath)

	// proxyTargetInformerFactory to watch a certain proxy target
	proxyTargetInformerFactory := proxyinformers.NewSharedScopedInformerFactoryWithOptions(proxyClusterTargetClient, resyncPeriod, proxyinformers.WithTweakListOptions(
		func(listOptions *metav1.ListOptions) {
			listOptions.FieldSelector = fields.OneTermEqualSelector("metadata.name", cfg.ProxyTargetName).String()
		},
	))

	logger.Info("attempting to retrieve the Proxy target resource")
	var proxyTarget *proxyv1alpha1.WorkspaceProxy
	err = wait.PollImmediateInfinite(5*time.Second, func() (bool, error) {
		var err error
		proxyTarget, err = proxyClusterTargetClient.ProxyV1alpha1().WorkspaceProxies().Get(ctx, cfg.ProxyTargetName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		// If the ProxyTargetUID flag is set, we compare the provided value with the kcp proxy target uid, if the values don't match
		// the proxy will refuse to work.
		if cfg.ProxyTargetUID != "" && cfg.ProxyTargetUID != string(proxyTarget.UID) {
			return false, fmt.Errorf("unexpected Proxy target UID %s, expected %s, refusing to proxy", proxyTarget.UID, cfg.ProxyTargetUID)
		}

		// We need to update first time otherwise first patch on non-existing status will fail.
		// TODO: this will change once we add first reconcilers
		copy := proxyTarget.DeepCopy()
		copy.Status = proxyv1alpha1.WorkspaceProxyStatus{
			Phase: proxyv1alpha1.WorkspaceProxyPhaseInitializing,
		}

		_, err = proxyClusterTargetClient.ProxyV1alpha1().WorkspaceProxies().UpdateStatus(ctx, copy, metav1.UpdateOptions{})
		if err != nil {
			return false, err
		}

		return true, nil

	})
	if err != nil {
		return err
	}

	// TODO: Proxy logic here
	proxyTargetInformerFactory.Start(ctx.Done())
	proxyTargetInformerFactory.Proxy().V1alpha1().WorkspaceProxies().Informer()
	proxyTargetInformerFactory.WaitForCacheSync(ctx.Done())

	startHeartbeat(ctx, proxyClusterTargetClient, cfg.ProxyTargetName, cfg.ProxyTargetUID)

	return nil
}

func startHeartbeat(ctx context.Context, client proxylientset.Interface, proxyTargetName, proxyTargetUID string) {
	logger := klog.FromContext(ctx)

	// Attempt to heartbeat every interval
	go wait.UntilWithContext(ctx, func(ctx context.Context) {
		var heartbeatTime time.Time

		// TODO(marun) Figure out a strategy for backoff to avoid a thundering herd problem with lots of proxy
		// Attempt to heartbeat every second until successful. Errors are logged instead of being returned so the
		// poll error can be safely ignored.
		_ = wait.PollImmediateInfiniteWithContext(ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
			patchBytes := []byte(fmt.Sprintf(`[{"op":"test","path":"/metadata/uid","value":%q},{"op":"replace","path":"/status/lastProxyHeartbeatTime","value":%q}]`, proxyTargetUID, time.Now().Format(time.RFC3339)))
			syncTarget, err := client.ProxyV1alpha1().WorkspaceProxies().Patch(ctx, proxyTargetName, types.JSONPatchType, patchBytes, metav1.PatchOptions{}, "status")
			if err != nil {
				logger.Error(err, "failed to set status.lastProxyHeartbeatTime")
				return false, nil
			}
			if syncTarget.Status.LastProxyHeartbeatTime == nil {
				logger.Error(fmt.Errorf("unexpected nil status.lastProxyHeartbeatTime"), "failed to set status.lastProxyHeartbeatTime")
				return false, nil
			}
			heartbeatTime = syncTarget.Status.LastProxyHeartbeatTime.Time
			return true, nil
		})
		logger.V(5).Info("Heartbeat set", "heartbeatTime", heartbeatTime)
	}, heartbeatInterval)
}
