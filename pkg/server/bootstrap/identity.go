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

package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"path"
	"strings"
	"sync"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	errorsutil "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	configshard "github.com/kcp-dev/kcp/config/shard"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/identitycache"
)

var (
	// KcpRootGroupExportNames lists the APIExports in the root workspace for standard kcp groups
	KcpRootGroupExportNames = map[string]string{
		"tenancy.kcp.dev":     "tenancy.kcp.dev",
		"scheduling.kcp.dev":  "scheduling.kcp.dev",
		"workload.kcp.dev":    "workload.kcp.dev",
		"apiresource.kcp.dev": "apiresource.kcp.dev",
	}

	// KcpRootGroupResourceExportNames lists the APIExports in the root workspace for standard kcp group resources
	KcpRootGroupResourceExportNames = map[schema.GroupResource]string{
		{Group: "tenancy.kcp.dev", Resource: "clusterworkspaceshards"}: "shards.tenancy.kcp.dev",
	}
)

type identities struct {
	lock sync.RWMutex

	groupIdentities         map[string]string
	groupResourceIdentities map[schema.GroupResource]string
}

func (ids *identities) grIdentity(gr schema.GroupResource) (string, bool) {
	ids.lock.RLock()
	defer ids.lock.RUnlock()

	if id, found := ids.groupResourceIdentities[gr]; found {
		return id, true
	}
	if id, found := ids.groupIdentities[gr.Group]; found {
		return id, true
	}
	return "", false
}

// NewConfigWithWildcardIdentities creates a rest.Config with injected resource identities for
// individual group or group resources. Each group or resource is coming from one APIExport whose
// names are passed in as a map.
//
// The returned resolve function will get the APIExports and extract the identities.
// The resolve func might return an error if the APIExport is not found or for other reason. Only
// after it succeeds a client using the returned rest.Config can use the group and group resources
// with identities.
func NewConfigWithWildcardIdentities(config *rest.Config,
	groupExportNames map[string]string,
	groupResourceExportNames map[schema.GroupResource]string,
	localShardKubeClusterClient kcpkubernetesclientset.ClusterInterface) (identityConfig *rest.Config, resolve func(ctx context.Context) error) {
	identityRoundTripper, identityResolver := NewWildcardIdentitiesWrappingRoundTripper(groupExportNames, groupResourceExportNames, config, localShardKubeClusterClient)
	identityConfig = rest.CopyConfig(config)
	identityConfig.Wrap(identityRoundTripper)
	return identityConfig, identityResolver
}

// NewWildcardIdentitiesWrappingRoundTripper creates an HTTP RoundTripper
// that injected resource identities for individual group or group resources.
// Each group or resource is coming from one APIExport whose names are passed in as a map.
// The RoundTripper is exposed as a function that allows wrapping the RoundTripper
//
// The method also returns the resolve function that gets the APIExports and extract the identities.
// The resolve func might return an error if the APIExport is not found or for other reason. Only
// after it succeeds a client using the returned RoundTripper can use the group and group resources
// with identities.
func NewWildcardIdentitiesWrappingRoundTripper(groupExportNames map[string]string,
	groupResourceExportNames map[schema.GroupResource]string,
	config *rest.Config,
	localShardKubeClusterClient kcpkubernetesclientset.ClusterInterface) (func(rt http.RoundTripper) http.RoundTripper, func(ctx context.Context) error) {
	ids := &identities{
		groupIdentities:         map[string]string{},
		groupResourceIdentities: map[schema.GroupResource]string{},
	}

	for group := range groupExportNames {
		ids.groupIdentities[group] = ""
	}
	for gr := range groupResourceExportNames {
		ids.groupResourceIdentities[gr] = ""
	}

	return injectKcpIdentities(ids), wildcardIdentitiesResolver(ids, groupExportNames, groupResourceExportNames, apiExportIdentityProvider(config, localShardKubeClusterClient))
}

func wildcardIdentitiesResolver(ids *identities,
	groupExportNames map[string]string,
	groupResourceExportNames map[schema.GroupResource]string,
	apiExportIdentityProviderFn func(ctx context.Context, apiExportName string) (string, error)) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		logger := klog.FromContext(ctx)
		var errs []error
		for group, name := range groupExportNames {
			logger := logging.WithObject(logger, &apisv1alpha1.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Name:        name,
					Annotations: map[string]string{logicalcluster.AnnotationKey: tenancyv1alpha1.RootCluster.String()},
				},
			}).WithValues("group", group)
			ids.lock.RLock()
			id := ids.groupIdentities[group]
			ids.lock.RUnlock()

			if id != "" {
				continue
			}

			apiExportIdentity, err := apiExportIdentityProviderFn(ctx, name)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if apiExportIdentity == "" {
				errs = append(errs, fmt.Errorf("APIExport %s is not ready", name))
				continue
			}
			logger = logger.WithValues("identity", apiExportIdentity)

			ids.lock.Lock()
			ids.groupIdentities[group] = apiExportIdentity
			ids.lock.Unlock()

			logger.V(2).Info("APIExport has identity")
		}
		for gr, name := range groupResourceExportNames {
			logger := logging.WithObject(logger, &apisv1alpha1.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Name:        name,
					Annotations: map[string]string{logicalcluster.AnnotationKey: tenancyv1alpha1.RootCluster.String()},
				},
			}).WithValues("gr", gr.String())
			ids.lock.RLock()
			id := ids.groupResourceIdentities[gr]
			ids.lock.RUnlock()

			if id != "" {
				continue
			}

			apiExportIdentity, err := apiExportIdentityProviderFn(ctx, name)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if apiExportIdentity == "" {
				errs = append(errs, fmt.Errorf("APIExport %s is not ready", name))
				continue
			}
			logger = logger.WithValues("identity", apiExportIdentity)

			ids.lock.Lock()
			ids.groupResourceIdentities[gr] = apiExportIdentity
			ids.lock.Unlock()

			logger.V(2).Info("APIExport has identity")
		}
		return errorsutil.NewAggregate(errs)
	}
}

