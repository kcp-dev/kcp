/*
Copyright 2025 The KCP Authors.

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
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/kcp-dev/logicalcluster/v3"
)

type treeNode struct {
	name       string
	path       logicalcluster.Path
	info       *workspaceInfo
	expanded   bool
	selected   bool
	selectable bool
	children   []*treeNode
	parent     *treeNode
	depth      int
}

type model struct {
	tree              *treeNode
	currentNode       *treeNode
	currentIndex      int
	selectedWorkspace *logicalcluster.Path
	width             int
	height            int
	visibleNodesCache []*treeNode
	cacheValid        bool
}

func (m *model) invalidateCache() {
	m.cacheValid = false
}

func (m *model) getVisibleNodes() []*treeNode {
	if !m.cacheValid {
		m.visibleNodesCache = nil
		m.collectVisibleNodes(m.tree, &m.visibleNodesCache)
		m.cacheValid = true

		for i, node := range m.visibleNodesCache {
			if node == m.currentNode {
				m.currentIndex = i
				break
			}
		}
	}
	return m.visibleNodesCache
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit

		case "up", "k":
			m.moveToPrevious()
			return m, nil

		case "down", "j":
			m.moveToNext()
			return m, nil

		case "right", "l", " ":
			if m.currentNode != nil && !m.currentNode.expanded && len(m.currentNode.children) > 0 {
				m.currentNode.expanded = true
				m.invalidateCache()
			}
			return m, nil

		case "left", "h", "backspace":
			if m.currentNode != nil && m.currentNode.expanded {
				m.currentNode.expanded = false
				m.invalidateCache()
			}
			return m, nil

		case "enter":
			if m.currentNode != nil && m.currentNode.info != nil {
				m.selectedWorkspace = &m.currentNode.info.Path
				return m, tea.Quit
			}
			return m, nil
		}
	}

	return m, nil
}

func (m model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("170"))

	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Padding(1, 2).
		Width(m.width - 4)

	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241"))

	detailStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("248")).
		Align(lipgloss.Right)

	title := titleStyle.Render("kcp Workspaces")

	var details string
	if m.currentNode != nil && m.currentNode.info != nil {
		detailParts := []string{m.currentNode.info.Path.String()}
		if m.currentNode.info.Type != nil {
			typeRef := string(m.currentNode.info.Type.Name)
			if m.currentNode.info.Type.Path != "" {
				typeRef = m.currentNode.info.Type.Path + ":" + typeRef
			}
			detailParts = append(detailParts, typeRef)
		}
		if m.currentNode.info.Cluster != "" {
			detailParts = append(detailParts, fmt.Sprintf("cluster:%s", m.currentNode.info.Cluster))
		}
		if len(m.currentNode.info.APIExports) > 0 {
			detailParts = append(detailParts, fmt.Sprintf("exports:%d", len(m.currentNode.info.APIExports)))
		}
		if len(m.currentNode.info.APIExportEndpointSlices) > 0 {
			detailParts = append(detailParts, fmt.Sprintf("endpointslices:%d", len(m.currentNode.info.APIExportEndpointSlices)))
		}
		if len(m.currentNode.info.APIBindings) > 0 {
			detailParts = append(detailParts, fmt.Sprintf("bindings:%d", len(m.currentNode.info.APIBindings)))
		}
		details = detailStyle.Render(strings.Join(detailParts, " | "))
	}

	headerWidth := m.width - 8
	titleWidth := lipgloss.Width(title)
	detailWidth := lipgloss.Width(details)
	spacerWidth := headerWidth - titleWidth - detailWidth
	if spacerWidth < 0 {
		spacerWidth = 0
	}

	header := lipgloss.JoinHorizontal(lipgloss.Top,
		title,
		strings.Repeat(" ", spacerWidth),
		details,
	)

	visibleNodes := m.getVisibleNodes()
	treeLines := make([]string, 0, len(visibleNodes)+2)
	treeLines = append(treeLines, "")

	for _, node := range visibleNodes {
		treeLines = append(treeLines, m.renderNode(node))
	}

	treeLines = append(treeLines, "")

	sectionStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		MarginTop(1)

	itemStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		MarginLeft(2)

	exportNameStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("39"))

	endpointSliceStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("135"))

	bindingNameStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("82"))

	bindingSourceStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("248")).
		Italic(true)

	boundResourceStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("220"))

	var apiDetails []string
	if m.currentNode != nil && m.currentNode.info != nil {
		var exportsColumn []string
		var slicesColumn []string
		var bindingsSection []string

		if len(m.currentNode.info.APIExports) > 0 || len(m.currentNode.info.APIExportEndpointSlices) > 0 {
			availableWidth := m.width - 12
			columnWidth := (availableWidth - 4) / 2

			if len(m.currentNode.info.APIExports) > 0 {
				exportsColumn = append(exportsColumn, sectionStyle.Render(fmt.Sprintf("APIExports (%d):", len(m.currentNode.info.APIExports))))
				for _, export := range m.currentNode.info.APIExports {
					exportsColumn = append(exportsColumn, itemStyle.Render(fmt.Sprintf("• %s", exportNameStyle.Render(export.Name))))
				}
			}

			if len(m.currentNode.info.APIExportEndpointSlices) > 0 {
				slicesColumn = append(slicesColumn, sectionStyle.Render(fmt.Sprintf("APIExportEndpointSlices (%d):", len(m.currentNode.info.APIExportEndpointSlices))))
				for _, slice := range m.currentNode.info.APIExportEndpointSlices {
					sliceLine := fmt.Sprintf("• %s", endpointSliceStyle.Render(slice.Name))
					if slice.Spec.APIExport.Name != "" {
						exportPath := slice.Spec.APIExport.Path
						if exportPath != "" {
							sliceLine += fmt.Sprintf(" → %s:%s", bindingSourceStyle.Render(exportPath), slice.Spec.APIExport.Name)
						} else {
							sliceLine += fmt.Sprintf(" → %s", slice.Spec.APIExport.Name)
						}
					}
					if len(slice.Status.APIExportEndpoints) > 0 {
						sliceLine += fmt.Sprintf(" (%d endpoints)", len(slice.Status.APIExportEndpoints))
					}
					slicesColumn = append(slicesColumn, itemStyle.Render(sliceLine))
				}
			}

			maxLines := len(exportsColumn)
			if len(slicesColumn) > maxLines {
				maxLines = len(slicesColumn)
			}

			for i := range maxLines {
				leftLine := ""
				rightLine := ""
				if i < len(exportsColumn) {
					leftLine = exportsColumn[i]
				}
				if i < len(slicesColumn) {
					rightLine = slicesColumn[i]
				}
				leftPadded := leftLine
				if lipgloss.Width(leftLine) < columnWidth {
					leftPadded = leftLine + strings.Repeat(" ", columnWidth-lipgloss.Width(leftLine))
				}
				combinedLine := lipgloss.JoinHorizontal(lipgloss.Top, leftPadded, strings.Repeat(" ", 4), rightLine)
				apiDetails = append(apiDetails, combinedLine)
			}
		}

		if len(m.currentNode.info.APIBindings) > 0 {
			bindingsSection = append(bindingsSection, sectionStyle.Render(fmt.Sprintf("APIBindings (%d):", len(m.currentNode.info.APIBindings))))
			for _, binding := range m.currentNode.info.APIBindings {
				bindingLine := fmt.Sprintf("• %s", bindingNameStyle.Render(binding.Name))
				if binding.Spec.Reference.Export != nil {
					exportPath := binding.Spec.Reference.Export.Path
					exportName := binding.Spec.Reference.Export.Name
					if exportPath != "" {
						bindingLine += fmt.Sprintf(" → %s:%s", bindingSourceStyle.Render(exportPath), exportName)
					} else {
						bindingLine += fmt.Sprintf(" → %s", bindingSourceStyle.Render(exportName))
					}
				}
				bindingsSection = append(bindingsSection, itemStyle.Render(bindingLine))
				if len(binding.Status.BoundResources) > 0 {
					for _, resource := range binding.Status.BoundResources {
						groupResource := resource.Resource
						if resource.Group != "" {
							groupResource = fmt.Sprintf("%s.%s", resource.Resource, resource.Group)
						}
						bindingsSection = append(bindingsSection, itemStyle.Render(fmt.Sprintf("  └─ %s", boundResourceStyle.Render(groupResource))))
					}
				}
			}
			apiDetails = append(apiDetails, bindingsSection...)
		}
	}

	help := helpStyle.Render("↑/↓: navigate  →/←: expand/collapse  q: quit")

	var prompt string
	if m.currentNode != nil && m.currentNode.info != nil {
		prompt = lipgloss.NewStyle().
			Foreground(lipgloss.Color("34")).
			Render("\nPress Enter to switch to this workspace")
	}

	var contentParts []string
	contentParts = append(contentParts, header, strings.Join(treeLines, "\n"))

	if len(apiDetails) > 0 {
		contentParts = append(contentParts, strings.Repeat("─", m.width-8), strings.Join(apiDetails, "\n"))
	}

	contentParts = append(contentParts, help, prompt)

	content := lipgloss.JoinVertical(lipgloss.Left, contentParts...)

	return borderStyle.Render(content)
}

func (m *model) renderNode(node *treeNode) string {
	prefix := strings.Repeat("  ", node.depth)

	icon := "  "
	if len(node.children) > 0 {
		if node.expanded {
			icon = "▼ "
		} else {
			icon = "▶ "
		}
	}

	nodeName := prefix + icon + node.name
	line := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Render("  " + nodeName)

	if m.currentNode == node {
		if node.selected {
			line = lipgloss.NewStyle().
				Foreground(lipgloss.Color("11")).
				Background(lipgloss.Color("235")).
				Bold(true).
				Render("→ " + nodeName)
		} else {
			line = lipgloss.NewStyle().
				Background(lipgloss.Color("235")).
				Render("→ " + nodeName)
		}
	} else if node.selected {
		line = lipgloss.NewStyle().
			Foreground(lipgloss.Color("11")).
			Render("  " + nodeName)
	}

	return line
}

func (m *model) moveToPrevious() {
	allNodes := m.getVisibleNodes()
	if m.currentIndex > 0 {
		m.currentIndex--
		m.currentNode = allNodes[m.currentIndex]
	}
}

func (m *model) moveToNext() {
	allNodes := m.getVisibleNodes()
	if m.currentIndex < len(allNodes)-1 {
		m.currentIndex++
		m.currentNode = allNodes[m.currentIndex]
	}
}

func (m *model) collectVisibleNodes(node *treeNode, nodes *[]*treeNode) {
	if node == nil {
		return
	}

	*nodes = append(*nodes, node)

	if node.expanded {
		for _, child := range node.children {
			child.depth = node.depth + 1
			m.collectVisibleNodes(child, nodes)
		}
	}
}
