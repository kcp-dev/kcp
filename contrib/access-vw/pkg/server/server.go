/*
Copyright 2026 The kcp Authors.

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

// Package server wires the Access Virtual Workspace binary together:
// the shared access graph, the RBAC provider that populates it, and a
// virtual-workspace root apiserver (kcp virtual-workspace-framework)
// serving the access virtual workspace at /services/access behind
// kcp's front-proxy.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/authorization/union"
	openapinamer "k8s.io/apiserver/pkg/endpoints/openapi"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	netutils "k8s.io/utils/net"

	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	accessv1alpha1 "github.com/kcp-dev/kcp/contrib/access-vw/pkg/apis/access/v1alpha1"
	generatedopenapi "github.com/kcp-dev/kcp/contrib/access-vw/pkg/generated/openapi"
	"github.com/kcp-dev/kcp/contrib/access-vw/pkg/graph"
	"github.com/kcp-dev/kcp/contrib/access-vw/pkg/rbacprovider"
	"github.com/kcp-dev/kcp/contrib/access-vw/pkg/virtual"
	"github.com/kcp-dev/kcp/contrib/access-vw/pkg/virtual/scar"
)

const debugGraphPath = "/debug/graph"

// Run starts the provider and serves the Access virtual workspace until
// ctx is cancelled.
func Run(ctx context.Context, o *Options) error {
	if err := o.Complete(); err != nil {
		return err
	}
	if err := o.Validate(); err != nil {
		return err
	}

	restConfig, err := clientcmd.BuildConfigFromFlags("", o.Kubeconfig)
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	g := graph.New()

	provider := rbacprovider.New(o.EndpointBase)
	provider.RestConfig = restConfig
	provider.APIExportEndpointSlice = o.APIExportEndpointSlice

	providerErr := make(chan error, 1)
	go func() {
		providerErr <- provider.Start(ctx, g)
	}()

	mode := "single-shard"
	if o.APIExportEndpointSlice != "" {
		mode = "multi-shard (apiexport=" + o.APIExportEndpointSlice + ")"
	}
	klog.InfoS("rbacprovider running", "mode", mode, "kubeconfig", o.Kubeconfig)

	vws := []rootapiserver.NamedVirtualWorkspace{
		scar.NewVirtualWorkspace(g),
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(accessv1alpha1.AddToScheme(scheme))
	codecs := serializer.NewCodecFactory(scheme)

	recommended := genericapiserver.NewRecommendedConfig(codecs)

	namer := openapinamer.NewDefinitionNamer(scheme)
	recommended.OpenAPIConfig = genericapiserver.DefaultOpenAPIConfig(generatedopenapi.GetOpenAPIDefinitions, namer)
	recommended.OpenAPIConfig.Info.Title = "kcp-access-vw"
	recommended.OpenAPIV3Config = genericapiserver.DefaultOpenAPIV3Config(generatedopenapi.GetOpenAPIDefinitions, namer)
	recommended.OpenAPIV3Config.Info.Title = "kcp-access-vw"

	rootCfg, err := rootapiserver.NewConfig(recommended)
	if err != nil {
		return fmt.Errorf("create root apiserver config: %w", err)
	}
	rootCfg.Extra.VirtualWorkspaces = vws

	if err := o.SecureServing.MaybeDefaultWithSelfSignedCerts("localhost", nil, []net.IP{netutils.ParseIPSloppy("127.0.0.1")}); err != nil {
		return fmt.Errorf("default serving certs: %w", err)
	}
	if err := o.SecureServing.ApplyTo(&recommended.Config.SecureServing); err != nil {
		return fmt.Errorf("apply secure serving: %w", err)
	}
	if err := o.Authentication.ApplyTo(&recommended.Config.Authentication, recommended.Config.SecureServing, nil); err != nil {
		return fmt.Errorf("apply authentication: %w", err)
	}
	if err := o.Authorization.ApplyTo(&recommended.Config, func() []rootapiserver.NamedVirtualWorkspace { return vws }); err != nil {
		return fmt.Errorf("apply authorization: %w", err)
	}

	// /debug/graph sits outside the VW authorizers (it is not under a
	// /services/ prefix), so grant it to authenticated users explicitly.
	recommended.Config.Authorization.Authorizer = union.New(
		recommended.Config.Authorization.Authorizer,
		pathScopedAuthorizer(debugGraphPath, virtual.AuthenticatedOnlyAuthorizer()),
	)

	completed := rootCfg.Complete()
	rootServer, err := rootapiserver.NewServer(completed, genericapiserver.NewEmptyDelegate())
	if err != nil {
		return fmt.Errorf("create root apiserver: %w", err)
	}

	// Debug endpoint: pretty-printed snapshot of the access graph.
	rootServer.GenericAPIServer.Handler.NonGoRestfulMux.HandleFunc(debugGraphPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(g.Snapshot()); err != nil {
			klog.ErrorS(err, "encoding access graph snapshot")
		}
	})

	prepared := rootServer.GenericAPIServer.PrepareRun()

	klog.InfoS("access-vw serving",
		"scar", scar.RootPath+"/apis/"+accessv1alpha1.SchemeGroupVersion.Group+"/"+accessv1alpha1.SchemeGroupVersion.Version+"/selfclusteraccessreviews",
	)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- prepared.RunWithContext(ctx)
	}()

	select {
	case err := <-providerErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("rbac provider failed: %w", err)
		}
		<-serveErr
		return nil
	case err := <-serveErr:
		return err
	}
}

func pathScopedAuthorizer(path string, delegate authorizer.Authorizer) authorizer.Authorizer {
	return authorizer.AuthorizerFunc(func(ctx context.Context, attrs authorizer.Attributes) (authorizer.Decision, string, error) {
		if !attrs.IsResourceRequest() && attrs.GetPath() == path {
			return delegate.Authorize(ctx, attrs)
		}
		return authorizer.DecisionNoOpinion, "", nil
	})
}
