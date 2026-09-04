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

package server

import (
	"context"

	"github.com/kcp-dev/kcp/pkg/reconciler/cache/syncer/authoritativeshards"
)

func (s *Server) installControllers(ctx context.Context) error {
	if s.Options.CacheSyncer.Enabled {
		if err := s.installAuthoritativeShardsController(ctx); err != nil {
			return err
		}
	}

	return nil
}

func (s *Server) installAuthoritativeShardsController(ctx context.Context) error {
	c, err := authoritativeshards.NewController()
	if err != nil {
		return err
	}
	go c.Start(ctx, 2)
	return nil
}
