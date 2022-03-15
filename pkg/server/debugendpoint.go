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

package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path"
	"sync"

	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/klog/v2"
)

func (s *Server) installController(ctx context.Context, controllerName string, enabled bool, run func(context.Context)) error {
	server := s.serverChain.MiniAggregator.GenericAPIServer

	var (
		lock    sync.RWMutex
		ctrlCtx context.Context
		cancel  context.CancelFunc
	)

	server.Handler.NonGoRestfulMux.Handle(path.Join("/debug/controllers", controllerName, "enabled"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			lock.RLock()
			defer lock.RUnlock()

			if enabled {
				if _, err := w.Write([]byte("1")); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				}
			} else {
				if _, err := w.Write([]byte("0")); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				}
			}
		case "POST":
			lock.Lock()
			defer lock.Unlock()

			bs, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
			defer r.Body.Close() // nolint: errcheck

			newEnabled := string(bs) == "1"
			if newEnabled != enabled {
				if newEnabled {
					ctrlCtx, cancel = context.WithCancel(ctx)
					run(ctrlCtx)
				} else {
					cancel()
				}
				enabled = newEnabled
			}
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}))

	s.AddPostStartHook(fmt.Sprintf("kcp-start-%s-controller", controllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-start-%s-controller: %v", controllerName, err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		lock.RLock()
		defer lock.RUnlock()
		go run(ctx)

		return nil
	})

	return nil
}
