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
	"fmt"
	"sort"
	"strings"
	"text/template"
	"time"

	jsonpatch "github.com/evanphx/json-patch"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/cliplugins/helpers"
)

//go:embed *.yaml
var embeddedResources embed.FS

const (
	// These resource names include a kcp- due to the intended use in pclusters.
	SyncerResourceName = "kcp-syncer"
	SyncerSecretName   = "kcp-syncer-config"

	// The name of the key for the upstream config in the pcluster secret.
	SyncerSecretConfigKey = "kubeconfig"

	// The prefix for syncer-supporting auth resources in kcp.
	SyncerAuthResourcePrefix = "syncer-"

	// Max length of service account name (cluster role has no limit)
	MaxSyncerAuthResourceName = 254

	// SyncerIDPrefix is the syncer id prefix is only 7 characters so that the 224 bits
	// of an sha hash can be suffixed and still be within kube's 63
	// char resource name limit.
	//
	// TODO(marun) This prefix should be reserved to avoid user resources being misidentified as syncer resources.
	// TODO(marun) Would a shorter hash be sufficient?
	SyncerIDPrefix = "kcpsync"
)

// Sync prepares a kcp workspace for use with a syncer and outputs the
// configuration required to deploy a syncer to the pcluster to stdout.
func (c *Config) Sync(ctx context.Context, workloadClusterName, kcpNamespaceName, image string, resourcesToSync []string, replicas int) error {
	config, err := clientcmd.NewDefaultClientConfig(*c.startingConfig, c.overrides).ClientConfig()
	if err != nil {
		return err
	}

	token, err := enableSyncerForWorkspace(ctx, config, workloadClusterName, kcpNamespaceName)
	if err != nil {
		return err
	}

	configURL, currentClusterName, err := helpers.ParseClusterURL(config.Host)
	if err != nil {
		return fmt.Errorf("current URL %q does not point to cluster workspace", config.Host)
	}

	// Compose the syncer's upstream configuration server URL without any path. This is
	// required so long as the API importer and syncer expect to require cluster clients.
	//
	// TODO(marun) It's probably preferable that the syncer and importer are provided a
	// cluster configuration since they only operate against a single workspace.
	serverURL := configURL.Scheme + "://" + configURL.Host

	input := templateInput{
		ServerURL:       serverURL,
		CAData:          base64.StdEncoding.EncodeToString(config.CAData),
		Token:           token,
		KCPNamespace:    kcpNamespaceName,
		LogicalCluster:  currentClusterName.String(),
		WorkloadCluster: workloadClusterName,
		Image:           image,
		Replicas:        replicas,
		ResourcesToSync: resourcesToSync,
	}

	resources, err := renderSyncerResources(input)
	if err != nil {
		return err
	}

	_, err = c.Out.Write(resources)
	return err
}

// GetSyncerID returns the resource identifier of a syncer for the given logical
// cluster and workload cluster. The ID is unique for unique pairs of inputs to ensure
// a pcluster can be configured with multiple syncers for a given kcp instance.
func GetSyncerID(logicalClusterName string, workloadClusterName string) string {
	syncerHash := sha256.Sum224([]byte(logicalClusterName + workloadClusterName))
	return fmt.Sprintf("%s%x", SyncerIDPrefix, syncerHash)
}

