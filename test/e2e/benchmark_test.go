package e2e

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kcp-dev/kcp/test/e2e/framework"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubernetesclientset "k8s.io/client-go/kubernetes"
)

type adapter struct {
	*testing.B
}

func (a adapter) Deadline() (deadline time.Time, ok bool) {
	return time.Time{}, false
}

func (a adapter) Parallel() {
	panic("invalid call")
}

var _ framework.TestingTInterface = &adapter{}

func BenchmarkMutation(b *testing.B) {
	b.StopTimer()
	start := time.Now()
	b.Log("Starting kcp server...")
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
	}()
	t := &adapter{B: b}
	artifactDir, dataDir, err := framework.ScratchDirs(t)
	if err != nil {
		b.Fatalf("failed to create scratch dirs: %v", err)
	}

	initialized, running, err := framework.NewKcpServer(t, framework.KcpConfig{Name: "main", Args: []string{}}, artifactDir, dataDir)
	if err != nil {
		b.Fatalf("failed to create server: %v", err)
	}

	wg := sync.WaitGroup{}
	wg.Add(1)
	if err := initialized.Run(ctx); err != nil {
		b.Fatalf("failed to run server: %v", err)
	} else {
		go func(s framework.InitializedServer) {
			defer wg.Done()
			if err := s.Ready(); err != nil {
				b.Errorf("kcp server %s never became ready: %v", s.Name(), err)
			}
		}(initialized)
	}
	wg.Wait()

	// if we've failed during startup, don't bother running the benchmark
	if b.Failed() {
		return
	}
	b.Logf("Started kcp server after %s", time.Since(start))

	cfg, err := running.Config()
	if err != nil {
		b.Fatalf("failed to get config for server: %v", err)
	}
	cfg.QPS = math.MaxInt
	cfg.Burst = math.MaxInt
	clusterName := "admin"
	kubeClients, err := kubernetesclientset.NewClusterForConfig(cfg)
	if err != nil {
		b.Fatalf("failed to construct kube client for server: %v", err)
	}
	kubeClient := kubeClients.Cluster(clusterName).CoreV1().Namespaces()

	b.Log("Created first namespace, starting benchmark ...")
	b.ResetTimer()
	b.SetParallelism(10 * runtime.NumCPU())
	b.StartTimer()
	b.RunParallel(func(pb *testing.PB) {
		name := strconv.Itoa(rand.Int())
		for pb.Next() {
			_, err := kubeClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"a": "0"}}}, metav1.CreateOptions{})
			if err != nil {
				b.Errorf("failed to create namespace: %v", err)
				return
			}
			break
		}
		var i int
		for pb.Next() {
			_, err := kubeClient.Patch(ctx, name, types.MergePatchType, []byte(fmt.Sprintf(`{
  "metadata":{
    "labels":{
      "a": %q
    }
  }
}`, strconv.Itoa(i+1))), metav1.PatchOptions{})
			if err != nil {
				b.Errorf("failed to mutate namespace: %v", err)
				return
			}
			i += 1
		}
	})
}
