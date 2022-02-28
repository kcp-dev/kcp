module github.com/kcp-dev/kcp

go 1.16

require (
	github.com/MakeNowJust/heredoc v1.0.0
	github.com/egymgmbh/go-prefix-writer v0.0.0-20180609083313-7326ea162eca
	github.com/emicklei/go-restful v2.9.5+incompatible
	github.com/envoyproxy/go-control-plane v0.10.1
	github.com/evanphx/json-patch v5.6.0+incompatible
	github.com/google/go-cmp v0.5.6
	github.com/google/uuid v1.1.2
	github.com/googleapis/gnostic v0.5.5
	github.com/muesli/reflow v0.1.0
	github.com/onsi/gomega v1.10.1
	github.com/spf13/cobra v1.2.1
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.7.0
	github.com/wayneashleyberry/terminal-dimensions v1.0.0
	go.etcd.io/etcd/server/v3 v3.5.0
	go.uber.org/multierr v1.7.0
	google.golang.org/grpc v1.40.0
	google.golang.org/protobuf v1.27.1
	k8s.io/api v0.0.0
	k8s.io/apiextensions-apiserver v0.0.0
	k8s.io/apimachinery v0.0.0
	k8s.io/apiserver v0.0.0
	k8s.io/cli-runtime v0.0.0
	k8s.io/client-go v0.0.0
	k8s.io/code-generator v0.0.0
	k8s.io/component-base v0.0.0
	k8s.io/klog/v2 v2.30.0
	k8s.io/kube-openapi v0.0.0-20211115234752-e816edb12b65
	k8s.io/kubectl v0.0.0
	k8s.io/kubernetes v0.0.0
	k8s.io/utils v0.0.0-20211208161948-7d6a63dca704
	sigs.k8s.io/yaml v1.2.0
)

replace (
	k8s.io/api => github.com/sttts/kubernetes/staging/src/k8s.io/api v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/apiextensions-apiserver => github.com/sttts/kubernetes/staging/src/k8s.io/apiextensions-apiserver v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/apimachinery => github.com/sttts/kubernetes/staging/src/k8s.io/apimachinery v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/apiserver => github.com/sttts/kubernetes/staging/src/k8s.io/apiserver v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/cli-runtime => github.com/sttts/kubernetes/staging/src/k8s.io/cli-runtime v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/client-go => github.com/sttts/kubernetes/staging/src/k8s.io/client-go v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/cloud-provider => github.com/sttts/kubernetes/staging/src/k8s.io/cloud-provider v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/cluster-bootstrap => github.com/sttts/kubernetes/staging/src/k8s.io/cluster-bootstrap v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/code-generator => github.com/sttts/kubernetes/staging/src/k8s.io/code-generator v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/component-base => github.com/sttts/kubernetes/staging/src/k8s.io/component-base v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/component-helpers => github.com/sttts/kubernetes/staging/src/k8s.io/component-helpers v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/controller-manager => github.com/sttts/kubernetes/staging/src/k8s.io/controller-manager v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/cri-api => github.com/sttts/kubernetes/staging/src/k8s.io/cri-api v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/csi-translation-lib => github.com/sttts/kubernetes/staging/src/k8s.io/csi-translation-lib v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/kube-aggregator => github.com/sttts/kubernetes/staging/src/k8s.io/kube-aggregator v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/kube-controller-manager => github.com/sttts/kubernetes/staging/src/k8s.io/kube-controller-manager v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/kube-proxy => github.com/sttts/kubernetes/staging/src/k8s.io/kube-proxy v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/kube-scheduler => github.com/sttts/kubernetes/staging/src/k8s.io/kube-scheduler v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/kubectl => github.com/sttts/kubernetes/staging/src/k8s.io/kubectl v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/kubelet => github.com/sttts/kubernetes/staging/src/k8s.io/kubelet v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/kubernetes => github.com/sttts/kubernetes v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/legacy-cloud-providers => github.com/sttts/kubernetes/staging/src/k8s.io/legacy-cloud-providers v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/metrics => github.com/sttts/kubernetes/staging/src/k8s.io/metrics v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/mount-utils => github.com/sttts/kubernetes/staging/src/k8s.io/mount-utils v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/pod-security-admission => github.com/sttts/kubernetes/staging/src/k8s.io/pod-security-admission v0.0.0-20220217204355-7e3b9b0557ff
	k8s.io/sample-apiserver => github.com/sttts/kubernetes/staging/src/k8s.io/sample-apiserver v0.0.0-20220217204355-7e3b9b0557ff
)
