/*
Copyright 2023 The KCP Authors.

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

package webhook

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apiserver/pkg/server"
	webhookutil "k8s.io/apiserver/pkg/util/webhook"
	"k8s.io/client-go/rest"
)

type ServiceToURLTransformer struct {
	loopbackClientConfig *rest.Config
}

// SetLoopbackClientConfig implements the WantsLoopbackClientConfig interface.
func (t *ServiceToURLTransformer) SetLoopbackClientConfig(loopbackClientConfig rest.Config) {
	conf := rest.CopyConfig(&loopbackClientConfig)
	_ = rest.LoadTLSFiles(conf)
	t.loopbackClientConfig = conf
}

func (t *ServiceToURLTransformer) TransformMutatingWebhookConfiguration(i interface{}) (interface{}, error) {
	conf := i.(*admissionregistrationv1.MutatingWebhookConfiguration)
	conf = conf.DeepCopy()
	clusterName := logicalcluster.From(conf)
	for i := range conf.Webhooks {
		if err := t.transformClientConfig(clusterName, &conf.Webhooks[i].ClientConfig); err != nil {
			return nil, err
		}
	}

	return conf, nil
}

func (t *ServiceToURLTransformer) TransformValidatingWebhookConfiguration(i interface{}) (interface{}, error) {
	conf := i.(*admissionregistrationv1.ValidatingWebhookConfiguration)
	conf = conf.DeepCopy()
	clusterName := logicalcluster.From(conf)
	for i := range conf.Webhooks {
		if err := t.transformClientConfig(clusterName, &conf.Webhooks[i].ClientConfig); err != nil {
			return nil, err
		}
	}

	return conf, nil
}

func (t *ServiceToURLTransformer) transformClientConfig(clusterName logicalcluster.Name, clientConfig *admissionregistrationv1.WebhookClientConfig) error {
	if clientConfig.URL != nil && strings.HasPrefix(*clientConfig.URL, t.loopbackClientConfig.Host) {
		return errors.New("invalid webhook url")
	}
	if clientConfig.Service != nil {
		namespace := clientConfig.Service.Namespace
		name := clientConfig.Service.Name
		port := int32(443)
		if clientConfig.Service.Port != nil {
			port = *clientConfig.Service.Port
		}
		servicePath := ""
		if clientConfig.Service.Path != nil {
			servicePath = *clientConfig.Service.Path
		}

		proxyPath, err := url.JoinPath(clusterName.Path().RequestPath(), "api/v1/namespaces", namespace, "services", fmt.Sprintf("https:%s:%d", name, port), "proxy", servicePath)
		if err != nil {
			return err
		}

		configURL, err := url.Parse(t.loopbackClientConfig.Host)
		if err != nil {
			return err
		}

		configURL.Path = proxyPath
		configURLString := configURL.String()
		clientConfig.URL = &configURLString
		clientConfig.Service = nil
	}

	return nil
}

func (t *ServiceToURLTransformer) WrapAuthenticationInforesolver(air webhookutil.AuthenticationInfoResolver) webhookutil.AuthenticationInfoResolver {
	return &webhookutil.AuthenticationInfoResolverDelegator{
		ClientConfigForFunc: func(hostPort string) (*rest.Config, error) {
			cfg, err := air.ClientConfigFor(hostPort)
			if err != nil {
				return nil, err
			}
			if t.loopbackClientConfig.Host == "https://"+hostPort {
				// Combine CAData from the config with any existing CA bundle provided
				if len(cfg.TLSClientConfig.CAData) > 0 {
					cfg.TLSClientConfig.CAData = append(cfg.TLSClientConfig.CAData, '\n')
				}
				cfg.TLSClientConfig.CAData = append(cfg.TLSClientConfig.CAData, t.loopbackClientConfig.CAData...)
				cfg.TLSClientConfig.ServerName = server.LoopbackClientServerNameOverride
				cfg.BearerToken = t.loopbackClientConfig.BearerToken
			}

			return cfg, nil
		},
		ClientConfigForServiceFunc: air.ClientConfigForService,
	}
}
