package mounts

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func getClientFromKubeConfig(kubeconfig []byte) (*kubernetes.Clientset, *rest.Config, error) {
	config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, nil, err
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, err
	}

	return clientset, config, nil
}

// generateSecret generates a random string of specified length
func generateSecret(length int) (string, error) {
	// Allocate a byte slice for the random bytes
	bytes := make([]byte, length)

	// Read random bytes into the slice
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Encode the bytes to a base64 string to make it printable
	return base64.URLEncoding.EncodeToString(bytes)[:length], nil
}
