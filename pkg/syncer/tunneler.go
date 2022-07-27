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

package syncer

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"

	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	virtualcommandoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	"github.com/kcp-dev/kcp/pkg/revdial"
)

func startTunneler(ctx context.Context, upstream, downstream *rest.Config, key string) error {
	// syncer --> kcp
	clientUpstream, err := rest.HTTPClientFor(upstream)
	if err != nil {
		return err
	}

	// syncer --> local apiserver
	url, err := url.Parse(downstream.Host)
	if err != nil {
		return err
	}

	proxy := httputil.NewSingleHostReverseProxy(url)
	if err != nil {
		return err
	}

	clientDownstream, err := rest.HTTPClientFor(downstream)
	if err != nil {
		return err
	}
	proxy.Transport = clientDownstream.Transport

	u, err := url.Parse(upstream.Host)
	if err != nil {
		return err
	}
	// create the reverse connection
	dst := u.Scheme + "://" + u.Host + virtualcommandoptions.DefaultRootPathPrefix + revdial.DefaultRootPathPrefix + "/"
	klog.Infof("syncer: connecting to %s with key %s", dst, key)
	l, err := revdial.NewListener(clientUpstream, dst, key)
	if err != nil {
		return err
	}
	defer l.Close()

	// reverse proxy the request coming from the reverse connection to the p-cluster apiserver
	server := &http.Server{Handler: proxy}
	defer server.Close()

	klog.V(2).Infof("Serving on reverse connection")
	errCh := make(chan error)
	go func() {
		errCh <- server.Serve(l)
	}()

	select {
	case err = <-errCh:
	case <-ctx.Done():
		err = server.Close()
	}
	klog.V(2).Infof("Stop serving on reverse connection")
	return err
}
