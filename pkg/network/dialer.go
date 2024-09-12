/*
Copyright 2024 The KCP Authors.
Copyright 2020 Red Hat Inc.

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

// Taken from https://github.com/openshift/library-go/pull/926/files.

package network

import (
	"context"
	"fmt"
	"net"
	"net/http"

	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/klog/v2"
)

type DialContext func(ctx context.Context, network, address string) (net.Conn, error)

// DefaultDialContext returns a DialContext function from a network dialer with default options sets.
func DefaultDialContext() DialContext {
	return dialerWithDefaultOptions()
}

// DefaultTransportWrapper wraps the provided roundtripper with default settings.
func DefaultTransportWrapper(rt http.RoundTripper) http.RoundTripper {
	tr, err := transportFor(rt)
	if err != nil {
		klog.FromContext(context.Background()).Error(err, "Cannot set timeout settings on roundtripper")
		return rt
	}
	tr.DialContext = DefaultDialContext()
	return rt
}

func transportFor(rt http.RoundTripper) (*http.Transport, error) {
	if rt == nil {
		return nil, nil
	}

	switch transport := rt.(type) {
	case *http.Transport:
		return transport, nil
	case utilnet.RoundTripperWrapper:
		return transportFor(transport.WrappedRoundTripper())
	default:
		return nil, fmt.Errorf("unknown transport type, expected *http.Transport, got: %T", rt)
	}
}
