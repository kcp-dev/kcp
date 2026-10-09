/*
Copyright 2025 The kcp Authors.

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

package options

import (
	"github.com/spf13/pflag"

	synceroptions "github.com/kcp-dev/kcp/pkg/cache/syncer/options"
)

// CacheSyncer holds the embedded cache-syncer options for the cache-server.
type CacheSyncer struct {
	Syncer  synceroptions.Options
	Enabled bool
}

func NewCacheSyncer() *CacheSyncer {
	return &CacheSyncer{Enabled: false}
}

func (s *CacheSyncer) AddFlags(fs *pflag.FlagSet) {
	s.Syncer.AddFlags(fs, "cache-syncer-")
	fs.BoolVar(&s.Enabled, "run-cache-syncer", s.Enabled,
		"Run the cache-syncer in-process alongside the cache-server.")
}

// Validate checks that the required TLS flags are present when the syncer is enabled.
// Returns nil if the syncer is disabled.
func (s *CacheSyncer) Validate() []error {
	if !s.Enabled {
		return nil
	}
	return s.Syncer.Validate("cache-syncer-")
}
