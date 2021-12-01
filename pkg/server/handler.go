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

package server

import (
	"net/http"
	"regexp"
	"strings"

	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
)

var reClusterName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,78}[a-z0-9]$`)

func ServeHTTP(apiHandler http.Handler, c *genericapiserver.Config) func(w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		var clusterName string
		if path := req.URL.Path; strings.HasPrefix(path, "/clusters/") {
			path = strings.TrimPrefix(path, "/clusters/")
			i := strings.Index(path, "/")
			if i == -1 {
				http.Error(w, "Unknown cluster", http.StatusNotFound)
				return
			}
			clusterName, path = path[:i], path[i:]
			req.URL.Path = path
			for i := 0; i < 2 && len(req.URL.RawPath) > 1; i++ {
				slash := strings.Index(req.URL.RawPath[1:], "/")
				if slash == -1 {
					http.Error(w, "Unknown cluster", http.StatusNotFound)
					return
				}
				req.URL.RawPath = req.URL.RawPath[slash:]
			}
		} else {
			clusterName = req.Header.Get("X-Kubernetes-Cluster")
		}
		var cluster genericapirequest.Cluster
		switch clusterName {
		case "*":
			// HACK: just a workaround for testing
			cluster.Wildcard = true
			fallthrough
		case "":
			cluster.Name = genericcontrolplane.SanitizedClusterName(c.ExternalAddress, genericcontrolplane.RootClusterName)
		default:
			if !reClusterName.MatchString(clusterName) {
				http.Error(w, "Unknown cluster", http.StatusNotFound)
				return
			}
			cluster.Name = clusterName
		}
		ctx := genericapirequest.WithCluster(req.Context(), cluster)
		apiHandler.ServeHTTP(w, req.WithContext(ctx))
	}
}
