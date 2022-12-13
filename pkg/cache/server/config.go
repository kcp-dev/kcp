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

package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	apiextensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	kcpapiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/kcp/clientset/versioned"
	kcpapiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/kcp/informers/externalversions"
	apiextensionsoptions "k8s.io/apiextensions-apiserver/pkg/cmd/server/options"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/features"
	genericapiserver "k8s.io/apiserver/pkg/server"
	genericoptions "k8s.io/apiserver/pkg/server/options"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/apiserver/pkg/util/webhook"
	"k8s.io/client-go/rest"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	cacheserveroptions "github.com/kcp-dev/kcp/pkg/cache/server/options"
	"github.com/kcp-dev/kcp/pkg/embeddedetcd"
	"github.com/kcp-dev/kcp/pkg/server/filters"
)

const resyncPeriod = 10 * time.Hour

type Config struct {
	Options       *cacheserveroptions.CompletedOptions
	ApiExtensions *apiextensionsapiserver.Config
	EmbeddedEtcd  *embeddedetcd.Config

	ExtraConfig
}

type completedConfig struct {
	Options       *cacheserveroptions.CompletedOptions
	ApiExtensions apiextensionsapiserver.CompletedConfig
	EmbeddedEtcd  embeddedetcd.CompletedConfig

	ExtraConfig
}

type ExtraConfig struct {
	ApiExtensionsClusterClient kcpapiextensionsclientset.ClusterInterface

	ApiExtensionsSharedInformerFactory kcpapiextensionsinformers.SharedInformerFactory
}

type CompletedConfig struct {
	// embed a private pointer that cannot be instantiated outside this package.
	*completedConfig
}

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
func (c *Config) Complete() (CompletedConfig, error) {
	return CompletedConfig{&completedConfig{
		Options:       c.Options,
		ApiExtensions: c.ApiExtensions.Complete(),
		EmbeddedEtcd:  c.EmbeddedEtcd.Complete(),
		ExtraConfig:   c.ExtraConfig,
	}}, nil
}

