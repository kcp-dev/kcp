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

package envoycontrolplane

import (
	"context"
	"fmt"
	"net"

	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	cluster "github.com/envoyproxy/go-control-plane/envoy/service/cluster/v3"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	listener "github.com/envoyproxy/go-control-plane/envoy/service/listener/v3"
	route "github.com/envoyproxy/go-control-plane/envoy/service/route/v3"
	cachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	envoycachev3 "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	xds "github.com/envoyproxy/go-control-plane/pkg/server/v3"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	health "google.golang.org/grpc/health/grpc_health_v1"

	"k8s.io/apimachinery/pkg/labels"
	v1 "k8s.io/client-go/listers/networking/v1"
	"k8s.io/klog/v2"
)

const (
	grpcMaxConcurrentStreams = 1000000

	NodeID       = "kcp-ingress"
	toEnvoyLabel = "ingress-controller/envoy"
)

func init() {
	var err error

	// Create the selector
	envoyReadySelector, err = labels.Parse(toEnvoyLabel + "=" + "true")
	if err != nil {
		klog.Fatalf("failed to parse selector: %v", err)
	}
}

var envoyReadySelector labels.Selector

// EnvoyControlPlane is an envoy control plane that handles configuration update
// and the management of the xDS server.
type EnvoyControlPlane struct {
	xdsServer      *xds.Server
	ingressLister  v1.IngressLister
	translator     *translator
	managementPort uint
	snapshotCache  cache.SnapshotCache
	callbacks      xds.Callbacks
}

// NewEnvoyControlPlane creates a new EnvoyControlPlane instance.
func NewEnvoyControlPlane(managementPort, envoyListenPort uint, ingressLister v1.IngressLister, callbacks xds.Callbacks) *EnvoyControlPlane {
	snapshotCache := cache.NewSnapshotCache(true, cache.IDHash{}, nil)

	ecp := EnvoyControlPlane{
		managementPort: managementPort,
		ingressLister:  ingressLister,
		translator:     newTranslator(envoyListenPort),
		snapshotCache:  snapshotCache,
		callbacks:      callbacks,
	}

	return &ecp
}

// Start starts the envoy XDS server
func (ecp *EnvoyControlPlane) Start(ctx context.Context) error {

	server := xds.NewServer(ctx, ecp.snapshotCache, ecp.callbacks)
	ecp.xdsServer = &server

	grpcServer := grpc.NewServer(grpc.MaxConcurrentStreams(grpcMaxConcurrentStreams))
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", ecp.managementPort))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	// register services
	discovery.RegisterAggregatedDiscoveryServiceServer(grpcServer, *ecp.xdsServer)
	health.RegisterHealthServer(grpcServer, healthServer{})
	cluster.RegisterClusterDiscoveryServiceServer(grpcServer, *ecp.xdsServer)
	listener.RegisterListenerDiscoveryServiceServer(grpcServer, *ecp.xdsServer)
	route.RegisterRouteDiscoveryServiceServer(grpcServer, *ecp.xdsServer)

	errCh := make(chan error)
	go func() {
		defer close(errCh)
		if err = grpcServer.Serve(lis); err != nil {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		grpcServer.GracefulStop()
		return nil
	case err := <-errCh:
		return fmt.Errorf("failed to serve: %w", err)
	}
}

// UpdateEnvoyConfig creates a new envoy config snapshot and updates the xDS server
// using the information from the ingresses that are labeled with the toEnvoyLabel.
func (ecp *EnvoyControlPlane) UpdateEnvoyConfig(ctx context.Context) error {
	clustersResources := make([]cachetypes.Resource, 0)
	virtualhosts := make([]*envoyroutev3.VirtualHost, 0)

	ingresses, err := ecp.ingressLister.List(envoyReadySelector)
	if err != nil {
		return err
	}

	for _, ingress := range ingresses {
		ingclusters, ingvhosts := ecp.translator.translateIngress(ingress)
		clustersResources = append(clustersResources, ingclusters...)
		virtualhosts = append(virtualhosts, ingvhosts...)
	}

	routeConfig := ecp.translator.newRouteConfig("defaultroute", virtualhosts)
	hcm := ecp.translator.newHTTPConnectionManager(routeConfig.Name)
	listener, _ := ecp.translator.newHTTPListener(hcm)

	res := make(map[resource.Type][]cachetypes.Resource, 0)

	res[resource.RouteType] = []cachetypes.Resource{routeConfig}
	res[resource.ListenerType] = []cachetypes.Resource{listener}
	res[resource.ClusterType] = clustersResources

	newSnapshot, err := envoycachev3.NewSnapshot(
		uuid.New().String(),
		res,
	)
	if err != nil {
		klog.Errorf("failed to create snapshot: %v", err)
		return err
	}

	return ecp.snapshotCache.SetSnapshot(ctx, NodeID, newSnapshot)
}

type healthServer struct {
	health.UnimplementedHealthServer
}
