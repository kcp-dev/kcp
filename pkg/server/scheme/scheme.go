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

package scheme

import (
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	corev1 "k8s.io/kubernetes/pkg/apis/core/v1"

	admissionregistrationinstall "k8s.io/kubernetes/pkg/apis/admissionregistration/install"
	authenticationinstall "k8s.io/kubernetes/pkg/apis/authentication/install"
	authorizationinstall "k8s.io/kubernetes/pkg/apis/authorization/install"
	certificatesinstall "k8s.io/kubernetes/pkg/apis/certificates/install"
	coordinationinstall "k8s.io/kubernetes/pkg/apis/coordination/install"
	eventsinstall "k8s.io/kubernetes/pkg/apis/events/install"
	rbacinstall "k8s.io/kubernetes/pkg/apis/rbac/install"
)

func init() {
	utilruntime.Must(core.AddToGenericControlPlaneScheme(Scheme))
	utilruntime.Must(corev1.AddToControlPlaneScheme(Scheme))
	utilruntime.Must(Scheme.SetVersionPriority(corev1.SchemeGroupVersion))

	authenticationinstall.Install(Scheme)
	authorizationinstall.Install(Scheme)
	certificatesinstall.Install(Scheme)
	coordinationinstall.Install(Scheme)
	rbacinstall.Install(Scheme)
	eventsinstall.Install(Scheme)
	admissionregistrationinstall.Install(Scheme)
}

var (
	// Scheme is the default instance of runtime.Scheme to which control plane types
	// in the Kubernetes API are already registered.
	// NOTE: If you are copying this file to start a new api group, STOP! This Scheme
	// is special and should appear ONLY in the api group, unless you really know what
	// you're doing.
	Scheme = runtime.NewScheme()

	// Codecs provides access to encoding and decoding for the scheme
	Codecs = serializer.NewCodecFactory(Scheme)

	// ParameterCodec handles versioning of objects that are converted to query parameters.
	ParameterCodec = runtime.NewParameterCodec(Scheme)
)
