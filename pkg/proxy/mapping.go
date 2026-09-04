/*
Copyright 2022 The kcp Authors.

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

package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/fsnotify/fsnotify"

	"sigs.k8s.io/yaml"

	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/proxy/metrics"
	"github.com/kcp-dev/kcp/pkg/server/proxy"
	"github.com/kcp-dev/kcp/pkg/server/proxy/types"
)

// loadMappings reads path mappings from filename.
// Empty filename or a missing file yields nil mappings.
func loadMappings(filename string) ([]types.PathMapping, error) {
	if filename == "" {
		return nil, nil
	}

	mappingData, err := os.ReadFile(filename)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var mapping []types.PathMapping
	if err := yaml.Unmarshal(mappingData, &mapping); err != nil {
		return nil, err
	}

	return mapping, nil
}

// NewHandler builds the additional-routes proxy handler from the configured mappings.
func NewHandler(ctx context.Context, mappings []types.PathMapping) (http.Handler, error) {
	handlers := proxy.HttpHandler{
		Mappings: types.HttpHandlerMappings{},
	}

	logger := klog.FromContext(ctx)
	for _, m := range mappings {
		logger.WithValues("mapping", m).V(2).Info("adding mapping")

		u, err := url.Parse(m.Backend)
		if err != nil {
			return nil, fmt.Errorf("failed to create path mapping for path %q: failed to parse URL %q: %w", m.Path, m.Backend, err)
		}

		transport, err := newTransport(m.ProxyClientCert, m.ProxyClientKey, m.BackendServerCA)
		if err != nil {
			return nil, fmt.Errorf("failed to create path mapping for path %q: %w", m.Path, err)
		}

		reverseProxy := httputil.NewSingleHostReverseProxy(u)
		reverseProxy.Transport = transport
		reverseProxy.ErrorHandler = metrics.NewProxyErrorHandler()
		var handler http.Handler = reverseProxy

		userHeader := "X-Remote-User"
		groupHeader := "X-Remote-Group"
		extraHeaderPrefix := "X-Remote-Extra-"
		if m.UserHeader != "" {
			userHeader = m.UserHeader
		}
		if m.GroupHeader != "" {
			groupHeader = m.GroupHeader
		}
		if m.ExtraHeaderPrefix != "" {
			extraHeaderPrefix = m.ExtraHeaderPrefix
		}

		handler = WithProxyAuthHeaders(handler, userHeader, groupHeader, extraHeaderPrefix)

		logger.V(2).WithValues("path", m.Path).Info("adding handler")
		if m.Path == "/" {
			handlers.DefaultHandler = handler
		} else {
			handlers.Mappings = append(handlers.Mappings, types.HttpHandlerMapping{
				Weight:  len(m.Path),
				Path:    m.Path,
				Handler: handler,
			})
		}
	}

	handlers.Mappings.Sort()

	return &handlers, nil
}

// reloadableHandler delegates to an atomically swappable handler.
type reloadableHandler struct {
	handler atomic.Pointer[http.Handler]
}

func (h *reloadableHandler) store(handler http.Handler) {
	h.handler.Store(&handler)
}

func (h *reloadableHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	(*h.handler.Load()).ServeHTTP(w, r)
}

// watchMappingFile calls reload when filename is created or written until ctx is done.
// The parent directory is watched to catch late creation, deletion and atomic updates.
// Reload errors are logged and the previous state is kept.
func watchMappingFile(ctx context.Context, filename string, reload func() error) error {
	logger := klog.FromContext(ctx).WithValues("file", filename)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create mapping file watcher: %w", err)
	}
	defer watcher.Close()

	if err := watcher.Add(filepath.Dir(filename)); err != nil {
		return fmt.Errorf("failed to watch mapping file directory: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			logger.Error(err, "mapping file watch error")
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if event.Name != filename || !event.Op.Has(fsnotify.Create) && !event.Op.Has(fsnotify.Write) {
				continue
			}
			// file may be gone mid atomic-swap; the create of the new file follows
			if _, err := os.Stat(filename); err != nil {
				continue
			}
			if err := reload(); err != nil {
				logger.Error(err, "failed to reload mapping file, keeping previous mappings")
				continue
			}
			logger.V(2).Info("reloaded mapping file")
		}
	}
}
