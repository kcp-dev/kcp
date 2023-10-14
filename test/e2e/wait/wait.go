package wait

import "time"

// ForeverTestTimeout is the timeout used by the KCP test suite. Kubernetes uses 30s,
// but we have a lot of tests that are slow, so we use 60s.
// Ref: k8s.io/apimachinery/pkg/util/wait/wait.go
var ForeverTestTimeout = time.Second * 60
