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

package embeddedetcd

import (
	"context"
	"fmt"
	"time"

	"go.etcd.io/etcd/server/v3/embed"

	"k8s.io/klog/v2"
)

type Server struct {
	config CompletedConfig
}

func NewServer(config CompletedConfig) *Server {
	if config.Config == nil {
		return nil
	}
	return &Server{
		config: config,
	}
}

// Run starts the embedded etcd server. It blocks until it is ready for up to a minute.
func (s *Server) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.Info("Starting embedded etcd server")
	e, err := embed.StartEtcd(s.config.Config.Config)
	if err != nil {
		return err
	}
	// Shutdown when context is closed
	go func() {
		<-ctx.Done()
		e.Close()
	}()

	select {
	case <-e.Server.ReadyNotify():
		return nil
	case <-time.After(60 * time.Second):
		e.Server.Stop() // trigger a shutdown
		return fmt.Errorf("server took too long to start")
	case e := <-e.Err():
		return e
	}
}
