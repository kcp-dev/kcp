package main

import (
	_ "github.com/kcp-dev/kcp/sdk/cmd/apigen"

	_ "k8s.io/code-generator/cmd/applyconfiguration-gen"
	_ "k8s.io/code-generator/cmd/client-gen"
	_ "k8s.io/code-generator/cmd/deepcopy-gen"
	_ "k8s.io/code-generator/cmd/informer-gen"
	_ "k8s.io/code-generator/cmd/lister-gen"
	_ "sigs.k8s.io/controller-tools/cmd/controller-gen"
)
