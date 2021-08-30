module github.com/kcp-dev/kcp

go 1.16

require (
	github.com/MakeNowJust/heredoc v1.0.0
	github.com/google/go-cmp v0.5.6
	github.com/muesli/reflow v0.1.0
	github.com/spf13/cobra v1.1.3
	github.com/spf13/pflag v1.0.5
	github.com/wayneashleyberry/terminal-dimensions v1.0.0
	go.etcd.io/etcd/client/v3 v3.5.0
	go.etcd.io/etcd/server/v3 v3.5.0
	go.uber.org/multierr v1.7.0
	k8s.io/api v0.0.0
	k8s.io/apiextensions-apiserver v0.0.0
	k8s.io/apimachinery v0.0.0
	k8s.io/apiserver v0.0.0
	k8s.io/client-go v0.0.0
	k8s.io/code-generator v0.0.0
	k8s.io/klog v1.0.0
	k8s.io/kube-openapi v0.0.0-20210421082810-95288971da7e
	k8s.io/kubernetes v0.0.0
	sigs.k8s.io/yaml v1.2.0
)

replace (
	k8s.io/api => ../../../k8s.io/kubernetes/staging/src/k8s.io/api
	k8s.io/apiextensions-apiserver => ../../../k8s.io/kubernetes/staging/src/k8s.io/apiextensions-apiserver
	k8s.io/apimachinery => ../../../k8s.io/kubernetes/staging/src/k8s.io/apimachinery
	k8s.io/apiserver => ../../../k8s.io/kubernetes/staging/src/k8s.io/apiserver
	k8s.io/cli-runtime => ../../../k8s.io/kubernetes/staging/src/k8s.io/cli-runtime
	k8s.io/client-go => ../../../k8s.io/kubernetes/staging/src/k8s.io/client-go
	k8s.io/cloud-provider => ../../../k8s.io/kubernetes/staging/src/k8s.io/cloud-provider
	k8s.io/cluster-bootstrap => ../../../k8s.io/kubernetes/staging/src/k8s.io/cluster-bootstrap
	k8s.io/code-generator => ../../../k8s.io/kubernetes/staging/src/k8s.io/code-generator
	k8s.io/component-base => ../../../k8s.io/kubernetes/staging/src/k8s.io/component-base
	k8s.io/component-helpers => ../../../k8s.io/kubernetes/staging/src/k8s.io/component-helpers
	k8s.io/controller-manager => ../../../k8s.io/kubernetes/staging/src/k8s.io/controller-manager
	k8s.io/cri-api => ../../../k8s.io/kubernetes/staging/src/k8s.io/cri-api
	k8s.io/csi-translation-lib => ../../../k8s.io/kubernetes/staging/src/k8s.io/csi-translation-lib
	k8s.io/kube-aggregator => ../../../k8s.io/kubernetes/staging/src/k8s.io/kube-aggregator
	k8s.io/kube-controller-manager => ../../../k8s.io/kubernetes/staging/src/k8s.io/kube-controller-manager
	k8s.io/kube-proxy => ../../../k8s.io/kubernetes/staging/src/k8s.io/kube-proxy
	k8s.io/kube-scheduler => ../../../k8s.io/kubernetes/staging/src/k8s.io/kube-scheduler
	k8s.io/kubectl => ../../../k8s.io/kubernetes/staging/src/k8s.io/kubectl
	k8s.io/kubelet => ../../../k8s.io/kubernetes/staging/src/k8s.io/kubelet
	k8s.io/kubernetes => ../../../k8s.io/kubernetes
	k8s.io/legacy-cloud-providers => ../../../k8s.io/kubernetes/staging/src/k8s.io/legacy-cloud-providers
	k8s.io/metrics => ../../../k8s.io/kubernetes/staging/src/k8s.io/metrics
	k8s.io/mount-utils => ../../../k8s.io/kubernetes/staging/src/k8s.io/mount-utils
	k8s.io/pod-security-admission => ../../../k8s.io/kubernetes/staging/src/k8s.io/pod-security-admission
	k8s.io/sample-apiserver => ../../../k8s.io/kubernetes/staging/src/k8s.io/sample-apiserver
)
