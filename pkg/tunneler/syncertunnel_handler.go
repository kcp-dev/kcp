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

package tunneler

import (
	"net/http"
	"time"

	"github.com/aojea/rwconn"

	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

// WithSyncerTunnelHandler adds an HTTP Handler that handles reverse connections via the tunnel subresource:
//
// https://host/clusters/<ws>/apis/workload.kcp.io/v1alpha1/synctargets/<name>/tunnel establish reverse connections and queue them so it can be consumed by the dialer
func (tn *tunneler) WithSyncerTunnelHandler(apiHandler http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := klog.FromContext(ctx)

		ri, ok := genericapirequest.RequestInfoFrom(ctx)
		if !ok {
			apiHandler.ServeHTTP(w, r)
			return
		}

		if !ri.IsResourceRequest ||
			ri.Resource != "synctargets" ||
			ri.Subresource != "tunnel" ||
			ri.APIGroup != workloadv1alpha1.SchemeGroupVersion.Group ||
			ri.APIVersion != workloadv1alpha1.SchemeGroupVersion.Version ||
			ri.Name == "" {
			apiHandler.ServeHTTP(w, r)
			return
		}

		cluster, err := genericapirequest.ValidClusterFrom(ctx)
		if err != nil {
			apiHandler.ServeHTTP(w, r)
			return
		}

		clusterName := cluster.Name
		syncerName := ri.Name

		logger = logger.WithValues("cluster", clusterName, "syncerName", syncerName, "action", "tunnel")
		logger.V(5).Info("tunneler connection received")
		d := tn.getDialer(clusterName, syncerName)
		// First flush response headers
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "flusher not implemented", http.StatusInternalServerError)
			return
		}

		// first connection to register the dialer and start the control loop
		fw := &flushWriter{w: w, f: flusher}
		doneCh := make(chan struct{})
		conn := rwconn.NewConn(r.Body, fw, rwconn.SetWriteDelay(500*time.Millisecond), rwconn.SetCloseHook(func() {
			// exit the handler
			close(doneCh)
		}))
		if d == nil || isClosedChan(d.Done()) {
			// start clean
			tn.deleteDialer(clusterName, syncerName)
			tn.createDialer(clusterName, syncerName, conn)
			// start control loop
			select {
			case <-r.Context().Done():
				conn.Close()
			case <-doneCh:
			}
			logger.V(5).Info("stopped tunnel control connection")
			return
		}
		logger.Info("Creating tunnel connection", "clustername", clusterName, "syncername", syncerName)
		// create a reverse connection
		logger.V(5).Info("tunnel connection started")
		select {
		case d.incomingConn <- conn:
		case <-d.Done():
			http.Error(w, "syncer tunnels: tunnel closed", http.StatusInternalServerError)
			return
		}
		// keep the handler alive until the connection is closed
		select {
		case <-r.Context().Done():
			conn.Close()
		case <-doneCh:
		}
		logger.V(5).Info("tunnel connection done", "remoteAddr", r.RemoteAddr)
	}
}