// enableSyncerForWorkspace creates a workload cluster with the given name and creates a service
// account for the syncer in the given namespace. The expectation is that the provided config is
// for a logical cluster (workspace). Returns the token the syncer will use to connect to kcp.
func enableSyncerForWorkspace(ctx context.Context, config *rest.Config, workloadClusterName, namespace string) (string, error) {
	kcpClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return "", fmt.Errorf("failed to create kcp client: %w", err)
	}

	workloadCluster, err := kcpClient.WorkloadV1alpha1().WorkloadClusters().Get(ctx,
		workloadClusterName,
		metav1.GetOptions{},
	)
	if err != nil && !errors.IsNotFound(err) {
		return "", fmt.Errorf("failed to get workloadcluster %s: %w", workloadClusterName, err)
	}
	if errors.IsNotFound(err) {
		// Create the workload cluster that will serve as a point of coordination between
		// kcp and the syncer (e.g. heartbeating from the syncer and virtual cluster urls
		// to the syncer).
		workloadCluster, err = kcpClient.WorkloadV1alpha1().WorkloadClusters().Create(ctx,
			&workloadv1alpha1.WorkloadCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: workloadClusterName,
				},
			},
			metav1.CreateOptions{},
		)
		if err != nil && !errors.IsAlreadyExists(err) {
			return "", fmt.Errorf("failed to create workloadcluster %s: %w", workloadClusterName, err)
		}
	}

	kubeClient, err := kubernetesclientset.NewForConfig(config)
	if err != nil {
		return "", fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	workloadClusterOwnerReferences := []metav1.OwnerReference{{
		APIVersion: workloadv1alpha1.SchemeGroupVersion.String(),
		Kind:       "WorkloadCluster",
		Name:       workloadCluster.Name,
		UID:        workloadCluster.UID,
	}}

	// Create a service account for the syncer with the necessary permissions. It will
	// be owned by the workload cluster to ensure cleanup.
	authResourceName := SyncerAuthResourcePrefix + workloadClusterName
	sa, err := kubeClient.CoreV1().ServiceAccounts(namespace).Get(ctx, authResourceName, metav1.GetOptions{})

	switch {
	case errors.IsNotFound(err):
		if sa, err = kubeClient.CoreV1().ServiceAccounts(namespace).Create(ctx, &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            authResourceName,
				OwnerReferences: workloadClusterOwnerReferences,
			},
		}, metav1.CreateOptions{}); err != nil && !errors.IsAlreadyExists(err) {
			return "", fmt.Errorf("failed to create ServiceAccount %s|%s/%s: %w", workloadClusterName, namespace, authResourceName, err)
		}
	case err == nil:
		oldData, err := json.Marshal(corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				OwnerReferences: sa.OwnerReferences,
			},
		})
		if err != nil {
			return "", fmt.Errorf("failed to marshal old data for ServiceAccount %s|%s/%s: %w", workloadClusterName, namespace, authResourceName, err)
		}

		newData, err := json.Marshal(corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				UID:             sa.UID,
				ResourceVersion: sa.ResourceVersion,
				OwnerReferences: mergeOwnerReference(sa.ObjectMeta.OwnerReferences, workloadClusterOwnerReferences),
			},
		})
		if err != nil {
			return "", fmt.Errorf("failed to marshal new data for ServiceAccount %s|%s/%s: %w", workloadClusterName, namespace, authResourceName, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return "", fmt.Errorf("failed to create patch for ServiceAccount %s|%s/%s: %w", workloadClusterName, namespace, authResourceName, err)
		}

		if sa, err = kubeClient.CoreV1().ServiceAccounts(namespace).Patch(ctx, sa.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
			return "", fmt.Errorf("failed to patch ServiceAccount %s|%s/%s: %w", workloadClusterName, authResourceName, namespace, err)
		}
	default:
		return "", fmt.Errorf("failed to get the ServiceAccount %s|%s/%s: %w", workloadClusterName, authResourceName, namespace, err)
	}

	// Grant the service account cluster-admin on the workspace
	// TODO(sttts): remove this once syncer workspace access goes through the virtual workspace
	subjects := []rbacv1.Subject{{
		Kind:      "ServiceAccount",
		Name:      authResourceName,
		Namespace: namespace,
	}}
	roleRef := rbacv1.RoleRef{
		Kind:     "ClusterRole",
		Name:     "cluster-admin",
		APIGroup: "rbac.authorization.k8s.io",
	}
	crb, err := kubeClient.RbacV1().ClusterRoleBindings().Get(ctx,
		authResourceName,
		metav1.GetOptions{})
	switch {
	case errors.IsNotFound(err):
		if _, err = kubeClient.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            authResourceName,
				OwnerReferences: workloadClusterOwnerReferences,
			},
			Subjects: subjects,
			RoleRef:  roleRef,
		}, metav1.CreateOptions{}); err != nil && !errors.IsAlreadyExists(err) {
			return "", err
		}
	case err == nil:
		oldData, err := json.Marshal(rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				OwnerReferences: crb.OwnerReferences,
			},
			Subjects: crb.Subjects,
			RoleRef:  crb.RoleRef,
		})
		if err != nil {
			return "", fmt.Errorf("failed to marshal old data for ClusterRoleBinding %s|%s: %w", workloadClusterName, authResourceName, err)
		}

		newData, err := json.Marshal(rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				UID:             crb.UID,
				ResourceVersion: crb.ResourceVersion,
				OwnerReferences: mergeOwnerReference(crb.OwnerReferences, workloadClusterOwnerReferences),
			},
			Subjects: subjects,
			RoleRef:  roleRef,
		})
		if err != nil {
			return "", fmt.Errorf("failed to marshal new data for ClusterRoleBinding %s|%s: %w", workloadClusterName, authResourceName, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return "", fmt.Errorf("failed to create patch for ClusterRoleBinding %s|%s: %w", workloadClusterName, authResourceName, err)
		}

		if _, err = kubeClient.RbacV1().ClusterRoleBindings().Patch(ctx, crb.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
			return "", fmt.Errorf("failed to patch ClusterRoleBinding %s|%s/%s: %w", workloadClusterName, authResourceName, namespace, err)
		}
	default:
		return "", err
	}

	// Wait for the service account to be updated with the name of the token secret
	tokenSecretName := ""
	err = wait.PollImmediateWithContext(ctx, 100*time.Millisecond, 20*time.Second, func(ctx context.Context) (bool, error) {
		serviceAccount, err := kubeClient.CoreV1().ServiceAccounts(namespace).Get(ctx, sa.Name, metav1.GetOptions{})
		if err != nil {
			klog.V(5).Infof("failed to retrieve ServiceAccount: %v", err)
			return false, nil
		}
		if len(serviceAccount.Secrets) == 0 {
			return false, nil
		}
		tokenSecretName = serviceAccount.Secrets[0].Name
		return true, nil
	})
	if err != nil {
		return "", fmt.Errorf("timed out waiting for token secret name to be set on ServiceAccount %s/%s", namespace, sa.Name)
	}

	// Retrieve the token that the syncer will use to authenticate to kcp
	tokenSecret, err := kubeClient.CoreV1().Secrets(namespace).Get(ctx, tokenSecretName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to retrieve Secret: %w", err)
	}
	saToken := tokenSecret.Data["token"]
	if len(saToken) == 0 {
		return "", fmt.Errorf("token secret %s/%s is missing a value for `token`", namespace, tokenSecretName)
	}

	return string(saToken), nil
}

