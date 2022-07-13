module github.com/kcp-dev/kcp

go 1.16

require (
	github.com/MakeNowJust/heredoc v1.0.0
	github.com/abiosoft/lineprefix v0.1.4
	github.com/bombsimon/logrusr/v3 v3.0.0
	github.com/egymgmbh/go-prefix-writer v0.0.0-20180609083313-7326ea162eca
	github.com/emicklei/go-restful v2.9.5+incompatible
	github.com/envoyproxy/go-control-plane v0.10.1
	github.com/evanphx/json-patch v5.6.0+incompatible
	github.com/fatih/color v1.12.0
	github.com/go-logr/logr v1.2.3
	github.com/google/go-cmp v0.5.6
	github.com/google/uuid v1.1.2
	github.com/googleapis/gnostic v0.5.5
	github.com/kcp-dev/apimachinery v0.0.0-20220708220956-c302aeddfde7
	github.com/kcp-dev/kcp/pkg/apis v0.0.0-00010101000000-000000000000
	github.com/kcp-dev/logicalcluster v1.1.1-0.20220705215104-8e46328c24a5
	github.com/martinlindhe/base36 v1.1.1
	github.com/muesli/reflow v0.1.0
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822
	github.com/sirupsen/logrus v1.8.1
	github.com/spf13/cobra v1.2.1
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.7.1
	go.etcd.io/etcd/client/pkg/v3 v3.5.0
	go.etcd.io/etcd/server/v3 v3.5.0
	go.uber.org/multierr v1.7.0
	google.golang.org/grpc v1.40.0
	google.golang.org/protobuf v1.27.1
	gopkg.in/square/go-jose.v2 v2.2.2
	k8s.io/api v0.23.5
	k8s.io/apiextensions-apiserver v0.23.5
	k8s.io/apimachinery v0.23.5
	k8s.io/apiserver v0.23.5
	k8s.io/cli-runtime v0.23.5
	k8s.io/client-go v0.23.5
	k8s.io/code-generator v0.23.5
	k8s.io/component-base v0.23.5
	k8s.io/klog/v2 v2.30.0
	k8s.io/kube-openapi v0.0.0-20211115234752-e816edb12b65
	k8s.io/kubernetes v1.23.5
	k8s.io/utils v0.0.0-20211116205334-6203023598ed
	sigs.k8s.io/structured-merge-diff/v4 v4.2.1
	sigs.k8s.io/yaml v1.2.0
)

replace (
	github.com/kcp-dev/kcp/pkg/apis => ./pkg/apis
	k8s.io/api => github.com/kcp-dev/kubernetes/staging/src/k8s.io/api v0.0.0-20220705085034-005ea1354ed5
	k8s.io/apiextensions-apiserver => github.com/kcp-dev/kubernetes/staging/src/k8s.io/apiextensions-apiserver v0.0.0-20220705085034-005ea1354ed5
	k8s.io/apimachinery => github.com/kcp-dev/kubernetes/staging/src/k8s.io/apimachinery v0.0.0-20220705085034-005ea1354ed5
	k8s.io/apiserver => github.com/kcp-dev/kubernetes/staging/src/k8s.io/apiserver v0.0.0-20220705085034-005ea1354ed5
	k8s.io/cli-runtime => github.com/kcp-dev/kubernetes/staging/src/k8s.io/cli-runtime v0.0.0-20220705085034-005ea1354ed5
	k8s.io/client-go => github.com/kcp-dev/kubernetes/staging/src/k8s.io/client-go v0.0.0-20220705085034-005ea1354ed5
	k8s.io/cloud-provider => github.com/kcp-dev/kubernetes/staging/src/k8s.io/cloud-provider v0.0.0-20220705085034-005ea1354ed5
	k8s.io/cluster-bootstrap => github.com/kcp-dev/kubernetes/staging/src/k8s.io/cluster-bootstrap v0.0.0-20220705085034-005ea1354ed5
	k8s.io/code-generator => github.com/kcp-dev/kubernetes/staging/src/k8s.io/code-generator v0.0.0-20220705085034-005ea1354ed5
	k8s.io/component-base => github.com/kcp-dev/kubernetes/staging/src/k8s.io/component-base v0.0.0-20220705085034-005ea1354ed5
	k8s.io/component-helpers => github.com/kcp-dev/kubernetes/staging/src/k8s.io/component-helpers v0.0.0-20220705085034-005ea1354ed5
	k8s.io/controller-manager => github.com/kcp-dev/kubernetes/staging/src/k8s.io/controller-manager v0.0.0-20220705085034-005ea1354ed5
	k8s.io/cri-api => github.com/kcp-dev/kubernetes/staging/src/k8s.io/cri-api v0.0.0-20220705085034-005ea1354ed5
	k8s.io/csi-translation-lib => github.com/kcp-dev/kubernetes/staging/src/k8s.io/csi-translation-lib v0.0.0-20220705085034-005ea1354ed5
	k8s.io/kube-aggregator => github.com/kcp-dev/kubernetes/staging/src/k8s.io/kube-aggregator v0.0.0-20220705085034-005ea1354ed5
	k8s.io/kube-controller-manager => github.com/kcp-dev/kubernetes/staging/src/k8s.io/kube-controller-manager v0.0.0-20220705085034-005ea1354ed5
	k8s.io/kube-proxy => github.com/kcp-dev/kubernetes/staging/src/k8s.io/kube-proxy v0.0.0-20220705085034-005ea1354ed5
	k8s.io/kube-scheduler => github.com/kcp-dev/kubernetes/staging/src/k8s.io/kube-scheduler v0.0.0-20220705085034-005ea1354ed5
	k8s.io/kubectl => github.com/kcp-dev/kubernetes/staging/src/k8s.io/kubectl v0.0.0-20220705085034-005ea1354ed5
	k8s.io/kubelet => github.com/kcp-dev/kubernetes/staging/src/k8s.io/kubelet v0.0.0-20220705085034-005ea1354ed5
	k8s.io/kubernetes => github.com/kcp-dev/kubernetes v0.0.0-20220705085034-005ea1354ed5
	k8s.io/legacy-cloud-providers => github.com/kcp-dev/kubernetes/staging/src/k8s.io/legacy-cloud-providers v0.0.0-20220705085034-005ea1354ed5
	k8s.io/metrics => github.com/kcp-dev/kubernetes/staging/src/k8s.io/metrics v0.0.0-20220705085034-005ea1354ed5
	k8s.io/mount-utils => github.com/kcp-dev/kubernetes/staging/src/k8s.io/mount-utils v0.0.0-20220705085034-005ea1354ed5
	k8s.io/pod-security-admission => github.com/kcp-dev/kubernetes/staging/src/k8s.io/pod-security-admission v0.0.0-20220705085034-005ea1354ed5
	k8s.io/sample-apiserver => github.com/kcp-dev/kubernetes/staging/src/k8s.io/sample-apiserver v0.0.0-20220705085034-005ea1354ed5
)
