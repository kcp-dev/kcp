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

type RunFunc func(ctx context.Context)
type WaitFunc func(ctx context.Context, s *Server) error

type controllerWrapper struct {
	Name   string
	Runner RunFunc
	Wait   WaitFunc
}

func (s *Server) installControllers(ctx context.Context) error {
	if s.Options.CacheSyncer.Enabled {
		if err := s.installCacheSyncerController(ctx); err != nil {
			return err
		}
	}
	s.startControllers(ctx)
	return nil
}

func (s *Server) installCacheSyncerController(ctx context.Context) error {
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

	logger.Info("registering cache-syncer root controller")
	return s.registerController(&controllerWrapper{
		Name: replication.ControllerName,
		Runner: func(ctx context.Context) {
			ctrl.Start(ctx)
		},
	})
}

func (s *Server) startControllers(ctx context.Context) {
	for _, controller := range s.controllers {
		go s.runController(ctx, controller)
	}
}

// startControllersWithoutLeaderElection starts controllers that must run on every replica
// regardless of leader election.
func (s *Server) startControllersWithoutLeaderElection(ctx context.Context) {
	for _, controller := range s.controllersWithoutLeaderElection {
		go s.runController(ctx, controller)
	}
}

func (s *Server) runController(ctx context.Context, controller *controllerWrapper) {
	log := klog.FromContext(ctx).WithValues("controller", controller.Name)
	if controller.Wait != nil {
		log.Info("waiting for sync")
		if err := controller.Wait(ctx, s); err != nil {
			log.Error(err, "failed to wait for sync")
			return
		}
	}
	log.Info("starting controller")
	controller.Runner(ctx)
}

func (s *Server) registerController(controller *controllerWrapper) error {
	if s.controllers[controller.Name] != nil {
		return fmt.Errorf("controller %s is already registered", controller.Name)
	}
	s.controllers[controller.Name] = controller
	return nil
}
