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

package boostrap

import (
	"context"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"path"
	"strings"
	"sync"

	"github.com/kcp-dev/logicalcluster/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	errorsutil "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
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
	grouptResourceExportNames map[schema.GroupResource]string,
	kcpClient kcpclient.Interface) (identityConfig *rest.Config, resolve func(ctx context.Context) error) {

	ids := &identities{
		groupIdentities:         map[string]string{},
		groupResourceIdentities: map[schema.GroupResource]string{},
	}

	for group := range groupExportNames {
		ids.groupIdentities[group] = ""
	}
	for gr := range grouptResourceExportNames {
		ids.groupResourceIdentities[gr] = ""
	}

	identityConfig = rest.CopyConfig(config)
	identityConfig.Wrap(injectKcpIdentities(ids))

	return identityConfig, func(ctx context.Context) error {
		var errs []error
		for group, name := range groupExportNames {
			ids.lock.RLock()
			id := ids.groupIdentities[group]
			ids.lock.RUnlock()

			if id != "" {
				continue
			}

			apiExport, err := kcpClient.ApisV1alpha1().APIExports().Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if apiExport.Status.IdentityHash == "" {
				errs = append(errs, fmt.Errorf("APIExport %s is not ready", name))
				continue
			}

			ids.lock.Lock()
			ids.groupIdentities[group] = apiExport.Status.IdentityHash
			ids.lock.Unlock()

			klog.V(2).Infof("APIExport %s|%s for group %q has identity %s", logicalcluster.From(apiExport), name, group, apiExport.Status.IdentityHash)
		}
		for gr, name := range grouptResourceExportNames {
			ids.lock.RLock()
			id := ids.groupResourceIdentities[gr]
			ids.lock.RUnlock()

			if id != "" {
				continue
			}

			apiExport, err := kcpClient.ApisV1alpha1().APIExports().Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if apiExport.Status.IdentityHash == "" {
				errs = append(errs, fmt.Errorf("APIExport %s is not ready", name))
				continue
			}

			ids.lock.Lock()
			ids.groupResourceIdentities[gr] = apiExport.Status.IdentityHash
			ids.lock.Unlock()

			klog.V(2).Infof("APIExport %s|%s for resource %s.%s has identity %s", logicalcluster.From(apiExport), name, gr.Resource, gr.Group, apiExport.Status.IdentityHash)
		}

		return errorsutil.NewAggregate(errs)
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
//   /clusters/*/apis/tenancy.kcp.dev/v1alpha1/clusterworkspaces/root
//
// becomes
//
//   /clusters/*/apis/tenancy.kcp.dev/v1alpha1/clusterworkspaces:<identity>/root
func decorateWildcardPathsWithResourceIdentities(urlPath string, ids *identities) (string, error) {
	// Check for: /clusters/*/apis/tenancy.kcp.dev/v1alpha1/clusterworkspaces/root
	if !strings.HasPrefix(urlPath, "/clusters/*/apis/") {
		return urlPath, nil
	}

	comps := strings.SplitN(strings.TrimPrefix(urlPath, "/"), "/", 7)
	if len(comps) < 6 {
		return urlPath, nil
	}

	gr := schema.GroupResource{Group: comps[3], Resource: comps[5]}
	if id, found := ids.grIdentity(gr); found {
		if len(id) == 0 {
			return "", fmt.Errorf("identity for %s is unknown", gr)
		}
		comps[5] += ":" + id

		return "/" + path.Join(comps...), nil
	}

	return urlPath, nil
}
