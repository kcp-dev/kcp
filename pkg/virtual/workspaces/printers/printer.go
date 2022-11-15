/*
Copyright 2021 The KCP Authors.

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

package printers

import (
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kprinters "k8s.io/kubernetes/pkg/printers"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func AddWorkspacePrintHandlers(h kprinters.PrintHandler) {
	workspaceColumnDefinitions := []metav1.TableColumnDefinition{
		{
			Name:        "Name",
			Type:        "string",
			Format:      "name",
			Description: metav1.ObjectMeta{}.SwaggerDoc()["name"],
			Priority:    0,
		},
		{
			Name:        "Type",
			Type:        "string",
			Description: "Type of the workspace",
			Priority:    0,
		},
		{
			Name:        "Phase",
			Type:        "string",
			Description: "The current phase (e.g. Scheduling, Initializing, Ready, Deleting)",
			Priority:    0,
		},
		{
			Name:        "URL",
			Type:        "string",
			Description: "URL to access the workspace",
			Priority:    0,
		},
	}

	if err := h.TableHandler(workspaceColumnDefinitions, printWorkspaceList); err != nil {
		panic(err)
	}
	if err := h.TableHandler(workspaceColumnDefinitions, printWorkspace); err != nil {
		panic(err)
	}
}

func printWorkspace(workspace *tenancyv1alpha1.ClusterWorkspace, options kprinters.GenerateOptions) ([]metav1.TableRow, error) {
	row := metav1.TableRow{
		Object: runtime.RawExtension{Object: workspace},
	}

	phase := workspace.Status.Phase
	if workspace.DeletionTimestamp != nil {
		phase = "Deleting"
	}
	row.Cells = append(row.Cells, workspace.Name, workspace.Spec.Type.Name, phase, workspace.Status.BaseURL)

	return []metav1.TableRow{row}, nil
}

func printWorkspaceList(list *tenancyv1alpha1.ClusterWorkspaceList, options kprinters.GenerateOptions) ([]metav1.TableRow, error) {
	sort.Sort(SortableWorkspaces(list.Items))
	rows := make([]metav1.TableRow, 0, len(list.Items))
	for i := range list.Items {
		r, err := printWorkspace(&list.Items[i], options)
		if err != nil {
			return nil, err
		}
		rows = append(rows, r...)
	}
	return rows, nil
}

// SortableWorkspaces is a list of workspaces that can be sorted
type SortableWorkspaces []tenancyv1alpha1.ClusterWorkspace

func (list SortableWorkspaces) Len() int {
	return len(list)
}

func (list SortableWorkspaces) Swap(i, j int) {
	list[i], list[j] = list[j], list[i]
}

func (list SortableWorkspaces) Less(i, j int) bool {
	return list[i].ObjectMeta.Name < list[j].ObjectMeta.Name
}
