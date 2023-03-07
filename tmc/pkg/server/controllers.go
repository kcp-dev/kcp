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

package server

import (
	"context"
	"fmt"
	_ "net/http/pprof"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"

	kcpapiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/kcp/clientset/versioned"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apiresource"
	schedulinglocationstatus "github.com/kcp-dev/kcp/pkg/reconciler/scheduling/location"
	schedulingplacement "github.com/kcp-dev/kcp/pkg/reconciler/scheduling/placement"
	workloadsapiexport "github.com/kcp-dev/kcp/pkg/reconciler/workload/apiexport"
	workloadsapiexportcreate "github.com/kcp-dev/kcp/pkg/reconciler/workload/apiexportcreate"
	"github.com/kcp-dev/kcp/pkg/reconciler/workload/heartbeat"
	workloadnamespace "github.com/kcp-dev/kcp/pkg/reconciler/workload/namespace"
	workloadplacement "github.com/kcp-dev/kcp/pkg/reconciler/workload/placement"
	workloadresource "github.com/kcp-dev/kcp/pkg/reconciler/workload/resource"
	synctargetcontroller "github.com/kcp-dev/kcp/pkg/reconciler/workload/synctarget"
	"github.com/kcp-dev/kcp/pkg/reconciler/workload/synctargetexports"
)

const workerCount = 10

func postStartHookName(controllerName string) string {
	return fmt.Sprintf("kcp-tmc-start-%s", controllerName)
}

func (s *Server) installWorkloadResourceScheduler(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, workloadresource.ControllerName)
	dynamicClusterClient, err := kcpdynamic.NewForConfig(config)
	if err != nil {
		return err
	}

	resourceScheduler, err := workloadresource.NewController(
		dynamicClusterClient,
		s.Core.DiscoveringDynamicSharedInformerFactory,
		s.Core.KcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
		s.Core.KubeSharedInformerFactory.Core().V1().Namespaces(),
		s.Core.KcpSharedInformerFactory.Scheduling().V1alpha1().Placements(),
	)
	if err != nil {
		return err
	}

	return s.Core.AddPostStartHook(postStartHookName(workloadresource.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(workloadresource.ControllerName))
		if err := s.Core.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go resourceScheduler.Start(ctx, workerCount)
		return nil
	})
}

func (s *Server) installApiResourceController(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, apiresource.ControllerName)

	crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(config)
	if err != nil {
		return err
	}
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := apiresource.NewController(
		crdClusterClient,
		kcpClusterClient,
		s.Core.KcpSharedInformerFactory.Apiresource().V1alpha1().NegotiatedAPIResources(),
		s.Core.KcpSharedInformerFactory.Apiresource().V1alpha1().APIResourceImports(),
		s.Core.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
	)
	if err != nil {
		return err
	}

	return s.Core.AddPostStartHook(postStartHookName(apiresource.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(apiresource.ControllerName))
		if err := s.Core.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(ctx, s.Options.Controllers.ApiResource.NumThreads)

		return nil
	})
}

func (s *Server) installSyncTargetHeartbeatController(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, heartbeat.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := heartbeat.NewController(
		kcpClusterClient,
		s.Core.KcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
		s.Options.Controllers.SyncTargetHeartbeat.HeartbeatThreshold,
	)
	if err != nil {
		return err
	}

	return s.Core.AddPostStartHook(postStartHookName(heartbeat.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(heartbeat.ControllerName))
		if err := s.Core.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(ctx)

		return nil
	})
}

func (s *Server) installSchedulingLocationStatusController(ctx context.Context, config *rest.Config) error {
	controllerName := "kcp-scheduling-location-status-controller"
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, controllerName)

	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := schedulinglocationstatus.NewController(
		kcpClusterClient,
		s.Core.KcpSharedInformerFactory.Scheduling().V1alpha1().Locations(),
		s.Core.KcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
	)
	if err != nil {
		return err
	}

	return s.Core.AddPostStartHook(postStartHookName(controllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(controllerName))
		if err := s.Core.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), workerCount)

		return nil
	})
}

