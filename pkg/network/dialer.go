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
	"net"
)

type DialContext func(ctx context.Context, network, address string) (net.Conn, error)

// DefaultDialContext returns a DialContext function from a network dialer with default options sets.
func DefaultDialContext() DialContext {
	return dialerWithDefaultOptions()
}
