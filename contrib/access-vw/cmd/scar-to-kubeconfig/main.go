/*
Copyright 2026 The kcp Authors.

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

// Command scar-to-kubeconfig calls the SCAR endpoint with a bearer
// token and writes a kubeconfig with one context per authorized cluster.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

type scarResponse struct {
	Status struct {
		Clusters []struct {
			ClusterName string `json:"clusterName"`
			Endpoint    string `json:"endpoint"`
		} `json:"clusters"`
	} `json:"status"`
}

func main() {
	scarURL := flag.String("scar-url", "https://localhost:9443/services/access/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews", "SCAR endpoint URL")
	token := flag.String("token", "", "Bearer token (required)")
	output := flag.String("output", "scar.kubeconfig", "Output kubeconfig path")
	insecure := flag.Bool("insecure", false, "Skip TLS verification for cluster endpoints")
	flag.Parse()

	if *token == "" {
		log.Fatal("error: -token is required")
	}

	clusters, err := callSCAR(context.Background(), *scarURL, *token, *insecure)
	if err != nil {
		log.Fatalf("SCAR call failed: %v", err)
	}
	if len(clusters) == 0 {
		log.Fatal("SCAR returned no clusters for this identity (check RBAC bindings)")
	}

	kubeconfig := buildKubeconfig(clusters, *token, *insecure)

	data, err := clientcmd.Write(*kubeconfig)
	if err != nil {
		log.Fatalf("serialize kubeconfig: %v", err)
	}

	if err := os.WriteFile(*output, data, 0600); err != nil {
		log.Fatalf("write %s: %v", *output, err)
	}

	fmt.Fprintf(os.Stderr, "wrote %s with %d cluster context(s)\n", *output, len(clusters))
	fmt.Fprintf(os.Stderr, "next: kubernetes-mcp-server --kubeconfig=%s --cluster-provider=kcp\n", *output)
}

func callSCAR(ctx context.Context, scarURL, token string, insecure bool) ([]scarCluster, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	if insecure {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, scarURL, strings.NewReader("{}"))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", scarURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("SCAR returned %d: %s", resp.StatusCode, string(body))
	}

	var scar scarResponse
	if err := json.Unmarshal(body, &scar); err != nil {
		return nil, fmt.Errorf("decode SCAR response: %w", err)
	}

	out := make([]scarCluster, len(scar.Status.Clusters))
	for i, c := range scar.Status.Clusters {
		out[i] = scarCluster{Name: c.ClusterName, Endpoint: c.Endpoint}
	}
	return out, nil
}

type scarCluster struct {
	Name     string
	Endpoint string
}

func buildKubeconfig(clusters []scarCluster, token string, insecure bool) *clientcmdapi.Config {
	config := clientcmdapi.NewConfig()

	for _, c := range clusters {
		cluster := clientcmdapi.NewCluster()
		cluster.Server = c.Endpoint
		if insecure {
			cluster.InsecureSkipTLSVerify = true
		}
		config.Clusters[c.Name] = cluster

		ctx := clientcmdapi.NewContext()
		ctx.Cluster = c.Name
		ctx.AuthInfo = "scar-user"
		config.Contexts[c.Name] = ctx
	}

	user := clientcmdapi.NewAuthInfo()
	user.Token = token
	config.AuthInfos["scar-user"] = user

	config.CurrentContext = clusters[0].Name

	return config
}
