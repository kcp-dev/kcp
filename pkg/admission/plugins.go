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
	"k8s.io/apiserver/pkg/admission/plugin/validatingadmissionpolicy"
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
	"github.com/kcp-dev/kcp/pkg/admission/apiexportendpointslice"
	"github.com/kcp-dev/kcp/pkg/admission/apiresourceschema"
	"github.com/kcp-dev/kcp/pkg/admission/crdnooverlappinggvr"
	"github.com/kcp-dev/kcp/pkg/admission/kubequota"
	kcplimitranger "github.com/kcp-dev/kcp/pkg/admission/limitranger"
	"github.com/kcp-dev/kcp/pkg/admission/logicalcluster"
	"github.com/kcp-dev/kcp/pkg/admission/logicalclusterfinalizer"
	kcpmutatingwebhook "github.com/kcp-dev/kcp/pkg/admission/mutatingwebhook"
	workspacenamespacelifecycle "github.com/kcp-dev/kcp/pkg/admission/namespacelifecycle"
	"github.com/kcp-dev/kcp/pkg/admission/pathannotation"
	"github.com/kcp-dev/kcp/pkg/admission/permissionclaims"
	"github.com/kcp-dev/kcp/pkg/admission/reservedcrdannotations"
	"github.com/kcp-dev/kcp/pkg/admission/reservedcrdgroups"
	"github.com/kcp-dev/kcp/pkg/admission/reservedmetadata"
	"github.com/kcp-dev/kcp/pkg/admission/reservednames"
	"github.com/kcp-dev/kcp/pkg/admission/shard"
	kcpvalidatingadmissionpolicy "github.com/kcp-dev/kcp/pkg/admission/validatingadmissionpolicy"
	kcpvalidatingwebhook "github.com/kcp-dev/kcp/pkg/admission/validatingwebhook"
	"github.com/kcp-dev/kcp/pkg/admission/workspace"
	"github.com/kcp-dev/kcp/pkg/admission/workspacetype"
	"github.com/kcp-dev/kcp/pkg/admission/workspacetypeexists"
)

// AllOrderedPlugins is the list of all the plugins in order.
var AllOrderedPlugins = beforeWebhooks(kubeapiserveroptions.AllOrderedPlugins,
	workspacenamespacelifecycle.PluginName,
	apiresourceschema.PluginName,
	workspace.PluginName,
	logicalclusterfinalizer.PluginName,
	shard.PluginName,
	workspacetype.PluginName,
	workspacetypeexists.PluginName,
	logicalcluster.PluginName,
	apiexport.PluginName,
	apibinding.PluginName,
	apibindingfinalizer.PluginName,
	apiexportendpointslice.PluginName,
	kcpmutatingwebhook.PluginName,
	kcpvalidatingadmissionpolicy.PluginName,
	kcpvalidatingwebhook.PluginName,
	kcplimitranger.PluginName,
	reservedcrdannotations.PluginName,
	reservedcrdgroups.PluginName,
	reservednames.PluginName,
	crdnooverlappinggvr.PluginName,
	reservedmetadata.PluginName,
	permissionclaims.PluginName,
	pathannotation.PluginName,
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
	workspace.Register(plugins)
	logicalclusterfinalizer.Register(plugins)
	shard.Register(plugins)
	workspacetype.Register(plugins)
	workspacetypeexists.Register(plugins)
	logicalcluster.Register(plugins)
	apiresourceschema.Register(plugins)
	apiexport.Register(plugins)
	apibinding.Register(plugins)
	apibindingfinalizer.Register(plugins)
	apiexportendpointslice.Register(plugins)
	workspacenamespacelifecycle.Register(plugins)
	kcpmutatingwebhook.Register(plugins)
	kcpvalidatingadmissionpolicy.Register(plugins)
	kcpvalidatingwebhook.Register(plugins)
	kcplimitranger.Register(plugins)
	reservedcrdannotations.Register(plugins)
	reservedcrdgroups.Register(plugins)
	reservednames.Register(plugins)
	crdnooverlappinggvr.Register(plugins)
	reservedmetadata.Register(plugins)
	permissionclaims.Register(plugins)
	pathannotation.Register(plugins)
	kubequota.Register(plugins)
}

var defaultOnPluginsInKcp = sets.New[string](
	workspacenamespacelifecycle.PluginName, // WorkspaceNamespaceLifecycle
	kcplimitranger.PluginName,              // WorkspaceLimitRanger
	certapproval.PluginName,                // CertificateApproval
	certsigning.PluginName,                 // CertificateSigning
	certsubjectrestriction.PluginName,      // CertificateSubjectRestriction

	// KCP
	workspace.PluginName,
	logicalclusterfinalizer.PluginName,
	shard.PluginName,
	workspacetype.PluginName,
	workspacetypeexists.PluginName,
	logicalcluster.PluginName,
	apiresourceschema.PluginName,
	apiexport.PluginName,
	apibinding.PluginName,
	apibindingfinalizer.PluginName,
	apiexportendpointslice.PluginName,
	kcpmutatingwebhook.PluginName,
	kcpvalidatingadmissionpolicy.PluginName,
	kcpvalidatingwebhook.PluginName,
	reservedcrdannotations.PluginName,
	reservedcrdgroups.PluginName,
	reservednames.PluginName,
	permissionclaims.PluginName,
	pathannotation.PluginName,
	kubequota.PluginName,
)

// defaultOnKubePluginsInKube is a copy of kubeapiserveroptions.defaultOnKubePlugins.
// Always keep this in sync with upstream. It is meant to detect during rebase which
// new plugins got added upstream and to react (enable or disable by default). We
// have a unit test in place to avoid drift.
var defaultOnKubePluginsInKube = sets.New[string](
	lifecycle.PluginName,                    // NamespaceLifecycle
	limitranger.PluginName,                  // LimitRanger
	serviceaccount.PluginName,               // ServiceAccount
	setdefault.PluginName,                   // DefaultStorageClass
	resize.PluginName,                       // PersistentVolumeClaimResize
	defaulttolerationseconds.PluginName,     // DefaultTolerationSeconds
	mutatingwebhook.PluginName,              // MutatingAdmissionWebhook
	validatingwebhook.PluginName,            // ValidatingAdmissionWebhook
	validatingadmissionpolicy.PluginName,    // ValidatingAdmissionPolicy
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
func DefaultOffAdmissionPlugins() sets.Set[string] {
	return sets.New[string](AllOrderedPlugins...).Difference(defaultOnPluginsInKcp)
}
