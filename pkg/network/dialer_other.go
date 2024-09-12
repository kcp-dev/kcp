//go:build !linux
// +build !linux

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
	"time"

	"k8s.io/klog/v2"
)

func dialerWithDefaultOptions() DialContext {
	klog.FromContext(context.Background()).V(2).Info("Creating the default network Dialer (unsupported platform). It may take up to 15 minutes to detect broken connections and establish a new one")
	nd := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return nd.DialContext
}