// NewConfig returns a new Config for the given options and optional rest.Config that point to the local server.
// Pass it only when you combine this server with a different one.
func NewConfig(opts *cacheserveroptions.CompletedOptions, optionalLocalShardRestConfig *rest.Config) (*Config, error) {
	c := &Config{
		Options: opts,
	}
	if opts.EmbeddedEtcd.Enabled {
		var err error
		c.EmbeddedEtcd, err = embeddedetcd.NewConfig(opts.EmbeddedEtcd, opts.Etcd.EnableWatchCache)
		if err != nil {
			return nil, err
		}
	}
	// change the storage prefix under which all resources are kept
	// this allows us to store the same GR under a different
	// prefix than the kcp server. It is useful when this server
	// shares the database with a kcp instance.
	//
	// It boils down to the following on the storage level:
	// for listing across shard:  /cache/<group>/<resource>:<identity>/*
	// for listing for one shard: /cache/<group>/<resource>:<identity>/<shard>/*
	opts.Etcd.StorageConfig.Prefix = "/cache"

	serverConfig := genericapiserver.NewRecommendedConfig(apiextensionsapiserver.Codecs)

	// disable SSA since the cache server should not change objects in any way
	// note, that this will break when (if) the feature is locked, until then we should be fine
	if err := utilfeature.DefaultMutableFeatureGate.Set(fmt.Sprintf("%s=false", features.ServerSideApply)); err != nil {
		return nil, err
	}
	if err := opts.ServerRunOptions.ApplyTo(&serverConfig.Config); err != nil {
		return nil, err
	}
	if err := opts.Etcd.ApplyTo(&serverConfig.Config); err != nil {
		return nil, err
	}
	if optionalLocalShardRestConfig == nil {
		if err := opts.SecureServing.ApplyTo(&serverConfig.Config.SecureServing, &serverConfig.Config.LoopbackClientConfig); err != nil {
			return nil, err
		}
	} else {
		if err := opts.SecureServing.ApplyTo(&serverConfig.Config.SecureServing, nil); err != nil {
			return nil, err
		}
		serverConfig.LoopbackClientConfig = rest.CopyConfig(optionalLocalShardRestConfig)
	}
	if err := opts.Authentication.ApplyTo(&serverConfig.Config.Authentication, serverConfig.SecureServing, serverConfig.OpenAPIConfig); err != nil {
		return nil, err
	}
	if err := opts.Authorization.ApplyTo(&serverConfig.Config.Authorization); err != nil {
		return nil, err
	}

	if err := opts.APIEnablement.ApplyTo(&serverConfig.Config, apiextensionsapiserver.DefaultAPIResourceConfigSource(), apiextensionsapiserver.Scheme); err != nil {
		return nil, err
	}

	serverConfig.Config.BuildHandlerChainFunc = func(apiHandler http.Handler, genericConfig *genericapiserver.Config) (secure http.Handler) {
		apiHandler = genericapiserver.DefaultBuildHandlerChainFromAuthz(apiHandler, genericConfig)
		apiHandler = genericapiserver.DefaultBuildHandlerChainBeforeAuthz(apiHandler, genericConfig)
		apiHandler = filters.WithAuditEventClusterAnnotation(apiHandler)
		apiHandler = filters.WithClusterScope(apiHandler)
		apiHandler = filters.WithClusterScopedStaticEndpoints(apiHandler)
		apiHandler = WithShardScope(apiHandler)
		apiHandler = WithServiceScope(apiHandler)
		return apiHandler
	}

	opts.Etcd.StorageConfig.Paging = utilfeature.DefaultFeatureGate.Enabled(features.APIListChunking)
	// this is where the true decodable levels come from.
	opts.Etcd.StorageConfig.Codec = apiextensionsapiserver.Codecs.LegacyCodec(apiextensionsv1beta1.SchemeGroupVersion, apiextensionsv1.SchemeGroupVersion)
	// prefer the more compact serialization (v1beta1) for storage until http://issue.k8s.io/82292 is resolved for objects whose v1 serialization is too big but whose v1beta1 serialization can be stored
	opts.Etcd.StorageConfig.EncodeVersioner = runtime.NewMultiGroupVersioner(apiextensionsv1beta1.SchemeGroupVersion, schema.GroupKind{Group: apiextensionsv1beta1.GroupName})
	serverConfig.RESTOptionsGetter = &genericoptions.SimpleRestOptionsFactory{Options: *opts.Etcd}

	// an ordered list of HTTP round trippers that add
	// shard and cluster awareness to all clients that use
	// the loopback config.
	rt := cacheclient.WithCacheServiceRoundTripper(serverConfig.LoopbackClientConfig)
	rt = cacheclient.WithShardNameFromContextRoundTripper(rt)
	rt = cacheclient.WithDefaultShardRoundTripper(rt, shard.Wildcard)
	rt = cacheclient.WithShardNameFromObjectRoundTripper(
		rt,
		func(rq *http.Request) (string, string, error) {
			if serverConfig.Config.RequestInfoResolver == nil {
				return "", "", fmt.Errorf("RequestInfoResolver wasn't provided")
			}
			// the k8s request info resolver expects a cluster-less path, but the client we're using knows how to
			// add the cluster we are targeting to the path before this round-tripper fires, so we need to strip it
			// to use the k8s library
			parts := strings.Split(rq.URL.Path, "/")
			if len(parts) < 4 {
				return "", "", fmt.Errorf("RequestInfoResolver: got invalid path: %v", rq.URL.Path)
			}
			if parts[1] != "clusters" {
				return "", "", fmt.Errorf("RequestInfoResolver: got path without cluster prefix: %v", rq.URL.Path)
			}
			// we clone the request here to safely mutate the URL path, but this cloned request is never realized
			// into anything on the network, just inspected by the k8s request info libraries
			clone := rq.Clone(rq.Context())
			clone.URL.Path = strings.Join(parts[3:], "/")
			requestInfo, err := serverConfig.Config.RequestInfoResolver.NewRequestInfo(clone)
			if err != nil {
				return "", "", err
			}
			return requestInfo.Resource, requestInfo.Verb, nil
		},
		"customresourcedefinitions")
	rt = rest.AddUserAgent(rt, "kcp-cache-server")

	var err error
	c.ApiExtensionsClusterClient, err = kcpapiextensionsclientset.NewForConfig(rt)
	if err != nil {
		return nil, err
	}

	c.ApiExtensionsSharedInformerFactory = kcpapiextensionsinformers.NewSharedInformerFactoryWithOptions(
		c.ApiExtensionsClusterClient,
		resyncPeriod,
	)

	c.ApiExtensions = &apiextensionsapiserver.Config{
		GenericConfig: serverConfig,
		ExtraConfig: apiextensionsapiserver.ExtraConfig{
			CRDRESTOptionsGetter: apiextensionsoptions.NewCRDRESTOptionsGetter(*opts.Etcd),
			// Wire in a ServiceResolver that always returns an error that ResolveEndpoint is not yet
			// supported. The effect is that CRD webhook conversions are not supported and will always get an
			// error.
			ServiceResolver:       &unimplementedServiceResolver{},
			MasterCount:           1,
			AuthResolverWrapper:   webhook.NewDefaultAuthenticationInfoResolverWrapper(nil, nil, rt, nil),
			Client:                c.ApiExtensionsClusterClient,
			Informers:             c.ApiExtensionsSharedInformerFactory,
			ClusterAwareCRDLister: &crdClusterLister{lister: c.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Lister()},
		},
	}

	return c, nil
}

// unimplementedServiceResolver is a webhook.ServiceResolver that always returns an error, because
// we have not implemented support for this yet. As a result, CRD webhook conversions are not
// supported.
type unimplementedServiceResolver struct{}

// ResolveEndpoint always returns an error that this is not yet supported.
func (r *unimplementedServiceResolver) ResolveEndpoint(namespace string, name string, port int32) (*url.URL, error) {
	return nil, errors.New("CRD webhook conversions are not supported")
}
