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

package controlplane

import (
	"fmt"
	"strconv"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoyfilterhcmv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	cachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/klog/v2"
)

// translator takes care of translating the ingress objects into Envoy resources.
type translator struct {
	envoyListenPort uint
}

// newTranslator returns a new translator.
func newTranslator(envoyListenPort uint) *translator {
	return &translator{
		envoyListenPort: envoyListenPort,
	}
}

// translateIngress has a "simple" implementation of a networkingv1.Ingress parser to translation to Envoy resources.
// It traverses the ingress spec object and creates a list of Envoy resources.
func (t *translator) translateIngress(ingress *networkingv1.Ingress) ([]cachetypes.Resource, []*envoyroutev3.VirtualHost) {
	// TODO(jmprusi): Hardcoded port, also, not TLS support. Review
	endpoints := make([]*envoyendpointv3.LbEndpoint, 0)
	for _, lb := range ingress.Status.LoadBalancer.Ingress {
		endpoint := &envoyendpointv3.LbEndpoint{}
		if lb.Hostname != "" {
			endpoint = t.newLBEndpoint(lb.Hostname, 80)
		} else if lb.IP != "" {
			endpoint = t.newLBEndpoint(lb.IP, 80)
		}
		endpoints = append(endpoints, endpoint)
	}

	// TODO(jmprusi): HTTP2 is set to false always, also allow for configuration of the timeout
	ingressKey, err := kcpcache.MetaClusterNamespaceKeyFunc(ingress)
	if err != nil {
		klog.Errorf("Error getting key for ingress %s: %v", ingress.Name, err)
		return nil, nil
	}
	cluster := t.newCluster(ingressKey, 2*time.Second, endpoints, envoyclusterv3.Cluster_STRICT_DNS)
	cluster.DnsLookupFamily = envoyclusterv3.Cluster_V4_ONLY

	virtualHosts := make([]*envoyroutev3.VirtualHost, 0)
	routes := make([]*envoyroutev3.Route, 0)
	domains := make([]string, 0)

	// TODO(jmprusi): We are ignoring the path type, we need to review this.
	for i, rule := range ingress.Spec.Rules {
		// TODO(jmprusi): If the host is empty we just ignore the rule, not ideal.
		if rule.HTTP.Paths == nil || rule.Host == "" {
			break
		}

		for _, path := range rule.HTTP.Paths {
			route := &envoyroutev3.Route{
				Name: ingressKey + strconv.Itoa(i),
				Match: &envoyroutev3.RouteMatch{
					PathSpecifier: &envoyroutev3.RouteMatch_Prefix{
						Prefix: path.Path,
					},
				},
				Action: &envoyroutev3.Route_Route{
					Route: &envoyroutev3.RouteAction{
						ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{
							Cluster: ingressKey,
						},
						Timeout: &durationpb.Duration{Seconds: 0},
						UpgradeConfigs: []*envoyroutev3.RouteAction_UpgradeConfig{{
							UpgradeType: "websocket",
							Enabled:     wrapperspb.Bool(true),
						}},
					},
				},
			}
			routes = append(routes, route)
		}
		domains = append(domains, rule.Host, rule.Host+":*")
	}

	vh := &envoyroutev3.VirtualHost{
		Name:    ingressKey,
		Domains: domains,
		Routes:  routes,
	}

	virtualHosts = append(virtualHosts, vh)

	return []cachetypes.Resource{cluster}, virtualHosts
}

func (t *translator) newLBEndpoint(ip string, port uint32) *envoyendpointv3.LbEndpoint {
	return &envoyendpointv3.LbEndpoint{
		HostIdentifier: &envoyendpointv3.LbEndpoint_Endpoint{
			Endpoint: &envoyendpointv3.Endpoint{
				Address: &envoycorev3.Address{
					Address: &envoycorev3.Address_SocketAddress{
						SocketAddress: &envoycorev3.SocketAddress{
							Protocol: envoycorev3.SocketAddress_TCP,
							Address:  ip,
							PortSpecifier: &envoycorev3.SocketAddress_PortValue{
								PortValue: port,
							},
							Ipv4Compat: true,
						},
					},
				},
			},
		},
	}
}

func (t *translator) newCluster(
	name string,
	connectTimeout time.Duration,
	endpoints []*envoyendpointv3.LbEndpoint,
	discoveryType envoyclusterv3.Cluster_DiscoveryType) *envoyclusterv3.Cluster {

	return &envoyclusterv3.Cluster{
		Name: name,
		ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{
			Type: discoveryType,
		},
		ConnectTimeout: durationpb.New(connectTimeout),
		LoadAssignment: &envoyendpointv3.ClusterLoadAssignment{
			ClusterName: name,
			Endpoints: []*envoyendpointv3.LocalityLbEndpoints{{
				LbEndpoints: endpoints,
			}},
		},
	}
}

func (t *translator) newRouteConfig(name string, virtualHosts []*envoyroutev3.VirtualHost) *envoyroutev3.RouteConfiguration {
	return &envoyroutev3.RouteConfiguration{
		Name:             name,
		VirtualHosts:     virtualHosts,
		ValidateClusters: wrapperspb.Bool(true),
	}
}

func (t *translator) newHTTPConnectionManager(routeConfigName string) *envoyfilterhcmv3.HttpConnectionManager {
	filters := make([]*envoyfilterhcmv3.HttpFilter, 0, 1)

	// Append the Router filter at the end.
	filters = append(filters, &envoyfilterhcmv3.HttpFilter{
		Name: wellknown.Router,
	})

	return &envoyfilterhcmv3.HttpConnectionManager{
		CodecType:   envoyfilterhcmv3.HttpConnectionManager_AUTO,
		StatPrefix:  "ingress_http",
		HttpFilters: filters,
		RouteSpecifier: &envoyfilterhcmv3.HttpConnectionManager_Rds{
			Rds: &envoyfilterhcmv3.Rds{
				ConfigSource: &envoycorev3.ConfigSource{
					ResourceApiVersion: resource.DefaultAPIVersion,
					ConfigSourceSpecifier: &envoycorev3.ConfigSource_Ads{
						Ads: &envoycorev3.AggregatedConfigSource{},
					},
					InitialFetchTimeout: durationpb.New(10 * time.Second),
				},
				RouteConfigName: routeConfigName,
			},
		},
	}
}

func (t *translator) newHTTPListener(manager *envoyfilterhcmv3.HttpConnectionManager) (*envoylistenerv3.Listener, error) {
	managerAny, err := anypb.New(manager)
	if err != nil {
		return nil, err
	}

	filters := []*envoylistenerv3.Filter{{
		Name:       wellknown.HTTPConnectionManager,
		ConfigType: &envoylistenerv3.Filter_TypedConfig{TypedConfig: managerAny},
	}}

	return &envoylistenerv3.Listener{
		Name: fmt.Sprintf("listener_%d", t.envoyListenPort),
		Address: &envoycorev3.Address{
			Address: &envoycorev3.Address_SocketAddress{
				SocketAddress: &envoycorev3.SocketAddress{
					Protocol: envoycorev3.SocketAddress_TCP,
					Address:  "0.0.0.0",
					PortSpecifier: &envoycorev3.SocketAddress_PortValue{
						PortValue: uint32(t.envoyListenPort),
					},
				},
			},
		},
		FilterChains: []*envoylistenerv3.FilterChain{
			{Filters: filters},
		},
	}, nil
}
