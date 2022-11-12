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

package admission

import (
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/plugin/namespace/lifecycle"
	"k8s.io/apiserver/pkg/admission/plugin/resourcequota"
	mutatingwebhook "k8s.io/apiserver/pkg/admission/plugin/webhook/mutating"
	validatingwebhook "k8s.io/apiserver/pkg/admission/plugin/webhook/validating"
	kubeapiserveroptions "k8s.io/kubernetes/pkg/kubeapiserver/options"
	certapproval "k8s.io/kubernetes/plugin/pkg/admission/certificates/approval"
	certsigning "k8s.io/kubernetes/plugin/pkg/admission/certificates/signing"
	certsubjectrestriction "k8s.io/kubernetes/plugin/pkg/admission/certificates/subjectrestriction"
	"k8s.io/kubernetes/plugin/pkg/admission/defaulttolerationseconds"
	"k8s.io/kubernetes/plugin/pkg/admission/limitranger"
	"k8s.io/kubernetes/plugin/pkg/admission/network/defaultingressclass"
	"k8s.io/kubernetes/plugin/pkg/admission/nodetaint"
	podpriority "k8s.io/kubernetes/plugin/pkg/admission/priority"
	"k8s.io/kubernetes/plugin/pkg/admission/runtimeclass"
	"k8s.io/kubernetes/plugin/pkg/admission/security/podsecurity"
	"k8s.io/kubernetes/plugin/pkg/admission/serviceaccount"
	"k8s.io/kubernetes/plugin/pkg/admission/storage/persistentvolume/resize"
	"k8s.io/kubernetes/plugin/pkg/admission/storage/storageclass/setdefault"
	"k8s.io/kubernetes/plugin/pkg/admission/storage/storageobjectinuseprotection"

	"github.com/kcp-dev/kcp/pkg/admission/apibinding"
	"github.com/kcp-dev/kcp/pkg/admission/apibindingfinalizer"
	"github.com/kcp-dev/kcp/pkg/admission/apiexport"
	"github.com/kcp-dev/kcp/pkg/admission/apiresourceschema"
	"github.com/kcp-dev/kcp/pkg/admission/clusterworkspace"
	"github.com/kcp-dev/kcp/pkg/admission/thisworkspacefinalizer"
	"github.com/kcp-dev/kcp/pkg/admission/clusterworkspaceshard"
	"github.com/kcp-dev/kcp/pkg/admission/clusterworkspacetype"
	"github.com/kcp-dev/kcp/pkg/admission/clusterworkspacetypeexists"
	"github.com/kcp-dev/kcp/pkg/admission/crdnooverlappinggvr"
	"github.com/kcp-dev/kcp/pkg/admission/kubequota"
	kcplimitranger "github.com/kcp-dev/kcp/pkg/admission/limitranger"
	kcpmutatingwebhook "github.com/kcp-dev/kcp/pkg/admission/mutatingwebhook"
	workspacenamespacelifecycle "github.com/kcp-dev/kcp/pkg/admission/namespacelifecycle"
	"github.com/kcp-dev/kcp/pkg/admission/permissionclaims"
	"github.com/kcp-dev/kcp/pkg/admission/reservedcrdannotations"
	"github.com/kcp-dev/kcp/pkg/admission/reservedcrdgroups"
	"github.com/kcp-dev/kcp/pkg/admission/reservedmetadata"
	"github.com/kcp-dev/kcp/pkg/admission/reservednames"
	"github.com/kcp-dev/kcp/pkg/admission/thisworkspace"
	kcpvalidatingwebhook "github.com/kcp-dev/kcp/pkg/admission/validatingwebhook"
)

// AllOrderedPlugins is the list of all the plugins in order.
var AllOrderedPlugins = beforeWebhooks(kubeapiserveroptions.AllOrderedPlugins,
	workspacenamespacelifecycle.PluginName,
	apiresourceschema.PluginName,
	clusterworkspace.PluginName,
	thisworkspacefinalizer.PluginName,
	clusterworkspaceshard.PluginName,
	clusterworkspacetype.PluginName,
	clusterworkspacetypeexists.PluginName,
	thisworkspace.PluginName,
	apiexport.PluginName,
	apibinding.PluginName,
	apibindingfinalizer.PluginName,
	kcpvalidatingwebhook.PluginName,
	kcpmutatingwebhook.PluginName,
	kcplimitranger.PluginName,
	reservedcrdannotations.PluginName,
	reservedcrdgroups.PluginName,
	reservednames.PluginName,
	crdnooverlappinggvr.PluginName,
	reservedmetadata.PluginName,
	permissionclaims.PluginName,
	kubequota.PluginName,
)

