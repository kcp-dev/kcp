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
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"
	"text/template"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
)

type syncerConfigArgs struct {
	Token  string
	CACert string
}

// TODO(marun) Make this method idempotent
func enableSyncer(ctx context.Context, config *rest.Config, workloadClusterName, namespace string) (*syncerConfigArgs, error) {
	kcpClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kcp client: %w", err)
	}

	workloadCluster, err := kcpClient.WorkloadV1alpha1().WorkloadClusters().Create(ctx,
		&workloadv1alpha1.WorkloadCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: workloadClusterName,
			},
		},
		metav1.CreateOptions{},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create WorkloadCluster: %w", err)
	}

	kubeClient, err := kubernetesclientset.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	syncerServiceAccountName := fmt.Sprintf("syncer-%s", workloadClusterName)
	sa, err := kubeClient.CoreV1().ServiceAccounts(namespace).Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name: syncerServiceAccountName,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: workloadv1alpha1.SchemeGroupVersion.String(),
				Kind:       reflect.TypeOf(workloadv1alpha1.WorkloadCluster{}).Name(),
				Name:       workloadCluster.Name,
				UID:        workloadCluster.UID,
			}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create ServiceAccount: %w", err)
	}

	// Wait for the service account to be updated with the name of the Token secret
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
		return nil, fmt.Errorf("timed out waiting for Token secret name to be set on ServiceAccount %s/%s", namespace, sa.Name)
	}

	tokenSecret, err := kubeClient.CoreV1().Secrets(namespace).Get(ctx, tokenSecretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve Secret: %w", err)
	}
	saToken := tokenSecret.Data["token"]
	if len(saToken) == 0 {
		return nil, fmt.Errorf("token secret %s/%s is missing a value for `token`", namespace, tokenSecretName)
	}

	caConfigMapName := "kube-root-ca.crt"
	caConfigMap, err := kubeClient.CoreV1().ConfigMaps(namespace).Get(ctx, caConfigMapName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve ConfigMap: %w", err)
	}
	caCert := caConfigMap.Data["ca.crt"]
	if len(caCert) == 0 {
		return nil, fmt.Errorf("token secret %s/%s is missing a value for `ca.crt`", namespace, caConfigMapName)

	}

	return &syncerConfigArgs{
		Token:  string(saToken),
		CACert: base64.StdEncoding.EncodeToString([]byte(caCert)),
	}, nil
}

type templateInput struct {
	syncerConfigArgs
	KCPNamespace    string
	FromCluster     string
	WorkloadCluster string
	Image           string
	ResourcesToSync []string
	ServerURL       string
}

func renderPClusterResources(input templateInput) ([]byte, error) {

	syncerTmpl := `
---
apiVersion: v1
kind: Namespace
metadata:
  name: {{.Namespace}}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{.ServiceAccount}}
  Namespace:  {{.Namespace}}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: {{.ClusterRole}}
rules:
- apiGroups:
  - ""
  resources:
  - namespaces
  verbs:
  - create
- apiGroups:
  - "apiextensions.k8s.io"
  resources:
  - customresourcedefinitions
  verbs:
  - "get"
  - "watch"
  - "list"
{{- range $clusterRole := .ClusterRoles}}
- apiGroups:
  - {{$clusterRole.APIGroup}}
  resources:
  {{- range $resource := $clusterRole.Resources}}
  - {{$resource}}
  {{- end}}
  verbs:
  - "*"
{{- end}}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: {{.ClusterRoleBinding}}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: {{.ClusterRole}}
subjects:
- kind: ServiceAccount
  name: {{.ServiceAccount}}
  Namespace:  {{.Namespace}}
---
apiVersion: v1
kind: Secret
metadata:
  name: {{.Secret}}
  Namespace:  {{.Namespace}}
data:
  kubeconfig: |
    apiVersion: v1
    kind: Config
    clusters:
    - name: default-cluster
      cluster:
        certificate-authority-data: {{.CACert}}
        server: {{.ServerURL}}
    contexts:
    - name: default-context
      context:
        cluster: default-cluster
        namespace: {{.KCPNamespace}}
        user: default-user
    current-context: default-context
    users:
    - name: default-user
      user:
        token: {{.Token}}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{.Deployment}}
  Namespace:  {{.Namespace}}
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: {{.DeploymentApp}}
  template:
    metadata:
      labels:
        app: {{.DeploymentApp}}
    spec:
      containers:
      - name: kcp-syncer
        command:
        - /ko-app/syncer
        args:
        - --from-kubeconfig=/kcp/kubeconfig
        - --workload-cluster-name={{.WorkloadCluster}}
        - --from-cluster={{.FromCluster}}
{{- range $resourceToSync := .ResourcesToSync}}
        - --sync-resources={{$resourceToSync}}
{{- end}}
        Image: {{.Image}}
        imagePullPolicy: IfNotPresent
        terminationMessagePolicy: FallbackToLogsOnError
        volumeMounts:
        - name: kcp-config
          mountPath: /kcp/
          readOnly: true
        env:
        - name: SYNCER_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.Namespace
      serviceAccountName: {{.ServiceAccount}}
      volumes:
        - name: kcp-config
          secret:
            secretName: {{.Secret}}
            optional: false
`

	syncerHash := sha256.Sum224([]byte(input.FromCluster + input.WorkloadCluster))
	syncerID := fmt.Sprintf("kcp-syncer-%x", syncerHash)

	tmplArgs := templateArgs{
		templateInput:      input,
		Namespace:          syncerID,
		ServiceAccount:     "kcp-syncer",
		ClusterRole:        syncerID,
		ClusterRoleBinding: syncerID,
		Secret:             "kcp-syncer",
		Deployment:         "kcp-syncer",
		DeploymentApp:      "kcp-syncer",
	}

	//Template syncerTmpl
	tmpl, err := template.New("syncerTmpl").Parse(syncerTmpl)
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

type clusterRole struct {
	APIGroup  string
	Resources []string
}

//getClusterRoles returns a list of cluster roles from the given resources
func getClusterRoles(resourcesToSync []string) ([]clusterRole, error) {
	roleMap := make(map[string][]string)

	for _, resource := range resourcesToSync {
		resourceVersionParts := strings.Split(resource, "/")
		if len(resourceVersionParts) != 2 {
			return nil, fmt.Errorf("invalid resource %q, resources should be like: services/v1 or deployments.apps/v1", resource)
		}
		qualifiedResourceName := resourceVersionParts[0]
		name, apiGroup, _ := strings.Cut(qualifiedResourceName, ".")
		if _, ok := roleMap[apiGroup]; !ok {
			roleMap[apiGroup] = []string{name}
		} else {
			roleMap[apiGroup] = append(roleMap[apiGroup], name)
		}
	}
	clusterRoles := []clusterRole{}

	for apiGroup, resources := range roleMap {
		clusterRoles = append(clusterRoles, clusterRole{
			APIGroup:  apiGroup,
			Resources: resources,
		})
	}

	return clusterRoles, nil
}

type templateArgs struct {
	templateInput
	Namespace          string
	ServiceAccount     string
	ClusterRole        string
	ClusterRoleBinding string
	SecretData         string
	Secret             string
	Deployment         string
	DeploymentApp      string
}
