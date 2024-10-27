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

package reconciler

import (
	"context"
	"fmt"
	"strings"
	"time"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	kcpclusterclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/logicalcluster/v3"
	"golang.org/x/sync/errgroup"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/pkg/version"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	mountsclusterclientset "github.com/kcp-dev/kcp/contrib/mounts-vw/client/clientset/versioned/cluster"
	mountsinformers "github.com/kcp-dev/kcp/contrib/mounts-vw/client/informers/externalversions"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/state"
)

/*
The manager is in charge of instantiating and starting the controllers
that do the reconciliation for Mounts .
*/

const (
	targetsAPIExportName = "targets.contrib.kcp.io"
	mountsAPIExportName  = "mounts.contrib.kcp.io"
	providersClusterPath = "root:providers:mounts"

	resyncPeriod = 10 * time.Hour
)

// ShardManager is the interface for the manager for a shard
type shardManager struct {
	name                string
	rootKCPAdminConfig  rest.Config
	shardClientConfig   rest.Config
	virtualWorkspaceURL string

	store state.ClientSetStoreInterface

	mountsSharedInformerFactory mountsinformers.SharedInformerFactory

	stopCh   chan struct{}
	syncedCh chan struct{}
}

type Manager struct {
	// root:providers:mounts:targets.contrib.kcp.io
	mountsTargetsExportShards map[string]shardManager

	// root:providers:mounts:mounts.contrib.kcp.io
	mountsMountsExportShards map[string]shardManager
}

// NewManager creates a manager able to start controllers
func NewManager(ctx context.Context, virtualWorkspaceURL string, kcpAdminRestConfig *rest.Config, store state.ClientSetStoreInterface) (*Manager, error) {
	logger := klog.FromContext(ctx)
	m := &Manager{
		mountsTargetsExportShards: map[string]shardManager{},
		mountsMountsExportShards:  map[string]shardManager{},
	}

	logger.Info("setting up targets manager")
	restClients, err := restConfigForAPIExport(ctx, kcpAdminRestConfig, targetsAPIExportName, logicalcluster.NewPath(providersClusterPath))
	if err != nil {
		return nil, err
	}
	targetsShards, err := setupShardManager(ctx, virtualWorkspaceURL, restClients, store)
	if err != nil {
		return nil, err
	}

	m.mountsTargetsExportShards = targetsShards

	logger.Info("setting up mounts manager")
	restClients, err = restConfigForAPIExport(ctx, kcpAdminRestConfig, mountsAPIExportName, logicalcluster.NewPath(providersClusterPath))
	if err != nil {
		return nil, err
	}
	mountsShards, err := setupShardManager(ctx, virtualWorkspaceURL, restClients, store)
	if err != nil {
		return nil, err
	}
	m.mountsMountsExportShards = mountsShards

	return m, nil

}

func setupShardManager(ctx context.Context, virtualWorkspaceURL string, clients []rest.Config, store state.ClientSetStoreInterface) (map[string]shardManager, error) {
	logger := klog.FromContext(ctx)
	logger.Info("shards found", "count", len(clients))
	shards := make(map[string]shardManager, len(clients))
	for _, c := range clients {
		name := strings.Split(strings.TrimPrefix(c.Host, "https://"), ".")[0]
		logger.Info("Shard", "name", name, "host", c.Host)

		informerMountsClient, err := mountsclusterclientset.NewForConfig(&c)
		if err != nil {
			return nil, err
		}

		mountsSharedInformerFactory := mountsinformers.NewSharedInformerFactoryWithOptions(
			informerMountsClient,
			resyncPeriod,
		)

		s := shardManager{
			name:                        name,
			virtualWorkspaceURL:         virtualWorkspaceURL,
			shardClientConfig:           c,
			store:                       store,
			stopCh:                      make(chan struct{}),
			syncedCh:                    make(chan struct{}),
			mountsSharedInformerFactory: mountsSharedInformerFactory,
		}

		shards[s.name] = s
	}

	return shards, nil
}

