/*
Copyright 2026 The kcp Authors.

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

// Package mounts holds helpers shared by the components that handle
// workspace mounts: admission, the mounts controller, the index and the
// proxies that forward requests to a mount target.
package mounts

import (
	"errors"
	"fmt"
	"net/url"
)

// ValidateURL parses raw as the target of a workspace mount and returns the
// parsed URL.
//
// A mount target receives the identity of every caller that enters the
// mounted workspace, and kcp connects to it from the shard's network position.
// Only absolute https URLs with a host and without user info, query or
// fragment are therefore accepted. TLS verification against the target is
// mandatory and not part of this check; it is enforced by the transport.
func ValidateURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, errors.New("must not be empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("scheme must be https, got %q", u.Scheme)
	}
	if u.Opaque != "" {
		return nil, errors.New("must be an absolute URL")
	}
	if u.Hostname() == "" {
		return nil, errors.New("host must be set")
	}
	if u.User != nil {
		return nil, errors.New("must not contain user info")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return nil, errors.New("must not contain a query")
	}
	if u.Fragment != "" || u.RawFragment != "" {
		return nil, errors.New("must not contain a fragment")
	}
	return u, nil
}
