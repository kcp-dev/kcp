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

package framework

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/martinlindhe/base36"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
)

type ArtifactFunc func(*testing.T, func() (runtime.Object, error))

// StaticTokenUserConfig returns a user config based on static user tokens defined in "test/e2e/framework/auth-tokens.csv".
// The token being used is "[username]-token".
func StaticTokenUserConfig(username string, cfg *rest.Config) *rest.Config {
	return ConfigWithToken(username+"-token", cfg)
}

// ConfigWithToken returns a copy of the given rest.Config with the given token set.
func ConfigWithToken(token string, cfg *rest.Config) *rest.Config {
	cfgCopy := rest.CopyConfig(cfg)
	cfgCopy.CertData = nil
	cfgCopy.KeyData = nil
	cfgCopy.BearerToken = token
	return cfgCopy
}

// UniqueGroup returns a unique API group with the given suffix by prefixing
// some random 8 character base36 string. suffix must start with a dot if the
// random string should be dot-separated.
func UniqueGroup(suffix string) string {
	ret := strings.ToLower(base36.Encode(rand.Uint64())[:8]) + suffix
	if ret[0] >= '0' && ret[0] <= '9' {
		return "a" + ret[1:]
	}
	return ret
}
