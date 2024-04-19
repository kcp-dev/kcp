/*
Copyright 2024 The KCP Authors.

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

package openapiv3

import (
	"bytes"
	"crypto/sha512"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/emicklei/go-restful/v3"
	"github.com/go-logr/logr"
	"github.com/kcp-dev/logicalcluster/v3"

	apiextensionshelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/controller/openapi/builder"
	"k8s.io/apiextensions-apiserver/pkg/kcp"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/server/mux"
	"k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/builder3"
	"k8s.io/kube-openapi/pkg/cached"
	"k8s.io/kube-openapi/pkg/common"
	"k8s.io/kube-openapi/pkg/common/restfuladapter"
	"k8s.io/kube-openapi/pkg/handler3"
	"k8s.io/kube-openapi/pkg/spec3"
	"k8s.io/utils/lru"
)

const (
	// DefaultServiceCacheSize is the default size of the OpenAPI service cache.
	// Equal API configurations in multiple workspaces are shared.
	DefaultServiceCacheSize = 100

	// TODO(sttts): move to central place in kube.
	BoundAnnotationKey = "apis.kcp.io/bound-crd"
)

// WithOpenAPIv3 returns a handler that serves OpenAPI v3 specs for CRDs, not
// forwarding /openapi/v3 requests to the delegate handler.
func WithOpenAPIv3(handler http.Handler, c *ServiceCache) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi/v3" || strings.HasPrefix(r.URL.Path, "/openapi/v3/") {
			c.ServeHTTP(w, r)
			return
		}

		handler.ServeHTTP(w, r)
	})
}

// ServiceCache implements a cluster-aware OpenAPI v3 handler, sharing the
// OpenAPI service for equal API surface configurations.
type ServiceCache struct {
	config *common.Config

	specGetter CRDSpecGetter
	crdLister  kcp.ClusterAwareCRDClusterLister

	services    *lru.Cache
	staticSpecs map[string]cached.Data[*spec3.OpenAPI]
}

func NewServiceCache(config *common.Config, crdLister kcp.ClusterAwareCRDClusterLister, specGetter CRDSpecGetter, serviceCacheSize int) *ServiceCache {
	return &ServiceCache{
		config:      config,
		specGetter:  specGetter,
		crdLister:   crdLister,
		services:    lru.New(serviceCacheSize),
		staticSpecs: map[string]cached.Data[*spec3.OpenAPI]{},
	}
}

func (c *ServiceCache) RegisterStaticAPIs(cont *restful.Container) error {
	// create static specs
	byGVPath := make(map[string][]*restful.WebService)
	for _, t := range cont.RegisteredWebServices() {
		// Strip the "/" prefix from the name
		gvPath := t.RootPath()[1:]
		byGVPath[gvPath] = []*restful.WebService{t}
	}
	for gvPath, ws := range byGVPath {
		spec, err := builder3.BuildOpenAPISpecFromRoutes(restfuladapter.AdaptWebServices(ws), c.config)
		if err != nil {
			return fmt.Errorf("failed to build OpenAPI v3 spec for %s: %w", gvPath, err)
		}
		etag, err := computeEtag(spec)
		if err != nil {
			return fmt.Errorf("failed to compute OpenAPI v3 spec etag for %s: %w", gvPath, err)
		}
		c.staticSpecs[gvPath] = cached.NewResultOK(spec, etag)
	}

	return nil
}

func (c *ServiceCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	clusterName, err := request.ClusterNameFrom(r.Context())
	if err != nil {
		http.NotFound(w, r)
		return
	}

	log := klog.FromContext(ctx).WithValues("cluster", clusterName, "path", r.URL.Path)

	// get both real CRDs and bound CRD from APIBindings
	crds, err := c.crdLister.Cluster(clusterName).List(ctx, labels.Everything())
	if err != nil {
		responsewriters.InternalError(w, r, err)
		return
	}

	// operate on sorted lists to have deterministic API configuration key
	orderedCRDs := make([]*apiextensionsv1.CustomResourceDefinition, len(crds))
	copy(orderedCRDs, crds)
	sort.Sort(byClusterAndName(orderedCRDs))

	// get the specs for all CRDs
	specs := make([]map[string]cached.Data[*spec3.OpenAPI], 0, len(orderedCRDs))
	for _, crd := range orderedCRDs {
		versionSpecs, err := c.specGetter.GetCRDSpecs(logicalcluster.From(crd), crd.Name)
		if err != nil {
			responsewriters.InternalError(w, r, err)
			return
		}
		specs = append(specs, versionSpecs)
	}

	// get the OpenAPI service from cache or create a new one
	key := apiConfigurationKey(orderedCRDs, specs)
	log = log.WithValues("key", key)
	entry, ok := c.services.Get(key)
	if !ok {
		log.V(7).Info("Creating new OpenAPI v3 service")

		// create a new OpenAPI service
		mux := mux.NewPathRecorderMux("cluster-aware-openapi-v3")
		service := handler3.NewOpenAPIService()
		err := service.RegisterOpenAPIV3VersionedService("/openapi/v3", mux)
		if err != nil {
			responsewriters.InternalError(w, r, err)
			return
		}

		// add static and dynamic APIs
		if err := addSpecs(service, c.staticSpecs, orderedCRDs, specs, log); err != nil {
			responsewriters.InternalError(w, r, err)
			return
		}

		// remember for next time
		c.services.Add(key, mux)
		entry = mux
	} else {
		log.V(7).Info("Reusing OpenAPI v3 service from cache")
	}

	service := entry.(http.Handler)
	service.ServeHTTP(w, r)
}

func addSpecs(service *handler3.OpenAPIService, static map[string]cached.Data[*spec3.OpenAPI], crds []*apiextensionsv1.CustomResourceDefinition, specs []map[string]cached.Data[*spec3.OpenAPI], log logr.Logger) error {
	// start with static specs
	byGroupVersionSpecs := make(map[string][]cached.Data[*spec3.OpenAPI])
	for gvPath, spec := range static {
		byGroupVersionSpecs[gvPath] = []cached.Data[*spec3.OpenAPI]{spec}
	}

	// add dynamic specs
	for i, crd := range crds {
		spec := specs[i]
		if !apiextensionshelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established) {
			continue
		}
		for _, v := range crd.Spec.Versions {
			versionSpec, ok := spec[v.Name]
			if !ok {
				continue
			}
			gv := schema.GroupVersion{Group: crd.Spec.Group, Version: v.Name}
			gvPath := groupVersionToOpenAPIV3Path(gv)
			byGroupVersionSpecs[gvPath] = append(byGroupVersionSpecs[gvPath], versionSpec)
		}
	}

	// lazily merge spec and add to service
	for gvPath, specs := range byGroupVersionSpecs {
		gvSpec := cached.NewListMerger(func(results []cached.Result[*spec3.OpenAPI]) cached.Result[*spec3.OpenAPI] {
			log.V(6).Info("Merging OpenAPI v3 specs", "gvPath", gvPath)
			specs := make([]*spec3.OpenAPI, 0, len(results))
			etags := make([]string, 0, len(results))
			for _, result := range results {
				if result.Err != nil {
					continue
				}
				specs = append(specs, result.Data)
				etags = append(etags, result.Etag)
			}
			merged, err := builder.MergeSpecsV3(specs...)
			if err != nil {
				return cached.NewResultErr[*spec3.OpenAPI](fmt.Errorf("failed to merge specs: %v", err))
			}
			return cached.NewResultOK[*spec3.OpenAPI](merged, fmt.Sprintf("%X", sha512.Sum512([]byte(strings.Join(etags, ",")))))
		}, specs)
		service.UpdateGroupVersionLazy(gvPath, gvSpec)
	}

	return nil
}

func apiConfigurationKey(orderedCRDs []*apiextensionsv1.CustomResourceDefinition, specs []map[string]cached.Data[*spec3.OpenAPI]) string {
	var buf bytes.Buffer
	for i, crd := range orderedCRDs {
		spec := specs[i]
		if !apiextensionshelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established) {
			continue
		}
		buf.WriteString(crd.Name)
		buf.WriteRune(':')
		firstVersion := true
		for _, v := range crd.Spec.Versions {
			versionSpec, ok := spec[v.Name]
			if !ok {
				continue
			}
			if !firstVersion {
				buf.WriteRune(',')
			}
			buf.WriteString(v.Name)
			buf.WriteRune(':')
			buf.WriteString(versionSpec.Get().Etag)

			firstVersion = false
		}

		buf.WriteRune(';')
	}

	return buf.String()
}

func groupVersionToOpenAPIV3Path(gv schema.GroupVersion) string {
	return "apis/" + gv.Group + "/" + gv.Version
}

type byClusterAndName []*apiextensionsv1.CustomResourceDefinition

func (a byClusterAndName) Len() int      { return len(a) }
func (a byClusterAndName) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a byClusterAndName) Less(i, j int) bool {
	_, aBound := a[i].Annotations[BoundAnnotationKey]
	_, bBound := a[i].Annotations[BoundAnnotationKey]
	if aBound && !bBound {
		return true
	}
	if !aBound && bBound {
		return false
	}

	aCluster := logicalcluster.From(a[i])
	bCluster := logicalcluster.From(a[j])
	if aCluster != bCluster {
		return aCluster < bCluster
	}

	return a[i].Name < a[j].Name
}

func computeEtag(spec *spec3.OpenAPI) (string, error) {
	bs, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("failed to marshal OpenAPI v3 spec: %w", err)
	}
	return fmt.Sprintf("%X", sha512.Sum512(bs)), nil
}
