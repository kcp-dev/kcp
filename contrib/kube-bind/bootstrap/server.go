package bootstrap

import (
	"context"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	bootstrapconfig "github.com/kcp-dev/kcp/contrib/kube-bind/bootstrap/config/config"
	bootstrapcore "github.com/kcp-dev/kcp/contrib/kube-bind/bootstrap/config/core"
	bootstrapkubebind "github.com/kcp-dev/kcp/contrib/kube-bind/bootstrap/config/kube-bind"
)

type Server struct {
	Config *Config
}

func NewServer(ctx context.Context, config *Config) (*Server, error) {
	s := &Server{
		Config: config,
	}

	return s, nil
}

func (s *Server) Start(ctx context.Context) error {
	fakeBatteries := sets.New("")
	logger := klog.FromContext(ctx)

	if err := bootstrapconfig.Bootstrap(
		ctx,
		s.Config.KcpClusterClient,
		s.Config.ApiextensionsClient,
		s.Config.DynamicClusterClient,
		fakeBatteries,
	); err != nil {
		logger.Error(err, "failed to bootstrap initial config workspace")
		return nil // don't klog.Fatal. This only happens when context is cancelled.
	}

	if err := bootstrapcore.Bootstrap(
		ctx,
		s.Config.KcpClusterClient,
		s.Config.ApiextensionsClient,
		s.Config.DynamicClusterClient,
		fakeBatteries,
	); err != nil {
		logger.Error(err, "failed to bootstrap core workspace")
		return nil // don't klog.Fatal. This only happens when context is cancelled.
	}

	if err := bootstrapkubebind.Bootstrap(
		ctx,
		s.Config.KcpClusterClient,
		s.Config.ApiextensionsClient,
		s.Config.DynamicClusterClient,
		fakeBatteries,
	); err != nil {
		logger.Error(err, "failed to bootstrap core workspace")
		return nil // don't klog.Fatal. This only happens when context is cancelled.
	}

	return nil
}
