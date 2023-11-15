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

package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"text/template"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/martinlindhe/base36"
	"github.com/spf13/cobra"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/kcp/pkg/cliplugins/base"
	"github.com/kcp-dev/kcp/pkg/cliplugins/helpers"
	proxyv1alpha1 "github.com/kcp-dev/kcp/proxy/apis/proxy/v1alpha1"
	proxyclient "github.com/kcp-dev/kcp/proxy/client/clientset/versioned"
	kcpclient "github.com/kcp-dev/kcp/sdk/client/clientset/versioned"
)

//go:embed *.yaml
var embeddedResources embed.FS

const (
	ProxySecretConfigKey     = "kubeconfig"
	ProxyIDPrefix            = "kcp-proxy-"
	MaxProxyTargetNameLength = validation.DNS1123SubdomainMaxLength - (9 + len(ProxyIDPrefix))
)

// ProxyOptions contains options for configuring a Proxy target and its corresponding proxy agent.
type ProxyOptions struct {
	*base.Options

	// ProxyImage is the container image that should be used for the proxy agent.
	ProxyImage string
	// Replicas is the number of replicas to configure in the proxy's deployment.
	Replicas int
	// OutputFile is the path to a file where the YAML for the proxy should be written.
	OutputFile string
	// QPS is the refill rate for the proxy client's rate limiter bucket (steady state requests per second).
	QPS float32
	// Burst is the maximum size for the proxy client's rate limiter bucket when idle.
	Burst int
	// ProxyTargetName is the name of the Proxy in the kcp workspace.
	ProxyTargetName string
	// ProxyTargetLabels are the labels to be applied to the proxy target in the kcp workspace.
	ProxyTargetLabels []string
	// ProxyNamespace is the name of the namespace in the kcp workspace where the service account is created for the
	// proxy.
	ProxyNamespace string
}

// NewProxyOptions returns a new ProxyOptions.
func NewProxyOptions(streams genericclioptions.IOStreams) *ProxyOptions {
	return &ProxyOptions{
		Options:        base.NewOptions(streams),
		ProxyNamespace: "default",
		Replicas:       1,
		QPS:            20,
		Burst:          30,
	}
}

// BindFlags binds fields ProxyOptions as command line flags to cmd's flagset.
func (o *ProxyOptions) BindFlags(cmd *cobra.Command) {
	o.Options.BindFlags(cmd)

	cmd.Flags().StringVar(&o.ProxyImage, "proxy-image", o.ProxyImage, "The proxy image to use in the proxy's deployment YAML. Images are published at https://github.com/kcp-dev/kcp/pkgs/container/kcp%2Fproxy.")
	cmd.Flags().IntVar(&o.Replicas, "replicas", o.Replicas, "Number of replicas of the proxy deployment.")
	cmd.Flags().StringVar(&o.ProxyNamespace, "proxy-namespace", o.ProxyNamespace, "The name of the proxy namespace to create a service account in.")
	cmd.Flags().StringVarP(&o.OutputFile, "output-file", "o", o.OutputFile, "The manifest file to be created and applied to the physical cluster. Use - for stdout.")
	cmd.Flags().Float32Var(&o.QPS, "qps", o.QPS, "QPS to use when talking to API servers.")
	cmd.Flags().IntVar(&o.Burst, "burst", o.Burst, "Burst to use when talking to API servers.")
	cmd.Flags().StringSliceVar(&o.ProxyTargetLabels, "labels", o.ProxyTargetLabels, "Labels to apply on the Proxy target created in kcp, each label should be in the format of key=value.")
}

// Complete ensures all dynamically populated fields are initialized.
func (o *ProxyOptions) Complete(args []string) error {
	if err := o.Options.Complete(); err != nil {
		return err
	}

	o.ProxyTargetName = args[0]

	return nil
}

// Validate validates the ProxyOptions are complete and usable.
func (o *ProxyOptions) Validate() error {
	var errs []error

	if err := o.Options.Validate(); err != nil {
		errs = append(errs, err)
	}

	if o.ProxyImage == "" {
		errs = append(errs, errors.New("--proxy-image is required"))
	}

	if o.ProxyNamespace == "" {
		errs = append(errs, errors.New("--proxy-namespace is required"))
	}

	if o.Replicas < 0 {
		errs = append(errs, errors.New("--replicas cannot be negative"))
	}
	if o.Replicas > 1 {
		// TODO: relax when we have leader-election in the syncer
		errs = append(errs, errors.New("only 0 and 1 are valid values for --replicas"))
	}

	if o.OutputFile == "" {
		errs = append(errs, errors.New("--output-file is required"))
	}

	if len(o.ProxyTargetName) > 254 {
		errs = append(errs, fmt.Errorf("the maximum length of the sync-target-name is %d", MaxProxyTargetNameLength))
	}

	for _, l := range o.ProxyTargetLabels {
		if len(strings.Split(l, "=")) != 2 {
			errs = append(errs, fmt.Errorf("label '%s' is not in the format of key=value", l))
		}
	}

	return utilerrors.NewAggregate(errs)
}