// mergeOwnerReference: merge a slice of ownerReference with a given ownerReferences
func mergeOwnerReference(ownerReferences, newOwnerReferences []metav1.OwnerReference) []metav1.OwnerReference {
	merged := []metav1.OwnerReference{}

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
// deploy the syncer to a pcluster.
type templateInput struct {
	// ServerURL is the logical cluster url the syncer configuration will use
	ServerURL string
	// CAData holds the PEM-encoded bytes of the ca certificate(s) a syncer will use to validate
	// kcp's serving certificate
	CAData string
	// Token is the service account token used to authenticate a syncer for access to a workspace
	Token string
	// KCPNamespace is the name of the kcp namespace of the syncer's service account
	KCPNamespace string
	// LogicalCluster is the qualified kcp logical cluster name the syncer will sync from
	LogicalCluster string
	// WorkloadCluster is the name of the workload cluster the syncer will use to
	// communicate its status and read configuration from
	WorkloadCluster string
	// ResourcesToSync is the set of qualified resource names (eg. ["services",
	// "deployments.apps.k8s.io") that the syncer will synchronize between the kcp
	// workspace and the pcluster.
	ResourcesToSync []string
	// Image is the name of the container image that the syncer deployment will use
	Image string
	// Replicas is the number of syncer pods to run (should be 0 or 1).
	Replicas int
}

// templateArgs represents the full set of arguments required to render the resources
// required to deploy the syncer.
type templateArgs struct {
	templateInput
	// LabelSafeLogicalCluster is the qualified kcp logical cluster name that is
	// safe to appear as a label value
	LabelSafeLogicalCluster string
	// Namespace is the name of the syncer namespace on the pcluster
	Namespace string
	// ServiceAccount is the name of the service account to create in the syncer
	// namespace on the pcluster.
	ServiceAccount string
	// ClusterRole is the name of the cluster role to create for the syncer on the
	// pcluster.
	ClusterRole string
	// ClusterRoleBinding is the name of the cluster role binding to create for the
	// syncer on the pcluster.
	ClusterRoleBinding string
	// GroupMappings is the mapping of api group to resources that will be used to
	// define the cluster role rules for the syncer in the pcluster. The syncer will be
	// granted full permissions for the resources it will synchronize.
	GroupMappings []groupMapping
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
func renderSyncerResources(input templateInput) ([]byte, error) {
	syncerID := GetSyncerID(input.LogicalCluster, input.WorkloadCluster)

	tmplArgs := templateArgs{
		templateInput:           input,
		LabelSafeLogicalCluster: strings.ReplaceAll(input.LogicalCluster, ":", "_"),
		Namespace:               syncerID,
		ServiceAccount:          SyncerResourceName,
		ClusterRole:             syncerID,
		ClusterRoleBinding:      syncerID,
		GroupMappings:           getGroupMappings(input.ResourcesToSync),
		Secret:                  SyncerSecretName,
		SecretConfigKey:         SyncerSecretConfigKey,
		Deployment:              SyncerResourceName,
		DeploymentApp:           syncerID,
	}

	syncerTemplate, err := embeddedResources.ReadFile("syncer.yaml")
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("syncerTemplate").Parse(string(syncerTemplate))
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

// groupMapping associates an api group to the resources in that group.
type groupMapping struct {
	APIGroup  string
	Resources []string
}

// getGroupMappings returns the set of api groups to resources for the given resources.
func getGroupMappings(resourcesToSync []string) []groupMapping {
	groupMap := make(map[string][]string)

	for _, resource := range resourcesToSync {
		nameParts := strings.SplitN(resource, ".", 2)
		name := nameParts[0]
		apiGroup := ""
		if len(nameParts) > 1 {
			apiGroup = nameParts[1]
		}
		if _, ok := groupMap[apiGroup]; !ok {
			groupMap[apiGroup] = []string{name}
		} else {
			groupMap[apiGroup] = append(groupMap[apiGroup], name)
		}
	}
	groupMappings := []groupMapping{}

	for apiGroup, resources := range groupMap {
		groupMappings = append(groupMappings, groupMapping{
			APIGroup:  apiGroup,
			Resources: resources,
		})
	}

	sortGroupMappings(groupMappings)

	return groupMappings
}

// sortGroupMappings sorts group mappings first by APIGroup and then by Resources.
func sortGroupMappings(groupMappings []groupMapping) {
	sort.Slice(groupMappings, func(i, j int) bool {
		if groupMappings[i].APIGroup == groupMappings[j].APIGroup {
			return strings.Join(groupMappings[i].Resources, ",") < strings.Join(groupMappings[j].Resources, ",")
		}
		return groupMappings[i].APIGroup < groupMappings[j].APIGroup
	})
}
