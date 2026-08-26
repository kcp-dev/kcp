/*
Copyright 2022 The kcp Authors.

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
	"time"

	apiextensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/server/bootstrap"
)

type Server struct {
	CompletedConfig

	apiextensions *apiextensionsapiserver.CustomResourceDefinitions
}

func NewServer(c CompletedConfig) (*Server, error) {
	s := &Server{
		CompletedConfig: c,
	}

	var err error
	s.apiextensions, err = s.ApiExtensions.New(genericapiserver.NewEmptyDelegate())
	if err != nil {
		return nil, err
	}
	return s, nil
}

// preparedGenericAPIServer is a private wrapper that enforces a call of PrepareRun() before Run can be invoked.
type preparedServer struct {
	*Server
	Handler *genericapiserver.APIServerHandler
}

func (s *Server) PrepareRun(ctx context.Context) (preparedServer, error) {
	logger := klog.FromContext(ctx).WithValues("component", "cache-server")
	if err := s.apiextensions.GenericAPIServer.AddPostStartHook("bootstrap-cache-server", func(hookContext genericapiserver.PostStartHookContext) error {
		logger := logger.WithValues("postStartHook", "bootstrap-cache-server")
		if err := bootstrap.Bootstrap(klog.NewContext(hookContext, logger), s.ApiExtensionsClusterClient); err != nil {
			logger.Error(err, "failed creating the static CustomResourcesDefinitions")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		return nil
	}); err != nil {
		return preparedServer{}, err
	}

	if err := s.apiextensions.GenericAPIServer.AddPostStartHook("cache-server-start-informers", func(hookContext genericapiserver.PostStartHookContext) error {
		logger := logger.WithValues("postStartHook", "cache-server-start-informers")
		s.ApiExtensionsSharedInformerFactory.Start(hookContext.Done())
		s.KcpSharedInformerFactory.Start(hookContext.Done())

		go s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().Run(hookContext.Done())
		go s.KcpSharedInformerFactory.Cache().V1alpha1().ClusterCachedResources().Informer().Run(hookContext.Done())
		go s.KcpSharedInformerFactory.Core().V1alpha1().Caches().Informer().Run(hookContext.Done())

		logger.Info("starting CRD and ClusterCachedResource informers")
		if err := wait.PollUntilContextCancel(hookContext, time.Millisecond*100, true, func(ctx context.Context) (bool, error) {
			crdsSynced := s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().HasSynced()
			clusterCachedResourcesSynced := s.KcpSharedInformerFactory.Cache().V1alpha1().ClusterCachedResources().Informer().HasSynced()
			cachesSynced := s.KcpSharedInformerFactory.Core().V1alpha1().Caches().Informer().HasSynced()
			return crdsSynced && clusterCachedResourcesSynced && cachesSynced, nil
		}); err != nil {
			logger.Error(err, "failed to start some of CRD and ClusterCachedResource informers")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		logger.Info("finished starting CRD and ClusterCachedResource informers")

		cache := &corev1alpha1.Cache{
			ObjectMeta: metav1.ObjectMeta{
				Name: s.Options.Extra.CacheName,
				Labels: map[string]string{
					"name": s.Options.Extra.CacheName,
				},
			},
			Spec: corev1alpha1.CacheSpec{},
		}
		logger.Info("Creating or updating Cache", "cache", s.Options.Extra.CacheName)
		if err := wait.PollUntilContextCancel(hookContext, time.Second, true, func(ctx context.Context) (bool, error) {
			ctx = cacheclient.WithShardInContext(ctx, bootstrap.SystemCacheServerShard)
			createOrUpdateCache := func(cl kcpclientset.ClusterInterface, cluster logicalcluster.Path) error {
				existingCache, err := cl.Cluster(cluster).CoreV1alpha1().Caches().Get(ctx, cache.Name, metav1.GetOptions{})
				if err != nil && !apierrors.IsNotFound(err) {
					logger.Error(err, "failed getting Cache", "cluster", cluster)
					return err
				} else if apierrors.IsNotFound(err) {
					if _, err := cl.Cluster(cluster).CoreV1alpha1().Caches().Create(ctx, cache, metav1.CreateOptions{}); err != nil {
						logger.Error(err, "failed creating Shard", "cluster", cluster)
						return err
					}
					createdShard, _ := cl.Cluster(cluster).CoreV1alpha1().Shards().Get(ctx, cache.Name, metav1.GetOptions{})
					logger.Info("Created Shard", "shard", s.Options.Extra.CacheName, "cluster", cluster, "shard", createdShard)
					return nil
				}
				if _, err := cl.Cluster(cluster).CoreV1alpha1().Caches().Update(ctx, existingCache, metav1.UpdateOptions{}); err != nil {
					logger.Error(err, "failed updating Shard", "cluster", cluster)
					return err
				}
				logger.Info("Updated Cache", "cache", s.Options.Extra.CacheName, "cluster", cluster)
				return nil
			}
			err := createOrUpdateCache(s.KcpClusterClient, SystemCacheCluster.Path())
			if err != nil {
				return false, err
			}
			return true, nil
		}); err != nil {
			logger.Error(err, "failed reconciling Cache resource", "cluster", SystemCacheCluster)
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		select {
		case <-hookContext.Done():
			return nil // context closed, avoid reporting success below
		default:
		}

		logger.Info("finished starting kube informers")
		return nil
	}); err != nil {
		return preparedServer{}, err
	}
	return preparedServer{s, s.apiextensions.GenericAPIServer.Handler}, nil
}

func (s preparedServer) Run(ctx context.Context) error {
	return s.apiextensions.GenericAPIServer.PrepareRun().RunWithContext(ctx)
}

func (s preparedServer) RunPostStartHooks(ctx context.Context) {
	s.apiextensions.GenericAPIServer.RunPostStartHooks(ctx)
}