// Run prepares a kcp workspace for use with a proxy and outputs the
// configuration required to deploy a prpxy to the pcluster to stdout.
func (o *ProxyOptions) Run(ctx context.Context) error {
	config, err := o.ClientConfig.ClientConfig()
	if err != nil {
		return err
	}

	var output io.Writer
	if o.OutputFile == "-" {
		output = o.IOStreams.Out
	} else {
		outputFile, err := os.Create(o.OutputFile)
		if err != nil {
			return err
		}
		defer outputFile.Close()
		output = outputFile
	}

	labels := map[string]string{}
	for _, l := range o.ProxyTargetLabels {
		parts := strings.Split(l, "=")
		if len(parts) != 2 {
			continue
		}
		labels[parts[0]] = parts[1]
	}

	token, proxyID, syncTarget, err := o.enableProxyForWorkspace(ctx, config, o.ProxyTargetName, o.ProxyNamespace, labels)
	if err != nil {
		return err
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
		Token:        token,
		KCPNamespace: o.ProxyNamespace,
		Namespace:    o.ProxyNamespace,

		ProxyTargetPath: logicalcluster.From(syncTarget).Path().String(),
		ProxyTargetName: o.ProxyTargetName,
		ProxyTargetUID:  string(syncTarget.UID),

		Image:    o.ProxyImage,
		Replicas: o.Replicas,
		QPS:      o.QPS,
		Burst:    o.Burst,
	}

	resources, err := renderSyncerResources(input, proxyID, nil)
	if err != nil {
		return err
	}

	_, err = output.Write(resources)
	if o.OutputFile != "-" {
		fmt.Fprintf(o.ErrOut, "\nWrote physical cluster manifest to %s for namespace %q. Use\n\n  KUBECONFIG=<pcluster-config> kubectl apply -f %q\n\nto apply it. "+
			"Use\n\n  KUBECONFIG=<pcluster-config> kubectl get deployment -n %q %s\n\nto verify the proxy pod is running.\n", o.OutputFile, o.ProxyNamespace, o.OutputFile, o.ProxyNamespace, proxyID)
	}
	return err
}

// getProxyID returns a unique ID for a proxy derived from the name and its UID. It's
// a valid DNS segment and can be used as namespace or object names.
func getProxyID(proxyTarget *proxyv1alpha1.WorkspaceProxy) string {
	hash := sha256.Sum224([]byte(proxyTarget.UID))
	base36hash := strings.ToLower(base36.EncodeBytes(hash[:]))
	return fmt.Sprintf("kcp-proxy-%s-%s", proxyTarget.Name, base36hash[:8])
}

