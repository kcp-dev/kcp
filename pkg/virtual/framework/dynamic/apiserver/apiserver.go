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

package apiserver

import (
	"errors"
	"net/http"
	"time"

	"github.com/emicklei/go-restful/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	genericapiserver "k8s.io/apiserver/pkg/server"

	virtualcontext "github.com/kcp-dev/kcp/pkg/virtual/framework/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
)

var (
	scheme = runtime.NewScheme()
	codecs = serializer.NewCodecFactory(scheme)

	// if you modify this, make sure you update the crEncoder.
	unversionedVersion = schema.GroupVersion{Group: "", Version: "v1"}
	unversionedTypes   = []runtime.Object{
		&metav1.Status{},
		&metav1.WatchEvent{},
		&metav1.APIVersions{},
		&metav1.APIGroupList{},
		&metav1.APIGroup{},
		&metav1.APIResourceList{},
	}
)

func init() {
	// we need to add the options to empty v1
	metav1.AddToGroupVersion(scheme, schema.GroupVersion{Group: "", Version: "v1"})

	scheme.AddUnversionedTypes(unversionedVersion, unversionedTypes...)
}

// DynamicAPIServerExtraConfig contains additional configuration for the DynamicAPIServer.
type DynamicAPIServerExtraConfig struct {
	APISetRetriever apidefinition.APIDefinitionSetGetter
}

// DynamicAPIServerConfig contains the configuration for the DynamicAPIServer.
type DynamicAPIServerConfig struct {
	GenericConfig *genericapiserver.RecommendedConfig
	ExtraConfig   DynamicAPIServerExtraConfig
}

// DynamicAPIServer contains state for a Kubernetes api server that can
// dynamically serve resources based on an API definitions (provided by the APISetRetriever).
type DynamicAPIServer struct {
	GenericAPIServer *genericapiserver.GenericAPIServer
	APISetRetriever  apidefinition.APIDefinitionSetGetter
}

type completedConfig struct {
	GenericConfig genericapiserver.CompletedConfig
	ExtraConfig   *DynamicAPIServerExtraConfig
}

type CompletedConfig struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	*completedConfig
}

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
func (c *DynamicAPIServerConfig) Complete() completedConfig {
	cfg := completedConfig{
		c.GenericConfig.Complete(),
		&c.ExtraConfig,
	}

	return cfg
}

// New returns a new instance of DynamicAPIServer from the given config.
func (c completedConfig) New(virtualWorkspaceName string, delegationTarget genericapiserver.DelegationTarget) (*DynamicAPIServer, error) {
	genericServer, err := c.GenericConfig.New(virtualWorkspaceName+"-virtual-workspace-apiserver", delegationTarget)
	if err != nil {
		return nil, err
	}

	director := genericServer.Handler.Director
	genericServer.Handler.Director = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		vwName, found := virtualcontext.VirtualWorkspaceNameFrom(r.Context())
		if !found {
			utilruntime.HandleError(errors.New("context should always contain a virtual workspace name when hitting a virtual workspace delegated APIServer"))
			http.NotFoundHandler().ServeHTTP(rw, r)
			return
		}
		if vwName == virtualWorkspaceName {
			director.ServeHTTP(rw, r)
			return
		}
		delegatedHandler := delegationTarget.UnprotectedHandler()
		if delegatedHandler != nil {
			delegatedHandler.ServeHTTP(rw, r)
		} else {
			http.NotFoundHandler().ServeHTTP(rw, r)
		}
	})

	s := &DynamicAPIServer{
		GenericAPIServer: genericServer,
		APISetRetriever:  c.ExtraConfig.APISetRetriever,
	}

	delegateHandler := delegationTarget.UnprotectedHandler()
	if delegateHandler == nil {
		delegateHandler = http.NotFoundHandler()
	}

	versionDiscoveryHandler := &versionDiscoveryHandler{
		apiSetRetriever: s.APISetRetriever,
		delegate:        delegateHandler,
	}

	groupDiscoveryHandler := &groupDiscoveryHandler{
		apiSetRetriever: s.APISetRetriever,
		delegate:        delegateHandler,
	}

	rootDiscoveryHandler := &rootDiscoveryHandler{
		apiSetRetriever: s.APISetRetriever,
		delegate:        delegateHandler,
	}

	crdHandler, err := newResourceHandler(
		s.APISetRetriever,
		versionDiscoveryHandler,
		groupDiscoveryHandler,
		rootDiscoveryHandler,
		delegateHandler,
		c.GenericConfig.AdmissionControl,
		s.GenericAPIServer.Authorizer,
		c.GenericConfig.RequestTimeout,
		time.Duration(c.GenericConfig.MinRequestTimeout)*time.Second,
		c.GenericConfig.MaxRequestBodyBytes,
		s.GenericAPIServer.StaticOpenAPISpec,
	)
	if err != nil {
		return nil, err
	}

	s.GenericAPIServer.Handler.GoRestfulContainer.Filter(func(req *restful.Request, resp *restful.Response, chain *restful.FilterChain) {
		pathParts := splitPath(req.Request.URL.Path)
		if len(pathParts) > 0 && pathParts[0] == "apis" ||
			len(pathParts) > 1 && pathParts[0] == "api" {
			crdHandler.ServeHTTP(resp.ResponseWriter, req.Request)
		} else {
			chain.ProcessFilter(req, resp)
		}
	})

	s.GenericAPIServer.Handler.GoRestfulContainer.Add(discovery.NewLegacyRootAPIHandler(c.GenericConfig.DiscoveryAddresses, s.GenericAPIServer.Serializer, "/api").WebService())

	s.GenericAPIServer.Handler.NonGoRestfulMux.Handle("/apis", crdHandler)
	s.GenericAPIServer.Handler.NonGoRestfulMux.HandlePrefix("/apis/", crdHandler)
	s.GenericAPIServer.Handler.NonGoRestfulMux.Handle("/api/v1", crdHandler)
	s.GenericAPIServer.Handler.NonGoRestfulMux.HandlePrefix("/api/v1/", crdHandler)

	// TODO(david): plug OpenAPI if necessary. For now, according to the various virtual workspace use-cases,
	// it doesn't seem necessary.
	// Of course this requires using the --validate=false argument with some kubectl command like kubectl apply.

	return s, nil
}
