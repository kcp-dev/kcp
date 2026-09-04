/*
Copyright 2025 The kcp Authors.

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

package server

import (
	"context"
	"fmt"

	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	cachesync "github.com/kcp-dev/kcp/pkg/cache/syncer"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/syncer/replication"
)

func (s *Server) installControllers(ctx context.Context) error {
	if !s.Options.CacheSyncer.Enabled {
		return nil
	}

	logger := klog.FromContext(ctx).WithValues("controller", replication.ControllerName)

	syncer := s.Options.CacheSyncer.Syncer
	peerTLSConfig := rest.TLSClientConfig{
		CAFile:   syncer.PeerCAFile,
		CertFile: syncer.PeerCertFile,
		KeyFile:  syncer.PeerKeyFile,
	}

	if len(syncer.InitialPeerURLs) > 0 {
		if err := cachesync.BootstrapFromPeer(ctx, s.ApiExtensionsClusterClient, s.SyncerSourceConfig, peerTLSConfig, syncer.InitialPeerURLs); err != nil {
			return fmt.Errorf("bootstrap cache from peer: %w", err)
		}
	}

	ctrl, err := replication.NewRootController(
		s.Options.Extra.CacheName,
		s.SyncerSourceConfig,
		peerTLSConfig,
		syncer.InitialPeerURLs,
		s.KcpSharedInformerFactory,
		s.ApiExtensionsSharedInformerFactory,
	)
	if err != nil {
		return fmt.Errorf("failed to create cache-syncer root controller: %w", err)
	}

	logger.Info("starting cache-syncer root controller")
	go ctrl.Start(ctx)
	return nil
}
