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

package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"text/template"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/kcp/pkg/cliplugins/helpers"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
)

//go:embed *.tmpl
var embeddedResources embed.FS

func main() {
	ctx := context.Background()
	err := run(ctx)
	if err != nil {
		fmt.Println(err)
	}
}

func run(ctx context.Context) error {

	namespace := "default"
	name := "cowboy"

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	loadingRules.ExplicitPath = "kcp.kubeconfig"
	startingConfig, err := loadingRules.GetStartingConfig()
	if err != nil {
		return err
	}

	config, err := clientcmd.NewDefaultClientConfig(*startingConfig, nil).ClientConfig()

	if err != nil {
		return err
	}

	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}

	_, err = kubeClient.CoreV1().ServiceAccounts(namespace).Get(ctx, name, metav1.GetOptions{})

	switch {
	case apierrors.IsNotFound(err):
		fmt.Fprintf(os.Stdout, "Creating service account %q\n", name)
		if _, err = kubeClient.CoreV1().ServiceAccounts(namespace).Create(ctx, &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create ServiceAccount %s|%s/%s: %w", name, namespace, name, err)
		}
	case err == nil:
		// nothing

	default:
		return fmt.Errorf("failed to get the ServiceAccount %s|%s/%s: %w", name, name, namespace, err)
	}

	secret, err := kubeClient.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})

	switch {
	case apierrors.IsNotFound(err):
		fmt.Fprintf(os.Stdout, "Creating service account secret %q\n", name)
		if secret, err = kubeClient.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Annotations: map[string]string{
					"kubernetes.io/service-account.name": name,
				},
			},
			Type: corev1.SecretTypeServiceAccountToken,
		}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create secret %s|%s/%s: %w", name, namespace, name, err)
		}
	case err == nil:
		// TODO: to the patch if needed if/when we migrate the secret format
	default:
		return fmt.Errorf("failed to get the Secret %s|%s/%s: %w", name, name, namespace, err)
	}

	// Create a cluster role that provides the proxy the minimal permissions
	// required by KCP to manage the proxy target, and by the proxy virtual
	// workspace to proxy.
	rules := []rbacv1.PolicyRule{
		{
			Verbs:         []string{"get", "list", "watch"},
			APIGroups:     []string{wildwestv1alpha1.SchemeGroupVersion.Group},
			Resources:     []string{"cowboys", "cowboys/status", "sheriffs", "sheriffs/status"},
			ResourceNames: []string{"default"},
		},
		{
			Verbs:         []string{"update", "patch"},
			APIGroups:     []string{wildwestv1alpha1.SchemeGroupVersion.Group},
			ResourceNames: []string{"default"},
			Resources:     []string{"cowboys", "cowboys/status", "sheriffs", "sheriffs/status"},
		},
		{
			Verbs:     []string{"get", "list", "watch"},
			APIGroups: []string{"apiextensions.k8s.io"},
			Resources: []string{"customresourcedefinitions"},
		},
		{
			Verbs:           []string{"access"},
			NonResourceURLs: []string{"/"},
		},
	}

	_, err = kubeClient.RbacV1().ClusterRoles().Get(ctx,
		name,
		metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		fmt.Fprintf(os.Stdout, "Creating cluster role %q to give service account %q\n\n 1. write and sync access to the proxy target %q\n\n", name, name, name)
		if _, err = kubeClient.RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Rules: rules,
		}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	case err == nil:
		// nothing
	default:
		return err
	}

	// Grant the service account the role created just above in the workspace
	subjects := []rbacv1.Subject{{
		Kind:      "ServiceAccount",
		Name:      name,
		Namespace: namespace,
	}}
	roleRef := rbacv1.RoleRef{
		Kind:     "ClusterRole",
		Name:     name,
		APIGroup: "rbac.authorization.k8s.io",
	}

	_, err = kubeClient.RbacV1().ClusterRoleBindings().Get(ctx,
		name,
		metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if err := kubeClient.RbacV1().ClusterRoleBindings().Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			return err
		}
	}

	fmt.Fprintf(os.Stdout, "Creating or updating cluster role binding %q to bind service account %q to cluster role %q.\n", name, name, name)
	if _, err = kubeClient.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Subjects: subjects,
		RoleRef:  roleRef,
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	time.Sleep(time.Second * 2)

	// Retrieve the token that the proxy will use to authenticate to kcp
	tokenSecret, err := kubeClient.CoreV1().Secrets(namespace).Get(ctx, secret.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to retrieve Secret: %w", err)
	}

	saTokenBytes := tokenSecret.Data["token"]
	if len(saTokenBytes) == 0 {
		return fmt.Errorf("token secret %s/%s is missing a value for `token`", namespace, secret.Name)
	}

	configURL, _, err := helpers.ParseClusterURL(config.Host)
	if err != nil {
		return fmt.Errorf("current URL %q does not point to workspace", config.Host)
	}

	// Make sure the generated URL has the port specified correctly.
	if _, _, err = net.SplitHostPort(configURL.Host); err != nil {
		var addrErr *net.AddrError
		const missingPort = "missing port in address"
		if errors.As(err, &addrErr) && addrErr.Err == missingPort {
			if configURL.Scheme == "https" {
				configURL.Host = net.JoinHostPort(configURL.Host, "443")
			} else {
				configURL.Host = net.JoinHostPort(configURL.Host, "80")
			}
		} else {
			return fmt.Errorf("failed to parse host %q: %w", configURL.Host, err)
		}
	}

	// Compose the syncer's upstream configuration server URL without any path. This is
	// required so long as the API importer and syncer expect to require cluster clients.
	//
	// TODO(marun) It's probably preferable that the syncer and importer are provided a
	// cluster configuration since they only operate against a single workspace.
	serverURL := configURL.Scheme + "://" + configURL.Host
	input := templateInput{
		ServerURL:    serverURL,
		CAData:       base64.StdEncoding.EncodeToString(config.CAData),
		Token:        string(saTokenBytes),
		KCPNamespace: namespace,
	}

	// Render the kubeconfig template
	kubeconfigBytes, err := renderSyncerResources(input)
	if err != nil {
		return fmt.Errorf("failed to render kubeconfig template: %w", err)
	}

	return os.WriteFile("repro/repro.kubeconfig", kubeconfigBytes, os.ModePerm)
}

// templateInput represents the external input required to render the resources to
// deploy the proxy to a pcluster.
type templateInput struct {
	// ServerURL is the logical cluster url the proxy configuration will use
	ServerURL string
	// CAData holds the PEM-encoded bytes of the ca certificate(s) a proxy will use to validate
	// kcp's serving certificate
	CAData string
	// Token is the service account token used to authenticate a syncer for access to a workspace
	Token string
	// KCPNamespace is the name of the kcp namespace of the proxy's service account
	KCPNamespace string
}

// templateArgs represents the full set of arguments required to render the resources
// required to deploy the syncer.
type templateArgs struct {
	templateInput
}

func renderSyncerResources(input templateInput) ([]byte, error) {
	tmplArgs := templateArgs{
		templateInput: input,
	}

	proxyTemplate, err := embeddedResources.ReadFile("kubeconfig.tmpl")
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("tmp").Parse(string(proxyTemplate))
	if err != nil {
		return nil, err
	}
	buffer := bytes.NewBuffer([]byte{})
	err = tmpl.Execute(buffer, tmplArgs)
	if err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