func apiExportIdentityProvider(config *rest.Config, localShardKubeClusterClient kcpkubernetesclientset.ClusterInterface) func(ctx context.Context, apiExportName string) (string, error) {
	return func(ctx context.Context, apiExportName string) (string, error) {
		rootShardKcpClient, err := kcpclientset.NewForConfig(config)
		if err != nil {
			return "", err
		}

		if localShardKubeClusterClient != nil {
			apiExportIdentitiesConfigMap, err := localShardKubeClusterClient.Cluster(configshard.SystemShardCluster.Path()).CoreV1().ConfigMaps("default").Get(ctx, identitycache.ConfigMapName, metav1.GetOptions{})
			if err == nil {
				apiExportIdentity, found := apiExportIdentitiesConfigMap.Data[apiExportName]
				if found {
					return apiExportIdentity, nil
				}
			}
			// fall back when:
			// - this is not the root shard (localShardKubeClusterClient == nil)
			// - the cm wasn't found
			// - an entry in the cm wasn't found
		}
		apiExport, err := rootShardKcpClient.Cluster(tenancyv1alpha1.RootCluster.Path()).ApisV1alpha1().APIExports().Get(ctx, apiExportName, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		return apiExport.Status.IdentityHash, nil
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

var _ http.RoundTripper = roundTripperFunc(nil)

func (rt roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return rt(r)
}

// injectKcpIdentities injects the KCP identities into the request URLs.
func injectKcpIdentities(ids *identities) func(rt http.RoundTripper) http.RoundTripper {
	return func(rt http.RoundTripper) http.RoundTripper {
		return roundTripperFunc(func(origReq *http.Request) (*http.Response, error) {
			urlPath, err := decorateWildcardPathsWithResourceIdentities(origReq.URL.Path, ids)
			if err != nil {
				return nil, err
			}
			if urlPath == origReq.URL.Path {
				return rt.RoundTrip(origReq)
			}

			req := *origReq // shallow copy
			req.URL = &url.URL{}
			*req.URL = *origReq.URL
			req.URL.Path = urlPath

			return rt.RoundTrip(&req)
		})
	}
}

// decorateWildcardPathsWithResourceIdentities adds per-API-group identity to wildcard URL paths, e.g.
//
//	/clusters/*/apis/tenancy.kcp.dev/v1alpha1/clusterworkspaces/root
//
// becomes
//
//	/clusters/*/apis/tenancy.kcp.dev/v1alpha1/clusterworkspaces:<identity>/root
func decorateWildcardPathsWithResourceIdentities(urlPath string, ids *identities) (string, error) {
	// Check for: /clusters/*/apis/tenancy.kcp.dev/v1alpha1/clusterworkspaces/root
	if !strings.HasPrefix(urlPath, "/clusters/*/apis/") {
		return urlPath, nil
	}

	comps := strings.SplitN(strings.TrimPrefix(urlPath, "/"), "/", 7)
	if len(comps) < 6 {
		return urlPath, nil
	}

	// It's possible the outgoing request already has an identity specified. Make sure we exclude that when
	// determining the resource in question.
	parts := strings.SplitN(comps[5], ":", 2)
	if len(parts) == 0 {
		return "", fmt.Errorf("invalid resource %q", comps[5])
	}

	resource := parts[0]

	gr := schema.GroupResource{Group: comps[3], Resource: resource}
	if id, found := ids.grIdentity(gr); found && gr != tenancyv1alpha1.Resource("thisworkspaces") { // TODO(sttts): remove ThisWorkspace exception when moved to its own API group
		if len(id) == 0 {
			return "", fmt.Errorf("identity for %s is unknown", gr)
		}

		passedInIdentity := ""
		if len(parts) > 1 {
			passedInIdentity = parts[1]
		}

		if passedInIdentity != "" && passedInIdentity != id {
			return "", fmt.Errorf("invalid identity %q for resource %q", passedInIdentity, resource)
		}

		comps[5] = resource + ":" + id

		return "/" + path.Join(comps...), nil
	}

	return urlPath, nil
}
