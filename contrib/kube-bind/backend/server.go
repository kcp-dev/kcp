/*
Copyright 2022 The Kube Bind Authors.

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

package backend

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"

	kubebindv1alpha1 "github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha1"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/contrib/kube-bind/controllers/clusterbinding"
	"github.com/kcp-dev/kcp/contrib/kube-bind/controllers/serviceexport"
	"github.com/kcp-dev/kcp/contrib/kube-bind/controllers/serviceexportrequest"
	"github.com/kcp-dev/kcp/contrib/kube-bind/controllers/servicenamespace"
	"github.com/kcp-dev/kcp/contrib/kube-bind/deploy"
	examplehttp "github.com/kcp-dev/kcp/contrib/kube-bind/http"
	examplekube "github.com/kcp-dev/kcp/contrib/kube-bind/kubernetes"
)

type Server struct {
	Config *Config

	OIDC       *examplehttp.OIDCServiceProvider
	Kubernetes *examplekube.Manager
	WebServer  *examplehttp.Server

	Controllers
}

type Controllers struct {
	ClusterBinding       *clusterbinding.Controller
	ServiceNamespace     *servicenamespace.Controller
	ServiceExport        *serviceexport.Controller
	ServiceExportRequest *serviceexportrequest.Controller
}

func NewServer(ctx context.Context, config *Config) (*Server, error) {
	s := &Server{
		Config: config,
	}

	var err error
	s.WebServer, err = examplehttp.NewServer(config.Options.Serve)
	if err != nil {
		return nil, fmt.Errorf("error setting up HTTP Server: %w", err)
	}

	// setup oidc backend
	callback := config.Options.OIDC.CallbackURL
	if callback == "" {
		callback = fmt.Sprintf("http://%s/callback", s.WebServer.Addr().String())
	}
	s.OIDC, err = examplehttp.NewOIDCServiceProvider(
		ctx,
		config.Options.OIDC.IssuerClientID,
		config.Options.OIDC.IssuerClientSecret,
		callback,
		config.Options.OIDC.IssuerURL,
		config.Options.OIDC.OIDCCAFile,
	)
	if err != nil {
		return nil, fmt.Errorf("error setting up OIDC: %w", err)
	}
	s.Kubernetes, err = examplekube.NewKubernetesManager(
		config.Options.NamespacePrefix,
		config.Options.PrettyName,
		config.ClientConfig,
		config.Options.ExternalAddress,
		config.Options.ExternalCA,
		config.Options.TLSExternalServerName,
		config.KubeInformers.Core().V1().Namespaces(),
		config.BindInformers,
	)
	if err != nil {
		return nil, fmt.Errorf("error setting up Kubernetes Manager: %w", err)
	}

	signingKey, err := base64.StdEncoding.DecodeString(config.Options.Cookie.SigningKey)
	if err != nil {
		return nil, fmt.Errorf("error creating signing key: %w", err)
	}

	var encryptionKey []byte
	if config.Options.Cookie.EncryptionKey != "" {
		var err error
		encryptionKey, err = base64.StdEncoding.DecodeString(config.Options.Cookie.EncryptionKey)
		if err != nil {
			return nil, fmt.Errorf("error creating encryption key: %w", err)
		}
	}

	handler, err := examplehttp.NewHandler(
		s.OIDC,
		config.Options.OIDC.AuthorizeURL,
		callback,
		config.Options.PrettyName,
		config.Options.TestingAutoSelect,
		signingKey,
		encryptionKey,
		kubebindv1alpha1.Scope(config.Options.ConsumerScope),
		s.Kubernetes,
		config.ApiextensionsInformers.Apiextensions().V1().CustomResourceDefinitions().Lister(),
	)
	if err != nil {
		return nil, fmt.Errorf("error setting up HTTP Handler: %w", err)
	}
	handler.AddRoutes(s.WebServer.Router)

	// construct controllers
	s.ClusterBinding, err = clusterbinding.NewController(
		config.ClientConfig,
		kubebindv1alpha1.Scope(config.Options.ConsumerScope),
		config.BindInformers.KubeBind().V1alpha1().ClusterBindings(),
		config.BindInformers.KubeBind().V1alpha1().APIServiceExports(),
		config.KubeInformers.Rbac().V1().ClusterRoles(),
		config.KubeInformers.Rbac().V1().ClusterRoleBindings(),
		config.KubeInformers.Rbac().V1().RoleBindings(),
		config.KubeInformers.Core().V1().Namespaces(),
	)
	if err != nil {
		return nil, fmt.Errorf("error setting up ClusterBinding Controller: %v", err)
	}
	s.ServiceNamespace, err = servicenamespace.NewController(
		config.ClientConfig,
		kubebindv1alpha1.Scope(config.Options.ConsumerScope),
		config.BindInformers.KubeBind().V1alpha1().APIServiceNamespaces(),
		config.BindInformers.KubeBind().V1alpha1().ClusterBindings(),
		config.BindInformers.KubeBind().V1alpha1().APIServiceExports(),
		config.KubeInformers.Core().V1().Namespaces(),
		config.KubeInformers.Rbac().V1().Roles(),
		config.KubeInformers.Rbac().V1().RoleBindings(),
	)
	if err != nil {
		return nil, fmt.Errorf("error setting up APIServiceNamespace Controller: %w", err)
	}
	s.ServiceExport, err = serviceexport.NewController(
		config.ClientConfig,
		config.BindInformers.KubeBind().V1alpha1().APIServiceExports(),
		config.ApiextensionsInformers.Apiextensions().V1().CustomResourceDefinitions(),
	)
	if err != nil {
		return nil, fmt.Errorf("error setting up APIServiceExport Controller: %w", err)
	}
	s.ServiceExportRequest, err = serviceexportrequest.NewController(
		config.ClientConfig,
		kubebindv1alpha1.Scope(config.Options.ConsumerScope),
		config.BindInformers.KubeBind().V1alpha1().APIServiceExportRequests(),
		config.BindInformers.KubeBind().V1alpha1().APIServiceExports(),
		config.ApiextensionsInformers.Apiextensions().V1().CustomResourceDefinitions(),
	)
	if err != nil {
		return nil, fmt.Errorf("error setting up ServiceExportRequest Controller: %w", err)
	}

	return s, nil
}

func (s *Server) OptionallyStartInformers(ctx context.Context) {
	logger := klog.FromContext(ctx)

	// start informer factories
	logger.Info("starting informers")
	s.Config.KubeInformers.Start(ctx.Done())
	s.Config.BindInformers.Start(ctx.Done())
	s.Config.ApiextensionsInformers.Start(ctx.Done())
	kubeSynced := s.Config.KubeInformers.WaitForCacheSync(ctx.Done())
	kubeBindSynced := s.Config.BindInformers.WaitForCacheSync(ctx.Done())
	apiextensionsSynced := s.Config.ApiextensionsInformers.WaitForCacheSync(ctx.Done())

	logger.Info("local informers are synced",
		"kubeSynced", fmt.Sprintf("%v", kubeSynced),
		"kubeBindSynced", fmt.Sprintf("%v", kubeBindSynced),
		"apiextensionsSynced", fmt.Sprintf("%v", apiextensionsSynced),
	)
}

func (s *Server) Addr() net.Addr {
	return s.WebServer.Addr()
}

func (s *Server) Run(ctx context.Context) error {
	dynamicClient, err := dynamic.NewForConfig(s.Config.ClientConfig)
	if err != nil {
		return err
	}

	if err := deploy.Bootstrap(ctx, s.Config.KubeClient.Discovery(), dynamicClient, sets.NewString()); err != nil {
		return err
	}

	// start controllers
	go s.Controllers.ServiceExport.Start(ctx, 1)
	go s.Controllers.ServiceNamespace.Start(ctx, 1)
	go s.Controllers.ClusterBinding.Start(ctx, 1)
	go s.Controllers.ServiceExportRequest.Start(ctx, 1)

	go func() {
		<-ctx.Done()
	}()
	return s.WebServer.Start(ctx)
}