func (o *ProxyOptions) applyProxyTarget(ctx context.Context, kcpClient kcpclient.Interface, proxyClient proxyclient.Interface, proxyTargetName string, labels map[string]string) (*proxyv1alpha1.WorkspaceProxy, error) {
	// TODO: check if workspace we are in is proxy type
	proxyTarget, err := proxyClient.ProxyV1alpha1().WorkspaceProxies().Get(ctx, proxyTargetName, metav1.GetOptions{})

	switch {
	case apierrors.IsNotFound(err):
		// Create the sync target that will serve as a point of coordination between
		// kcp and the syncer (e.g. heartbeating from the syncer and virtual cluster urls
		// to the syncer).
		fmt.Fprintf(o.ErrOut, "Creating proxy target %q\n", proxyTargetName)
		proxyTarget, err = proxyClient.ProxyV1alpha1().WorkspaceProxies().Create(ctx,
			&proxyv1alpha1.WorkspaceProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:   proxyTargetName,
					Labels: labels,
				},
				Spec: proxyv1alpha1.WorkspaceProxySpec{
					Type: proxyv1alpha1.ProxyTypePassthrough,
				},
			},
			metav1.CreateOptions{},
		)
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("failed to create proxy target %q: %w", proxyTargetName, err)
		}
		if err == nil {
			return proxyTarget, nil
		}
	case err != nil:
		return nil, err
	}

	if equality.Semantic.DeepEqual(labels, proxyTarget.ObjectMeta.Labels) {
		return proxyTarget, nil
	}

	// Patch proxy with updated labels
	oldData, err := json.Marshal(proxyv1alpha1.WorkspaceProxy{
		ObjectMeta: metav1.ObjectMeta{
			Labels: proxyTarget.ObjectMeta.Labels,
		},
		Spec: proxyv1alpha1.WorkspaceProxySpec{
			Type: proxyv1alpha1.ProxyTypePassthrough,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to Marshal old data for proxy target %s: %w", proxyTargetName, err)
	}

	newData, err := json.Marshal(proxyv1alpha1.WorkspaceProxy{
		ObjectMeta: metav1.ObjectMeta{
			UID:             proxyTarget.UID,
			ResourceVersion: proxyTarget.ResourceVersion,
			Labels:          labels,
		}, // to ensure they appear in the patch as preconditions
		Spec: proxyv1alpha1.WorkspaceProxySpec{
			Type: proxyv1alpha1.ProxyTypePassthrough,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to Marshal new data for proxy target %s: %w", proxyTargetName, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return nil, fmt.Errorf("failed to create merge patch for proxy target %q because: %w", proxyTargetName, err)
	}

	if proxyTarget, err = proxyClient.ProxyV1alpha1().WorkspaceProxies().Patch(ctx, proxyTargetName, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
		return nil, fmt.Errorf("failed to patch proxy target %s: %w", proxyTargetName, err)
	}
	return proxyTarget, nil
}

// enableProxyForWorkspace creates a proxy target with the given name and creates a service
// account for the proxy in the given namespace. The expectation is that the provided config is
// for a logical cluster (workspace). Returns the token the proxy will use to connect to kcp.
func (o *ProxyOptions) enableProxyForWorkspace(ctx context.Context, config *rest.Config, proxyTargetName, namespace string, labels map[string]string) (saToken string, proxyID string, proxyTarget *proxyv1alpha1.WorkspaceProxy, err error) {
	kcpClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to create kcp client: %w", err)
	}
	proxyClient, err := proxyclient.NewForConfig(config)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to create proxy client: %w", err)
	}

	proxyTarget, err = o.applyProxyTarget(ctx, kcpClient, proxyClient, proxyTargetName, labels)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to apply proxy targer %q: %w", proxyTargetName, err)
	}

	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	proxyID = getProxyID(proxyTarget)

	proxyTargetOwnerReferences := []metav1.OwnerReference{{
		APIVersion: proxyv1alpha1.SchemeGroupVersion.String(),
		Kind:       "workspaceproxy",
		Name:       proxyTarget.Name,
		UID:        proxyTarget.UID,
	}}
	sa, err := kubeClient.CoreV1().ServiceAccounts(namespace).Get(ctx, proxyID, metav1.GetOptions{})

	switch {
	case apierrors.IsNotFound(err):
		fmt.Fprintf(o.ErrOut, "Creating service account %q\n", proxyID)
		if sa, err = kubeClient.CoreV1().ServiceAccounts(namespace).Create(ctx, &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            proxyID,
				OwnerReferences: proxyTargetOwnerReferences,
			},
		}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return "", "", nil, fmt.Errorf("failed to create ServiceAccount %s|%s/%s: %w", proxyTargetName, namespace, proxyID, err)
		}
	case err == nil:
		oldData, err := json.Marshal(corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				OwnerReferences: sa.OwnerReferences,
			},
		})
		if err != nil {
			return "", "", nil, fmt.Errorf("failed to marshal old data for ServiceAccount %s|%s/%s: %w", proxyTargetName, namespace, proxyID, err)
		}

		newData, err := json.Marshal(corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				UID:             sa.UID,
				ResourceVersion: sa.ResourceVersion,
				OwnerReferences: mergeOwnerReference(sa.ObjectMeta.OwnerReferences, proxyTargetOwnerReferences),
			},
		})
		if err != nil {
			return "", "", nil, fmt.Errorf("failed to marshal new data for ServiceAccount %s|%s/%s: %w", proxyTargetName, namespace, proxyID, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return "", "", nil, fmt.Errorf("failed to create patch for ServiceAccount %s|%s/%s: %w", proxyTargetName, namespace, proxyID, err)
		}

		fmt.Fprintf(o.ErrOut, "Updating service account %q.\n", proxyID)
		if sa, err = kubeClient.CoreV1().ServiceAccounts(namespace).Patch(ctx, sa.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
			return "", "", nil, fmt.Errorf("failed to patch ServiceAccount %s|%s/%s: %w", proxyTargetName, proxyID, namespace, err)
		}
	default:
		return "", "", nil, fmt.Errorf("failed to get the ServiceAccount %s|%s/%s: %w", proxyTargetName, proxyID, namespace, err)
	}

	secret, err := kubeClient.CoreV1().Secrets(namespace).Get(ctx, proxyID, metav1.GetOptions{})

	switch {
	case apierrors.IsNotFound(err):
		fmt.Fprintf(o.ErrOut, "Creating service account secret %q\n", proxyID)
		if secret, err = kubeClient.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            proxyID,
				OwnerReferences: proxyTargetOwnerReferences,
				Annotations: map[string]string{
					"kubernetes.io/service-account.name": proxyID,
				},
			},
			Type: corev1.SecretTypeServiceAccountToken,
		}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return "", "", nil, fmt.Errorf("failed to create secret %s|%s/%s: %w", proxyTargetName, namespace, proxyID, err)
		}
	case err == nil:
		// TODO: to the patch if needed if/when we migrate the secret format
	default:
		return "", "", nil, fmt.Errorf("failed to get the Secret %s|%s/%s: %w", proxyTargetName, proxyID, namespace, err)
	}

	// Create a cluster role that provides the proxy the minimal permissions
	// required by KCP to manage the proxy target, and by the proxy virtual
	// workspace to proxy.
	rules := []rbacv1.PolicyRule{
		{
			Verbs:         []string{"tunnel"},
			APIGroups:     []string{proxyv1alpha1.SchemeGroupVersion.Group},
			ResourceNames: []string{proxyTargetName},
			Resources:     []string{"workspaceproxies"},
		},
		{
			Verbs:         []string{"get"},
			APIGroups:     []string{proxyv1alpha1.SchemeGroupVersion.Group},
			ResourceNames: []string{proxyTargetName},
			Resources:     []string{"workspaceproxies/tunnel"},
		},
		{
			Verbs:         []string{"get", "list", "watch"},
			APIGroups:     []string{proxyv1alpha1.SchemeGroupVersion.Group},
			Resources:     []string{"workspaceproxies", "workspaceproxies/status"},
			ResourceNames: []string{proxyTargetName},
		},
		{
			Verbs:         []string{"update", "patch", "put"},
			APIGroups:     []string{proxyv1alpha1.SchemeGroupVersion.Group},
			ResourceNames: []string{proxyTargetName},
			Resources:     []string{"workspaceproxies", "workspaceproxies/status"},
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

	cr, err := kubeClient.RbacV1().ClusterRoles().Get(ctx,
		proxyID,
		metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		fmt.Fprintf(o.ErrOut, "Creating cluster role %q to give service account %q\n\n 1. write and sync access to the proxy target %q\n\n", proxyID, proxyID, proxyID)
		if _, err = kubeClient.RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:            proxyID,
				OwnerReferences: proxyTargetOwnerReferences,
			},
			Rules: rules,
		}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return "", "", nil, err
		}
	case err == nil:
		oldData, err := json.Marshal(rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				OwnerReferences: cr.OwnerReferences,
			},
			Rules: cr.Rules,
		})
		if err != nil {
			return "", "", nil, fmt.Errorf("failed to marshal old data for ClusterRole %s|%s: %w", proxyTargetName, proxyID, err)
		}

		newData, err := json.Marshal(rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				UID:             cr.UID,
				ResourceVersion: cr.ResourceVersion,
				OwnerReferences: mergeOwnerReference(cr.OwnerReferences, proxyTargetOwnerReferences),
			},
			Rules: rules,
		})
		if err != nil {
			return "", "", nil, fmt.Errorf("failed to marshal new data for ClusterRole %s|%s: %w", proxyTargetName, proxyID, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return "", "", nil, fmt.Errorf("failed to create patch for ClusterRole %s|%s: %w", proxyTargetName, proxyID, err)
		}

		fmt.Fprintf(o.ErrOut, "Updating cluster role %q with\n\n 1. write and proxy access to the proxy target %q\n\n", proxyID, proxyID)
		if _, err = kubeClient.RbacV1().ClusterRoles().Patch(ctx, cr.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
			return "", "", nil, fmt.Errorf("failed to patch ClusterRole %s|%s/%s: %w", proxyTargetName, proxyID, namespace, err)
		}
	default:
		return "", "", nil, err
	}

	// Grant the service account the role created just above in the workspace
	subjects := []rbacv1.Subject{{
		Kind:      "ServiceAccount",
		Name:      proxyID,
		Namespace: namespace,
	}}
	roleRef := rbacv1.RoleRef{
		Kind:     "ClusterRole",
		Name:     proxyID,
		APIGroup: "rbac.authorization.k8s.io",
	}

	_, err = kubeClient.RbacV1().ClusterRoleBindings().Get(ctx,
		proxyID,
		metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return "", "", nil, err
	}
	if err == nil {
		if err := kubeClient.RbacV1().ClusterRoleBindings().Delete(ctx, proxyID, metav1.DeleteOptions{}); err != nil {
			return "", "", nil, err
		}
	}

	fmt.Fprintf(o.ErrOut, "Creating or updating cluster role binding %q to bind service account %q to cluster role %q.\n", proxyID, proxyID, proxyID)
	if _, err = kubeClient.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:            proxyID,
			OwnerReferences: proxyTargetOwnerReferences,
		},
		Subjects: subjects,
		RoleRef:  roleRef,
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", "", nil, err
	}

	// Retrieve the token that the proxy will use to authenticate to kcp
	tokenSecret, err := kubeClient.CoreV1().Secrets(namespace).Get(ctx, secret.Name, metav1.GetOptions{})
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to retrieve Secret: %w", err)
	}
	saTokenBytes := tokenSecret.Data["token"]
	if len(saTokenBytes) == 0 {
		return "", "", nil, fmt.Errorf("token secret %s/%s is missing a value for `token`", namespace, secret.Name)
	}

	return string(saTokenBytes), proxyID, proxyTarget, nil
}