func (s *Server) installWorkloadNamespaceScheduler(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, workloadnamespace.ControllerName)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := workloadnamespace.NewController(
		kubeClusterClient,
		s.Core.KubeSharedInformerFactory.Core().V1().Namespaces(),
		s.Core.KcpSharedInformerFactory.Scheduling().V1alpha1().Placements(),
	)
	if err != nil {
		return err
	}

	if err := s.Core.AddPostStartHook(postStartHookName(workloadnamespace.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(workloadnamespace.ControllerName))
		if err := s.Core.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), workerCount)

		return nil
	}); err != nil {
		return err
	}

	return nil
}

func (s *Server) installWorkloadPlacementScheduler(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, workloadplacement.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := workloadplacement.NewController(
		kcpClusterClient,
		s.Core.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
		s.Core.KcpSharedInformerFactory.Scheduling().V1alpha1().Locations(),
		s.Core.KcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
		s.Core.KcpSharedInformerFactory.Scheduling().V1alpha1().Placements(),
		s.Core.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
	)
	if err != nil {
		return err
	}

	return s.Core.AddPostStartHook(postStartHookName(workloadplacement.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(workloadplacement.ControllerName))
		if err := s.Core.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), workerCount)

		return nil
	})
}

func (s *Server) installSchedulingPlacementController(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, schedulingplacement.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := schedulingplacement.NewController(
		kcpClusterClient,
		s.Core.KubeSharedInformerFactory.Core().V1().Namespaces(),
		s.Core.KcpSharedInformerFactory.Scheduling().V1alpha1().Locations(),
		s.Core.KcpSharedInformerFactory.Scheduling().V1alpha1().Placements(),
	)
	if err != nil {
		return err
	}

	return s.Core.AddPostStartHook(postStartHookName(schedulingplacement.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(schedulingplacement.ControllerName))
		if err := s.Core.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), workerCount)

		return nil
	})
}

func (s *Server) installWorkloadsAPIExportController(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, workloadsapiexport.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := workloadsapiexport.NewController(
		kcpClusterClient,
		s.Core.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.Core.KcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas(),
		s.Core.KcpSharedInformerFactory.Apiresource().V1alpha1().NegotiatedAPIResources(),
		s.Core.KcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
	)
	if err != nil {
		return err
	}

	return s.Core.AddPostStartHook(postStartHookName(workloadsapiexport.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(workloadsapiexport.ControllerName))
		if err := s.Core.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), workerCount)

		return nil
	})
}

func (s *Server) installWorkloadsAPIExportCreateController(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, workloadsapiexportcreate.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := workloadsapiexportcreate.NewController(
		kcpClusterClient,
		s.Core.KcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
		s.Core.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.Core.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
		s.Core.KcpSharedInformerFactory.Scheduling().V1alpha1().Locations(),
	)
	if err != nil {
		return err
	}

	return s.Core.AddPostStartHook(postStartHookName(workloadsapiexportcreate.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(workloadsapiexportcreate.ControllerName))
		if err := s.Core.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), workerCount)

		return nil
	})
}

func (s *Server) installWorkloadsSyncTargetExportController(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, synctargetexports.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := synctargetexports.NewController(
		kcpClusterClient,
		s.Core.KcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
		s.Core.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.Core.KcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas(),
		s.Core.KcpSharedInformerFactory.Apiresource().V1alpha1().APIResourceImports(),
	)
	if err != nil {
		return err
	}

	return s.Core.AddPostStartHook(synctargetexports.ControllerName, func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(workloadsapiexportcreate.ControllerName))
		if err := s.Core.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), workerCount)

		return nil
	})
}

func (s *Server) installSyncTargetController(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, synctargetcontroller.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c := synctargetcontroller.NewController(
		kcpClusterClient,
		s.Core.KcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
		// TODO: change to s.CacheKcpSharedInformerFactory.Core().V1alpha1().Shards(),
		// once https://github.com/kcp-dev/kcp/issues/2649 is resolved
		s.Core.KcpSharedInformerFactory.Core().V1alpha1().Shards(),
	)
	if err != nil {
		return err
	}

	return s.Core.AddPostStartHook(postStartHookName(synctargetcontroller.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(synctargetcontroller.ControllerName))
		if err := s.Core.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), workerCount)

		return nil
	})
}