func beforeWebhooks(recommended []string, plugins ...string) []string {
	ret := make([]string, 0, len(recommended)+len(plugins))
	for _, plugin := range recommended {
		if plugin == mutatingwebhook.PluginName {
			ret = append(ret, plugins...)
		}
		ret = append(ret, plugin)
	}
	return ret
}

// RegisterAllKcpAdmissionPlugins registers all admission plugins.
// The order of registration is irrelevant, see AllOrderedPlugins for execution order.
func RegisterAllKcpAdmissionPlugins(plugins *admission.Plugins) {
	kubeapiserveroptions.RegisterAllAdmissionPlugins(plugins)
	clusterworkspace.Register(plugins)
	thisworkspacefinalizer.Register(plugins)
	clusterworkspaceshard.Register(plugins)
	clusterworkspacetype.Register(plugins)
	clusterworkspacetypeexists.Register(plugins)
	thisworkspace.Register(plugins)
	apiresourceschema.Register(plugins)
	apiexport.Register(plugins)
	apibinding.Register(plugins)
	apibindingfinalizer.Register(plugins)
	workspacenamespacelifecycle.Register(plugins)
	kcpvalidatingwebhook.Register(plugins)
	kcpmutatingwebhook.Register(plugins)
	kcplimitranger.Register(plugins)
	reservedcrdannotations.Register(plugins)
	reservedcrdgroups.Register(plugins)
	reservednames.Register(plugins)
	crdnooverlappinggvr.Register(plugins)
	reservedmetadata.Register(plugins)
	permissionclaims.Register(plugins)
	kubequota.Register(plugins)
}

var defaultOnPluginsInKcp = sets.NewString(
	workspacenamespacelifecycle.PluginName, // WorkspaceNamespaceLifecycle
	kcplimitranger.PluginName,              // WorkspaceLimitRanger
	certapproval.PluginName,                // CertificateApproval
	certsigning.PluginName,                 // CertificateSigning
	certsubjectrestriction.PluginName,      // CertificateSubjectRestriction

	// KCP
	clusterworkspace.PluginName,
	thisworkspacefinalizer.PluginName,
	clusterworkspaceshard.PluginName,
	clusterworkspacetype.PluginName,
	clusterworkspacetypeexists.PluginName,
	thisworkspace.PluginName,
	apiresourceschema.PluginName,
	apiexport.PluginName,
	apibinding.PluginName,
	apibindingfinalizer.PluginName,
	kcpvalidatingwebhook.PluginName,
	kcpmutatingwebhook.PluginName,
	reservedcrdannotations.PluginName,
	reservedcrdgroups.PluginName,
	reservednames.PluginName,
	permissionclaims.PluginName,
	kubequota.PluginName,
)

// defaultOnKubePluginsInKube is a copy of kubeapiserveroptions.defaultOnKubePlugins.
// Always keep this in sync with upstream. It is meant to detect during rebase which
// new plugins got added upstream and to react (enable or disable by default). We
// have a unit test in place to avoid drift.
var defaultOnKubePluginsInKube = sets.NewString(
	lifecycle.PluginName,                    // NamespaceLifecycle
	limitranger.PluginName,                  // LimitRanger
	serviceaccount.PluginName,               // ServiceAccount
	setdefault.PluginName,                   // DefaultStorageClass
	resize.PluginName,                       // PersistentVolumeClaimResize
	defaulttolerationseconds.PluginName,     // DefaultTolerationSeconds
	mutatingwebhook.PluginName,              // MutatingAdmissionWebhook
	validatingwebhook.PluginName,            // ValidatingAdmissionWebhook
	resourcequota.PluginName,                // ResourceQuota
	storageobjectinuseprotection.PluginName, // StorageObjectInUseProtection
	podpriority.PluginName,                  // PodPriority
	nodetaint.PluginName,                    // TaintNodesByCondition
	runtimeclass.PluginName,                 // RuntimeClass
	certapproval.PluginName,                 // CertificateApproval
	certsigning.PluginName,                  // CertificateSigning
	certsubjectrestriction.PluginName,       // CertificateSubjectRestriction
	defaultingressclass.PluginName,          // DefaultIngressClass
	podsecurity.PluginName,                  // PodSecurity)
)

// DefaultOffAdmissionPlugins get admission plugins off by default for kcp.
func DefaultOffAdmissionPlugins() sets.String {
	return sets.NewString(AllOrderedPlugins...).Difference(defaultOnPluginsInKcp)
}