// Start starts informers and controller instances
func (m Manager) Start(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	logger.V(2).Info("starting manager")

	// HACK: tenancy controller is needed only on the start where tenancy workspace is
	// scheduled.
	g := errgroup.Group{}

	for _, s := range m.mountsTargetsExportShards {
		ss := s
		g.Go(func() error {
			if err := ss.installTargetKubeClustersController(ctx); err != nil {
				return err
			}
			err := ss.start(ctx, "targets")
			if err != nil {
				return err
			}
			return nil
		})
	}

	for _, s := range m.mountsMountsExportShards {
		ss := s
		g.Go(func() error {
			if err := ss.installMountsController(ctx); err != nil {
				return err
			}
			err := ss.start(ctx, "mounts")
			if err != nil {
				return err
			}
			return nil
		})
	}

	return g.Wait()
}

func (s *shardManager) start(ctx context.Context, export string) error {
	logger := klog.FromContext(ctx).WithValues("shard", s.name, "export", export)

	s.mountsSharedInformerFactory.Start(s.stopCh)

	logger.Info("waiting for targets informers sync")
	wg := errgroup.Group{}
	wg.Go(func() error {
		s.mountsSharedInformerFactory.WaitForCacheSync(s.stopCh)
		return nil
	})
	wg.Wait()

	logger.Info("all informers synced, ready to start controllers")
	close(s.syncedCh)
	return nil
}

// restConfigForAPIExport returns a *rest.Config properly configured to communicate with the endpoint for the
// APIExport's virtual workspace. cfg is the bootstrap config, shardsClientConfig is the config for the shards clusters.
func restConfigForAPIExport(ctx context.Context, kcpAdminRestConfig *rest.Config, apiExportName string, cluster logicalcluster.Path) ([]rest.Config, error) {
	logger := klog.FromContext(ctx)
	logger.V(2).Info("getting apiexport")

	bootstrapConfig := rest.CopyConfig(kcpAdminRestConfig)
	proxyVersion := version.Get().GitVersion
	rest.AddUserAgent(bootstrapConfig, "kcp#proxy/bootstrap/"+proxyVersion)
	bootstrapClient, err := kcpclusterclientset.NewForConfig(bootstrapConfig)
	if err != nil {
		return nil, err
	}

	var apiExport *apisv1alpha1.APIExport
	if apiExportName != "" {
		if apiExport, err = bootstrapClient.ApisV1alpha1().APIExports().Cluster(cluster).Get(ctx, apiExportName, metav1.GetOptions{}); err != nil {
			return nil, fmt.Errorf("error getting APIExport %q: %w", apiExportName, err)
		}
	} else {
		logger := klog.FromContext(ctx)
		logger.V(2).Info("api-export-name is empty - listing")
		exports := &apisv1alpha1.APIExportList{}
		if exports, err = bootstrapClient.ApisV1alpha1().APIExports().List(ctx, metav1.ListOptions{}); err != nil {
			return nil, fmt.Errorf("error listing APIExports: %w", err)
		}
		if len(exports.Items) == 0 {
			return nil, fmt.Errorf("no APIExport found")
		}
		if len(exports.Items) > 1 {
			return nil, fmt.Errorf("more than one APIExport found")
		}
		apiExport = &exports.Items[0]
	}

	if len(apiExport.Status.VirtualWorkspaces) < 1 {
		return nil, fmt.Errorf("APIExport %q status.virtualWorkspaces is empty", apiExportName)
	}

	var results []rest.Config
	// TODO(mjudeikis): This should use separate rest.Config for direct shard communication.
	// KCPAdmin pointing to frontproxy would not works. This works only with `kcp start`
	for _, ws := range apiExport.Status.VirtualWorkspaces {
		logger.Info("virtual workspace", "url", ws.URL)
		cfg := rest.CopyConfig(kcpAdminRestConfig)
		cfg.Host = ws.URL
		results = append(results, *cfg)
	}

	return results, nil
}