// mergeOwnerReference: merge a slice of ownerReference with a given ownerReferences.
func mergeOwnerReference(ownerReferences, newOwnerReferences []metav1.OwnerReference) []metav1.OwnerReference {
	var merged []metav1.OwnerReference

	merged = append(merged, ownerReferences...)

	for _, ownerReference := range newOwnerReferences {
		found := false
		for _, mergedOwnerReference := range merged {
			if mergedOwnerReference.UID == ownerReference.UID {
				found = true
				break
			}
		}
		if !found {
			merged = append(merged, ownerReference)
		}
	}

	return merged
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
	// Namespace is the name of the kcp namespace of the proxy's service account
	Namespace string
	// ProxyTargetPath is the qualified kcp logical cluster name the proxy will sync from
	ProxyTargetPath string
	// ProxyTargetName is the name of the proxy target the proxy will use to
	// communicate its status and read configuration from
	ProxyTargetName string
	// ProxyTargetUID is the UID of the proxy target the proxy will use to
	// communicate its status and read configuration from. This information is used by the
	// Syncer in order to avoid a conflict when a synctarget gets deleted and another one is
	// created with the same name.
	ProxyTargetUID string
	// Image is the name of the container image that the syncer deployment will use
	Image string
	// Replicas is the number of syncer pods to run (should be 0 or 1).
	Replicas int
	// QPS is the qps the syncer uses when talking to an apiserver.
	QPS float32
	// Burst is the burst the syncer uses when talking to an apiserver.
	Burst int
}

