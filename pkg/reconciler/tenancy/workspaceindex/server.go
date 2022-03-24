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

package workspaceindex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"

	"k8s.io/apiserver/pkg/server/healthz"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
)

// NewServer creates a new server that can respond to requests for versioned data in workspaces.
func NewServer(port int, waiter cacheSyncWaiter, index Index, stable func() bool) Server {
	return &server{
		port:   port,
		waiter: waiter,
		index:  index,
		stable: stable,
	}
}

type Server interface {
	ListenAndServe(ctx context.Context)
}

type server struct {
	port   int
	waiter cacheSyncWaiter
	index  Index
	stable func() bool
}

type cacheSyncWaiter interface {
	WaitForCacheSync(stopCh <-chan struct{}) map[reflect.Type]bool
}

func (s *server) ListenAndServe(ctx context.Context) {
	mux := http.NewServeMux()
	mux.Handle("/shard", http.HandlerFunc(s.handleShard))
	mux.Handle("/data", http.HandlerFunc(s.handleData))
	healthz.InstallHandler(mux)
	healthz.InstallReadyzHandler(mux, healthz.NamedCheck("workspaces-synced", func(r *http.Request) error {
		if !s.stable() {
			return errors.New("work queues have outstanding keys")
		}
		return nil
	}), healthz.NewInformerSyncHealthz(s.waiter))
	httpServer := http.Server{Addr: ":" + strconv.Itoa(s.port), Handler: mux}
	go func() {
		<-ctx.Done()
		if err := httpServer.Shutdown(context.Background()); err != nil {
			klog.Error(err)
		}
	}()
	if err := httpServer.ListenAndServe(); err != nil {
		klog.Error(err)
	}
}

const (
	clusterNameQuery     = "clusterName"
	resourceVersionQuery = "resourceVersion"
)

func (s *server) handleShard(w http.ResponseWriter, r *http.Request) {
	// ensure the cache is stable before serving
	if !s.stable() {
		w.Header().Set("Retry-After", "10")
		http.Error(w, "workspace index cache is not stable", http.StatusServiceUnavailable)
		return
	}
	// first, validate the input from the user
	q := r.URL.Query()
	clusterName := q.Get(clusterNameQuery)
	if clusterName == "" {
		http.Error(w, "clusterName query must not be empty", http.StatusBadRequest)
		return
	}
	organization, workspace, err := helper.ParseLogicalClusterName(clusterName)
	if err != nil {
		http.Error(w, fmt.Sprintf("clusterName query invalid: %v", err), http.StatusBadRequest)
		return
	}
	resourceVersionString := q.Get(resourceVersionQuery)
	if resourceVersionString == "" {
		resourceVersionString = "0"
	}
	resourceVersion, err := strconv.ParseInt(resourceVersionString, 10, 64)
	if err != nil {
		http.Error(w, fmt.Sprintf("resourceVersion query must be an integer: %v", err), http.StatusBadRequest)
		return
	}

	// then, find the shard history for the workspace in question
	history, err := s.index.Get(organization, workspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if len(history) == 0 {
		http.Error(w, fmt.Sprintf("cluster %q has no shard history", clusterName), http.StatusInternalServerError)
		return
	}

	// finally, find the part of the history that the user asked for. We assume that the
	// history is sorted in ascending order of resourceVersion and that no resourceVersion
	// ranges overlap
	var shardName string
	if resourceVersion == 0 {
		// if the user asked for no particular resource version or literally "0", we want
		// up-to-date results, served from the current shard this workspace sits on
		shardName = history[len(history)-1].Name
	} else {
		idx := sort.Search(len(history), func(i int) bool {
			return history[i].LiveAfterResourceVersion < resourceVersion && history[i].LiveBeforeResourceVersion >= resourceVersion
		})
		if idx == len(history) {
			// this should never happen, since the current shard should have no liveBefore set
			http.Error(w, fmt.Sprintf("cluster %q has no shard history containing resourceVersion %d", clusterName, resourceVersion), http.StatusInternalServerError)
			return
		}
		shardName = history[idx].Name
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, shardName)
}

func (s *server) handleData(w http.ResponseWriter, r *http.Request) {
	if !s.stable() {
		w.Header().Set("Retry-After", "10")
		http.Error(w, "workspace index cache is not stable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	raw, err := s.index.MarshalJSON()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to marshal data: %v", err), http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, string(raw))
}
