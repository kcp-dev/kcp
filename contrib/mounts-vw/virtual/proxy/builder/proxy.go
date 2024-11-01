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

package builder

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/handler"
	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	targetsv1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-vw/apis/targets/v1alpha1"
	mountsinformers "github.com/kcp-dev/kcp/contrib/mounts-vw/client/informers/externalversions"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/state"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/utils/portforward"
)

type clusterProxyProvider struct {
	kubeClusterClient    kcpkubernetesclientset.ClusterInterface
	dynamicClusterClient kcpdynamic.ClusterInterface
	cachedProxyInformers mountsinformers.SharedInformerFactory
	rootPathPrefix       string
	state                state.ClientSetStoreInterface
}

type clusterParameters struct {
	virtualWorkspaceName string
}

func (c *clusterProxyProvider) newTemplate(clusterParameters clusterParameters, store state.ClientSetStoreInterface) *clusterProxy {
	return &clusterProxy{
		clusterProxyProvider: *c,
		clusterParameters:    clusterParameters,
		readyClusterCh:       make(chan struct{}),
		store:                store,
	}
}

type controlMsg struct {
	Command  string `json:"command,omitempty"`  // "keep-alive", "conn-ready", "pickup-failed"
	ConnPath string `json:"connPath,omitempty"` // conn pick-up URL path for "conn-url", "pickup-failed"
	Err      string `json:"err,omitempty"`
}

type key struct {
	clusterName logicalcluster.Name
	proxyName   string
}

type clusterProxy struct {
	clusterProxyProvider

	clusterParameters

	readyClusterCh chan struct{}

	// Proxy dialer pool
	mu    sync.Mutex
	store state.ClientSetStoreInterface
}

func (p *clusterProxy) readyCluster() error {
	select {
	case <-p.readyClusterCh:
		return nil
	default:
		return errors.New("proxy virtual workspace controllers are not started")
	}
}

func (p *clusterProxy) buildClusterProxyVirtualWorkspace() *handler.VirtualWorkspace {
	defer close(p.readyClusterCh)
	return &handler.VirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(p.resolveClusterProxyRootPath),
		Authorizer:       authorizer.AuthorizerFunc(p.authorizeCluster),
		ReadyChecker:     framework.ReadyFunc(p.readyCluster),
		HandlerFactory: handler.HandlerFactory(func(rootAPIServerConfig genericapiserver.CompletedConfig) (http.Handler, error) {
			return http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					ctx := r.Context()
					//	logger := klog.FromContext(ctx)

					ri, exists := genericapirequest.RequestInfoFrom(ctx)
					if !exists {
						http.Error(w, "could not determine resource", http.StatusInternalServerError)
						return
					}

					// proxy vcluster  (goes to same cluster but with vcluster inside namespace)

					// proxy kubecluster (goes to full cluster)

					switch ri.Resource {
					case "kubeclusters":
						p.proxyKubeCluster(ri, w, r)
					case "vclusters":
						p.proxyVCluster(ri, w, r)
					default:
						http.Error(w, "could not determine resource - unknown provider", http.StatusInternalServerError)
					}
				}), nil
		}),
	}
}

func (p *clusterProxy) proxyKubeCluster(ri *genericapirequest.RequestInfo, w http.ResponseWriter, r *http.Request) {
	if !ri.IsResourceRequest ||
		ri.Resource != "kubeclusters" ||
		ri.Subresource != "proxy" ||
		ri.APIGroup != targetsv1alpha1.SchemeGroupVersion.Group ||
		ri.APIVersion != targetsv1alpha1.SchemeGroupVersion.Version ||
		ri.Name == "" {
		http.Error(w, "could not determine resource", http.StatusInternalServerError)
		return
	}

	// Get secrets from url and strip it away:
	// https://localhost:6444/services/cluster-proxy/1t1mlh6tgdzuelpg/apis/targets.contrib.kcp.io/v1alpha1/kubeclusters/proxy-cluster/proxy/secret/cFI-tl0Qvwqnln4N
	//                  ┌─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘
	// We are now here: ┘
	parts := strings.SplitN(r.URL.Path, "/", 8)
	secrets := strings.TrimPrefix(parts[7], "secret/")
	parts = strings.SplitN(secrets, "/", 2)

	value, found := p.store.Get(state.KindKubeClusters, parts[0])
	if !found {
		http.Error(w, "Unauthorized", http.StatusInternalServerError)
		return
	}

	// Parse the API server URL from the config
	apiServerURL, err := url.Parse(value.Config.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Create a reverse proxy that targets the Kubernetes API server
	proxy := httputil.NewSingleHostReverseProxy(apiServerURL)
	transport, err := rest.TransportFor(value.Config)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	proxy.Transport = transport

	r.URL.Path = parts[1]
	proxy.ServeHTTP(w, r)
}

func (p *clusterProxy) proxyVCluster(ri *genericapirequest.RequestInfo, w http.ResponseWriter, r *http.Request) {
	if !ri.IsResourceRequest ||
		ri.Resource != "vclusters" ||
		ri.Subresource != "proxy" ||
		ri.APIGroup != targetsv1alpha1.SchemeGroupVersion.Group ||
		ri.APIVersion != targetsv1alpha1.SchemeGroupVersion.Version ||
		ri.Name == "" {
		http.Error(w, "could not determine resource", http.StatusInternalServerError)
		return
	}

	// Get secrets from url and strip it away:
	// https://localhost:6444/services/cluster-proxy/1t1mlh6tgdzuelpg/apis/targets.contrib.kcp.io/v1alpha1/vclusters/proxy-cluster/proxy/secret/cFI-tl0Qvwqnln4N
	//
	//	┌─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘
	//
	// We are now here: ┘
	parts := strings.SplitN(r.URL.Path, "/", 8)
	secrets := strings.TrimPrefix(parts[7], "secret/")
	parts = strings.SplitN(secrets, "/", 2)

	value, found := p.store.Get(state.KindVClusters, parts[0])
	if !found {
		http.Error(w, "Unauthorized", http.StatusInternalServerError)
		return
	}

	// Parse the API server URL from the config
	apiServerURL, err := url.Parse(value.Config.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Define port-forward parameters
	serviceName := "vcluster" // Name of the service hardcoded in provisioner.

	// Find a pod that matches the service's selector
	podName, err := findPodForService(value.Client, value.VClusterNamespace, serviceName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	tlsConfig, err := rest.TLSConfigFor(value.VClusterConfig)
	if err != nil {
		log.Fatalf("Failed to convert TLSClientConfig: %v", err)
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return portforward.DialContext(ctx, value.Config, value.VClusterNamespace, podName, "6443")
		},
		TLSClientConfig: tlsConfig,
	}

	// Create a reverse proxy that targets the Kubernetes API server
	proxy := httputil.NewSingleHostReverseProxy(apiServerURL)
	proxy.Transport = transport

	r.URL.Path = parts[1]
	proxy.ServeHTTP(w, r)
}

// findPodForService finds a pod that matches the service's selectors
func findPodForService(clientset *kubernetes.Clientset, namespace string, serviceName string) (string, error) {
	service, err := clientset.CoreV1().Services(namespace).Get(context.TODO(), serviceName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get service: %w", err)
	}

	// Get the label selector for the service
	labelSelector := metav1.FormatLabelSelector(&metav1.LabelSelector{MatchLabels: service.Spec.Selector})
	pods, err := clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return "", fmt.Errorf("failed to list pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pods found for service %s", serviceName)
	}

	// Return the name of the first pod found
	return pods.Items[0].Name, nil
}
