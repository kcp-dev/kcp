/*
Copyright 2022 The KCP Authors.

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
	"fmt"

	cliflag "k8s.io/component-base/cli/flag"
	_ "k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/genericcontrolplane/options"
)

//
// Mostly copied from k8s.io/kubernetes/cmd/kube-apiserver/app/options
//
// TODO: move all back into genericcontrolplane
//

type ServerRunOptions struct {
	options.ServerRunOptions
}

// Flags returns flags for a specific APIServer by section name
func (s *ServerRunOptions) Flags() (fss cliflag.NamedFlagSets) {
	// TODO: move all these back into genericcontrolplane
	s.GenericServerRunOptions.AddUniversalFlags(fss.FlagSet("generic"))
	s.Etcd.AddFlags(fss.FlagSet("etcd"))
	s.SecureServing.AddFlags(fss.FlagSet("secure serving"))
	s.Audit.AddFlags(fss.FlagSet("auditing"))
	s.Features.AddFlags(fss.FlagSet("features"))
	s.Authentication.AddFlags(fss.FlagSet("authentication"))

	//s.APIEnablement.AddFlags(fss.FlagSet("API enablement"))
	s.EgressSelector.AddFlags(fss.FlagSet("egress selector"))
	s.Admission.AddFlags(fss.FlagSet("admission"))

	s.Metrics.AddFlags(fss.FlagSet("metrics"))
	s.Logs.AddFlags(fss.FlagSet("logs"))
	s.Traces.AddFlags(fss.FlagSet("traces"))

	fs := fss.FlagSet("misc")
	fs.DurationVar(&s.EventTTL, "event-ttl", s.EventTTL,
		"Amount of time to retain events.")

	fs.BoolVar(&s.EnableLogsHandler, "enable-logs-handler", s.EnableLogsHandler,
		"If true, install a /logs handler for the apiserver logs.")
	fs.MarkDeprecated("enable-logs-handler", "This flag will be removed in v1.19") //nolint:golint,errcheck

	fs.Int64Var(&s.MaxConnectionBytesPerSec, "max-connection-bytes-per-sec", s.MaxConnectionBytesPerSec, ""+
		"If non-zero, throttle each user connection to this number of bytes/sec. "+
		"Currently only applies to long-running requests.")

	fs.IntVar(&s.IdentityLeaseDurationSeconds, "identity-lease-duration-seconds", s.IdentityLeaseDurationSeconds,
		"The duration of kube-apiserver lease in seconds, must be a positive number. (In use when the APIServerIdentity feature gate is enabled.)")

	fs.IntVar(&s.IdentityLeaseRenewIntervalSeconds, "identity-lease-renew-interval-seconds", s.IdentityLeaseRenewIntervalSeconds,
		"The interval of kube-apiserver renewing its lease in seconds, must be a positive number. (In use when the APIServerIdentity feature gate is enabled.)")

	fs.StringVar(&s.ProxyClientCertFile, "proxy-client-cert-file", s.ProxyClientCertFile, ""+
		"Client certificate used to prove the identity of the aggregator or kube-apiserver "+
		"when it must call out during a request. This includes proxying requests to a user "+
		"api-server and calling out to webhook admission plugins. It is expected that this "+
		"cert includes a signature from the CA in the --requestheader-client-ca-file flag. "+
		"That CA is published in the 'extension-apiserver-authentication' configmap in "+
		"the kube-system namespace. Components receiving calls from kube-aggregator should "+
		"use that CA to perform their half of the mutual TLS verification.")
	fs.StringVar(&s.ProxyClientKeyFile, "proxy-client-key-file", s.ProxyClientKeyFile, ""+
		"Private key for the client certificate used to prove the identity of the aggregator or kube-apiserver "+
		"when it must call out during a request. This includes proxying requests to a user "+
		"api-server and calling out to webhook admission plugins.")

	return fss
}

func validateAPIServerIdentity(options *ServerRunOptions) []error {
	var errs []error
	if options.IdentityLeaseDurationSeconds <= 0 {
		errs = append(errs, fmt.Errorf("--identity-lease-duration-seconds should be a positive number, but value '%d' provided", options.IdentityLeaseDurationSeconds))
	}
	if options.IdentityLeaseRenewIntervalSeconds <= 0 {
		errs = append(errs, fmt.Errorf("--identity-lease-renew-interval-seconds should be a positive number, but value '%d' provided", options.IdentityLeaseRenewIntervalSeconds))
	}
	return errs
}

// Validate checks Options and return a slice of found errs.
func (s *ServerRunOptions) Validate() []error {
	var errs []error
	errs = append(errs, s.Etcd.Validate()...)
	errs = append(errs, s.SecureServing.Validate()...)
	errs = append(errs, s.Authentication.Validate()...)
	errs = append(errs, s.Audit.Validate()...)
	errs = append(errs, s.Admission.Validate()...)
	//errs = append(errs, s.APIEnablement.Validate(legacyscheme.Scheme, apiextensionsapiserver.Scheme, aggregatorscheme.Scheme)...)
	errs = append(errs, s.Metrics.Validate()...)
	errs = append(errs, s.Logs.Validate()...)
	errs = append(errs, validateAPIServerIdentity(s)...)

	return errs
}
