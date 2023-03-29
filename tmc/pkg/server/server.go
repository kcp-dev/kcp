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

package server

import (
	"context"
	_ "net/http/pprof"

	"k8s.io/apimachinery/pkg/util/sets"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	configrootcompute "github.com/kcp-dev/kcp/config/rootcompute"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	coreserver "github.com/kcp-dev/kcp/pkg/server"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
)

type Server struct {
	CompletedConfig

	Core *coreserver.Server
}

func NewServer(c CompletedConfig) (*Server, error) {
	core, err := coreserver.NewServer(c.Core)
	if err != nil {
		return nil, err
	}

	s := &Server{
		CompletedConfig: c,

		Core: core,
	}

	return s, nil
}

func (s *Server) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithValues("component", "kcp")
	ctx = klog.NewContext(ctx, logger)

	controllerConfig := rest.CopyConfig(s.Core.IdentityConfig)

	enabled := sets.NewString(s.Options.Core.Controllers.IndividuallyEnabled...)
	if len(enabled) > 0 {
		logger.WithValues("controllers", enabled).Info("starting controllers individually")
	}

	if s.Options.Core.Controllers.EnableAll || enabled.Has("cluster") {
		// bootstrap root compute workspace
		computeBoostrapHookName := "rootComputeBoostrap"
		if err := s.Core.AddPostStartHook(computeBoostrapHookName, func(hookContext genericapiserver.PostStartHookContext) error {
			logger := logger.WithValues("postStartHook", computeBoostrapHookName)
			if s.Core.Options.Extra.ShardName == corev1alpha1.RootShard {
				// the root ws is only present on the root shard
				logger.Info("waiting to bootstrap root compute workspace until root phase1 is complete")
				s.Core.WaitForPhase1Finished()

				logger.Info("starting bootstrapping root compute workspace")
				if err := configrootcompute.Bootstrap(goContext(hookContext),
					s.Core.BootstrapApiExtensionsClusterClient,
					s.Core.BootstrapDynamicClusterClient,
					sets.NewString(s.Core.Options.Extra.BatteriesIncluded...),
				); err != nil {
					logger.Error(err, "failed to bootstrap root compute workspace")
					return nil // don't klog.Fatal. This only happens when context is cancelled.
				}
				logger.Info("finished bootstrapping root compute workspace")
			}
			return nil
		}); err != nil {
			return err
		}

		// TODO(marun) Consider enabling each controller via a separate flag
		if err := s.installApiResourceController(ctx, controllerConfig); err != nil {
			return err
		}
		if err := s.installSyncTargetHeartbeatController(ctx, controllerConfig); err != nil {
			return err
		}
		if err := s.installSyncTargetController(ctx, controllerConfig); err != nil {
			return err
		}
		if err := s.installWorkloadsSyncTargetExportController(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Core.Controllers.EnableAll || enabled.Has("resource-scheduler") {
		if err := s.installWorkloadResourceScheduler(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if kcpfeatures.DefaultFeatureGate.Enabled(kcpfeatures.LocationAPI) {
		if s.Options.Core.Controllers.EnableAll || enabled.Has("scheduling") {
			if err := s.installWorkloadNamespaceScheduler(ctx, controllerConfig); err != nil {
				return err
			}
			if err := s.installWorkloadPlacementScheduler(ctx, controllerConfig); err != nil {
				return err
			}
			if err := s.installSchedulingLocationStatusController(ctx, controllerConfig); err != nil {
				return err
			}
			if err := s.installSchedulingPlacementController(ctx, controllerConfig); err != nil {
				return err
			}
			if err := s.installWorkloadsAPIExportController(ctx, controllerConfig); err != nil {
				return err
			}
			if err := s.installWorkloadsDefaultLocationController(ctx, controllerConfig); err != nil {
				return err
			}
		}
	}

	return s.Core.Run(ctx)
}

// goContext turns the PostStartHookContext into a context.Context for use in routines that may or may not
// run inside of a post-start-hook. The k8s APIServer wrote the post-start-hook context code before contexts
// were part of the Go stdlib.
func goContext(parent genericapiserver.PostStartHookContext) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	go func(done <-chan struct{}) {
		<-done
		cancel()
	}(parent.StopCh)
	return ctx
}
