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

package coreidentityset

import (
	"errors"
	"net/http"
	"sync"

	"k8s.io/apiserver/pkg/server/healthz"

	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
)

// HealthCheckName is the name of the readiness check that reports the
// state of the shard's core identities.
const HealthCheckName = "core-identities"

// Health is a readiness check that fails while the shard's core identities
// are unresolved, inconsistent, or unconfirmed (see ReadinessError). It
// starts failing and is updated from every published CoreIdentitySet.
type Health struct {
	mu  sync.RWMutex
	err error
}

var _ healthz.HealthChecker = &Health{}

// NewHealth creates a Health that reports not-ready until updated.
func NewHealth() *Health {
	return &Health{err: errors.New("core identities are not resolved yet")}
}

func (h *Health) Name() string { return HealthCheckName }

func (h *Health) Check(_ *http.Request) error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.err
}

// Update derives the readiness from the given CoreIdentitySet.
func (h *Health) Update(set *adminv1alpha1.CoreIdentitySet) {
	h.Set(ReadinessError(set))
}

// Set sets the readiness error, nil meaning ready.
func (h *Health) Set(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.err = err
}
