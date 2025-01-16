/*
Copyright 2025 The KCP Authors.

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

package filters

import (
	"errors"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
)

// WithBlockInactiveLogicalClusters ensures that any requests to logical
// clusters marked inactive are rejected.
func WithBlockInactiveLogicalClusters(handler http.Handler, kcpClusterClient corev1alpha1informers.LogicalClusterClusterInformer) http.HandlerFunc {
	allowedPathPrefixes := []string{
		"/openapi",
		"/apis/core.kcp.io/v1alpha1/logicalclusters",
	}

	return func(w http.ResponseWriter, req *http.Request) {
		_, newURL, _, err := ClusterPathFromAndStrip(req)
		if err != nil {
			responsewriters.InternalError(w, req, err)
			return
		}

		isException := false
		for _, prefix := range allowedPathPrefixes {
			if strings.HasPrefix(newURL.String(), prefix) {
				isException = true
			}
		}

		cluster := request.ClusterFrom(req.Context())
		if cluster != nil && !cluster.Name.Empty() && !isException {
			logicalCluster, err := kcpClusterClient.Cluster(cluster.Name).Lister().Get(corev1alpha1.LogicalClusterName)
			if err == nil {
				if ann, ok := logicalCluster.ObjectMeta.Annotations[inactiveAnnotation]; ok && ann == "true" {
					responsewriters.ErrorNegotiated(
						apierrors.NewForbidden(corev1alpha1.Resource("logicalclusters"), cluster.Name.String(), errors.New("logical cluster is marked inactive")),
						errorCodecs, schema.GroupVersion{}, w, req,
					)
					return
				}
			} else if !apierrors.IsNotFound(err) {
				responsewriters.InternalError(w, req, err)
				return
			}
		}

		handler.ServeHTTP(w, req)
	}
}