// templateArgs represents the full set of arguments required to render the resources
// required to deploy the syncer.
type templateArgs struct {
	templateInput
	// ServiceAccount is the name of the service account to create in the syncer
	// namespace on the pcluster.
	ServiceAccount string
	// ClusterRole is the name of the cluster role to create for the syncer on the
	// pcluster.
	ClusterRole string
	// ClusterRoleBinding is the name of the cluster role binding to create for the
	// syncer on the pcluster.
	ClusterRoleBinding string
	// Secret is the name of the secret that will contain the kubeconfig the syncer
	// will use to connect to the kcp logical cluster (workspace) that it will
	// synchronize from.
	Secret string
	// Key in the syncer secret for the kcp logical cluster kubconfig.
	SecretConfigKey string
	// Deployment is the name of the deployment that will run the syncer in the
	// pcluster.
	Deployment string
	// DeploymentApp is the label value that the syncer's deployment will select its
	// pods with.
	DeploymentApp string
}

// renderSyncerResources renders the resources required to deploy a syncer to a pcluster.
//
// TODO(marun) Is it possible to set owner references in a set of applied resources? Ideally the
// cluster role and role binding would be owned by the namespace to ensure cleanup on deletion
// of the namespace.
func renderSyncerResources(input templateInput, syncerID string, resourceForPermission []string) ([]byte, error) {
	tmplArgs := templateArgs{
		templateInput:      input,
		ServiceAccount:     syncerID,
		ClusterRole:        syncerID,
		ClusterRoleBinding: syncerID,
		Secret:             syncerID,
		SecretConfigKey:    ProxySecretConfigKey,
		Deployment:         syncerID,
		DeploymentApp:      syncerID,
	}

	proxyTemplate, err := embeddedResources.ReadFile("proxy.yaml")
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
