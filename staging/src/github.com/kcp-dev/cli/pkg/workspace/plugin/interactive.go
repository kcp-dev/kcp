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
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pluginhelpers "github.com/kcp-dev/cli/pkg/helpers"
	"github.com/kcp-dev/logicalcluster/v3"
)

const (
	keyQuit      = "q"
	keyCtrlC     = "ctrl+c"
	keyEsc       = "esc"
	keyUp        = "up"
	keyUpAlt     = "k"
	keyDown      = "down"
	keyDownAlt   = "j"
	keyRight     = "right"
	keyRightAlt  = "l"
	keyLeft      = "left"
	keyLeftAlt   = "h"
	keyBackspace = "backspace"
	keySpace     = " "
	keyEnter     = "enter"

	textLoading         = "Loading..."
	textTitle           = "kcp Workspaces"
	textHelp            = "↑/↓: navigate  →/←/Space: expand/collapse  q: quit"
	textPrompt          = "Press Enter to switch to this workspace"
	textScrollIndicator = "  ..."
	iconExpanded        = "▼ "
	iconCollapsed       = "▶ "
	iconCurrent         = "→ "
	iconEmpty           = "  "

	borderWidthReduction     = 4
	headerWidthReduction     = 8
	apiDetailsWidthReduction = 12
	columnSpacing            = 4
	itemMarginLeft           = 2
	itemMarginTop            = 1
	borderPaddingHorizontal  = 2
	borderPaddingVertical    = 1

	colorTitle         = "170"
	colorBorder        = "62"
	colorHelp          = "241"
	colorDetail        = "248"
	colorSection       = "245"
	colorItem          = "252"
	colorExport        = "39"
	colorEndpointSlice = "135"
	colorBinding       = "82"
	colorBindingSource = "248"
	colorBoundResource = "220"
	colorPrompt        = "34"
	colorSelected      = "11"
	colorCurrentBg     = "235"

	childrenCheckLimit = 1
)

type treeNode struct {
	name           string
	path           logicalcluster.Path
	info           *workspaceInfo
	expanded       bool
	selected       bool
	selectable     bool
	children       []*treeNode
	parent         *treeNode
	depth          int
	childrenLoaded bool
	apiInfoLoaded  bool
	hasChildren    bool
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
	treeOptions       *TreeOptions
	yOffset           int
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
		case keyQuit, keyCtrlC, keyEsc:
			return m, tea.Quit

		case keyUp, keyUpAlt:
			m.moveToPrevious()
			if m.currentNode != nil && m.currentNode.info != nil && !m.currentNode.apiInfoLoaded {
				m.loadAPIInfo(m.currentNode)
			}
			return m, nil

		case keyDown, keyDownAlt:
			m.moveToNext()
			if m.currentNode != nil && m.currentNode.info != nil && !m.currentNode.apiInfoLoaded {
				m.loadAPIInfo(m.currentNode)
			}
			return m, nil

		case keyRight, keyRightAlt:
			if m.currentNode != nil && !m.currentNode.expanded {
				if !m.currentNode.childrenLoaded {
					m.loadChildren(m.currentNode)
				}
				if m.currentNode.hasChildren {
					m.currentNode.expanded = true
					m.invalidateCache()
				}
			}
			return m, nil

		case keyLeft, keyLeftAlt, keyBackspace:
			if m.currentNode != nil && m.currentNode.expanded {
				m.currentNode.expanded = false
				m.invalidateCache()
			}
			return m, nil

		case keySpace:
			if m.currentNode != nil {
				if !m.currentNode.expanded {
					if !m.currentNode.childrenLoaded {
						m.loadChildren(m.currentNode)
					}
					if m.currentNode.hasChildren {
						m.currentNode.expanded = true
						m.invalidateCache()
					}
				} else {
					m.currentNode.expanded = false
					m.invalidateCache()
				}
			}
			return m, nil

		case keyEnter:
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
		return textLoading
	}

	if m.currentNode != nil && m.currentNode.info != nil && !m.currentNode.apiInfoLoaded {
		m.loadAPIInfo(m.currentNode)
	}

	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorBorder)).
		Padding(borderPaddingVertical, borderPaddingHorizontal).
		Width(m.width - borderWidthReduction)

	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorHelp))

	header := m.renderHeader()

	helpRendered := helpStyle.Render(textHelp)

	var promptRendered string
	if m.currentNode != nil && m.currentNode.info != nil {
		promptRendered = lipgloss.NewStyle().Foreground(lipgloss.Color(colorPrompt)).Render("\n" + textPrompt)
	}

	separator, apiDetailsSection := m.renderAPIDetails()

	chromeHeight := lipgloss.Height(header) +
		lipgloss.Height(helpRendered) +
		lipgloss.Height(promptRendered) +
		lipgloss.Height(separator) +
		lipgloss.Height(apiDetailsSection) +
		(borderPaddingVertical * 2) + 2

	maxTreeRows := m.height - chromeHeight
	if maxTreeRows < 3 {
		maxTreeRows = 3
	}

	visibleNodes := m.getVisibleNodes()
	count := len(visibleNodes)

	if m.currentIndex < 0 {
		m.currentIndex = 0
	}
	if m.currentIndex >= count {
		m.currentIndex = count - 1
	}

	if m.currentIndex < m.yOffset {
		m.yOffset = m.currentIndex
	} else if m.currentIndex >= m.yOffset+maxTreeRows {
		m.yOffset = m.currentIndex - maxTreeRows + 1
	}

	if m.yOffset > count-maxTreeRows {
		m.yOffset = count - maxTreeRows
	}
	if m.yOffset < 0 {
		m.yOffset = 0
	}

	if m.yOffset > 0 && m.currentIndex == m.yOffset {
		m.yOffset--
	}
	if m.yOffset+maxTreeRows < count && m.currentIndex == m.yOffset+maxTreeRows-1 {
		m.yOffset++
	}

	startIdx := m.yOffset
	endIdx := startIdx + maxTreeRows
	if endIdx > count {
		endIdx = count
	}

	treeLines := make([]string, 0, maxTreeRows)
	for i := startIdx; i < endIdx; i++ {
		treeLines = append(treeLines, m.renderNode(visibleNodes[i]))
	}

	for len(treeLines) < maxTreeRows {
		treeLines = append(treeLines, "")
	}

	if startIdx > 0 && len(treeLines) > 0 {
		treeLines[0] = textScrollIndicator
	}
	if endIdx < count && len(treeLines) > 0 {
		treeLines[len(treeLines)-1] = textScrollIndicator
	}

	var contentParts []string
	contentParts = append(contentParts, header, strings.Join(treeLines, "\n"))

	if separator != "" {
		contentParts = append(contentParts, separator)
	}
	if apiDetailsSection != "" {
		contentParts = append(contentParts, apiDetailsSection)
	}

	contentParts = append(contentParts, helpRendered, promptRendered)

	return borderStyle.Render(lipgloss.JoinVertical(lipgloss.Left, contentParts...))
}

func (m *model) renderNode(node *treeNode) string {
	prefix := strings.Repeat("  ", node.depth)

	icon := iconEmpty
	if node.hasChildren || len(node.children) > 0 {
		if node.expanded {
			icon = iconExpanded
		} else {
			icon = iconCollapsed
		}
	}

	nodeName := prefix + icon + node.name
	line := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Render("  " + nodeName)

	if m.currentNode == node {
		if node.selected {
			line = lipgloss.NewStyle().
				Foreground(lipgloss.Color(colorSelected)).
				Background(lipgloss.Color(colorCurrentBg)).
				Bold(true).
				Render(iconCurrent + nodeName)
		} else {
			line = lipgloss.NewStyle().
				Background(lipgloss.Color(colorCurrentBg)).
				Render(iconCurrent + nodeName)
		}
	} else if node.selected {
		line = lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorSelected)).
			Render(iconEmpty + nodeName)
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

func (m *model) loadChildren(node *treeNode) {
	if node.childrenLoaded || m.treeOptions == nil {
		return
	}

	ctx := context.Background()
	workspaceName := node.name
	if node.parent != nil {
		if !m.treeOptions.Full {
			workspaceName = node.path.Base()
		}
	}

	results, err := m.treeOptions.kcpClusterClient.Cluster(node.path).TenancyV1alpha1().Workspaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		node.hasChildren = false
		node.childrenLoaded = true
		return
	}

	node.hasChildren = len(results.Items) > 0

	for _, ws := range results.Items {
		_, childPath, err := pluginhelpers.ParseClusterURL(ws.Spec.URL)
		if err != nil {
			continue
		}

		childName := ws.Name
		if m.treeOptions.Full {
			childName = workspaceName + ":" + childName
		}

		childWorkspaceInfo := &workspaceInfo{
			Path:    childPath,
			Type:    ws.Spec.Type,
			Cluster: ws.Spec.Cluster,
		}

		childResults, err := m.treeOptions.kcpClusterClient.Cluster(childPath).TenancyV1alpha1().Workspaces().List(ctx, metav1.ListOptions{Limit: childrenCheckLimit})
		hasChildren := err == nil && len(childResults.Items) > 0

		childNode := &treeNode{
			name:           childName,
			path:           childPath,
			info:           childWorkspaceInfo,
			selectable:     true,
			parent:         node,
			childrenLoaded: false,
			apiInfoLoaded:  false,
			hasChildren:    hasChildren,
		}

		node.children = append(node.children, childNode)
	}

	node.childrenLoaded = true
}

func (m *model) loadAPIInfo(node *treeNode) {
	if node.apiInfoLoaded || m.treeOptions == nil || node.info == nil {
		return
	}

	ctx := context.Background()
	workspace := node.path

	exportsList, err := m.treeOptions.kcpClusterClient.Cluster(workspace).ApisV1alpha2().APIExports().List(ctx, metav1.ListOptions{})
	if err == nil {
		node.info.APIExports = exportsList.Items
	}

	endpointSlicesList, err := m.treeOptions.kcpClusterClient.Cluster(workspace).ApisV1alpha1().APIExportEndpointSlices().List(ctx, metav1.ListOptions{})
	if err == nil {
		node.info.APIExportEndpointSlices = endpointSlicesList.Items
	}

	bindingsList, err := m.treeOptions.kcpClusterClient.Cluster(workspace).ApisV1alpha2().APIBindings().List(ctx, metav1.ListOptions{})
	if err == nil {
		node.info.APIBindings = bindingsList.Items
	}

	node.apiInfoLoaded = true
}

func (m *model) renderHeader() string {
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(colorTitle))

	detailStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorDetail)).
		Align(lipgloss.Right)

	title := titleStyle.Render(textTitle)

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

	headerWidth := m.width - headerWidthReduction
	titleWidth := lipgloss.Width(title)
	detailWidth := lipgloss.Width(details)
	spacerWidth := headerWidth - titleWidth - detailWidth
	if spacerWidth < 0 {
		spacerWidth = 0
	}

	return lipgloss.JoinHorizontal(lipgloss.Top,
		title,
		strings.Repeat(" ", spacerWidth),
		details,
	)
}

func (m *model) renderAPIDetails() (string, string) {
	if m.currentNode == nil || m.currentNode.info == nil {
		return "", ""
	}

	sectionStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorSection)).
		MarginTop(itemMarginTop)

	itemStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorItem)).
		MarginLeft(itemMarginLeft)

	exportNameStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorExport))

	endpointSliceStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorEndpointSlice))

	bindingNameStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorBinding))

	bindingSourceStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorBindingSource)).
		Italic(true)

	boundResourceStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorBoundResource))

	var apiDetails []string
	var apiDetailsSection string
	var separator string

	var exportsColumn []string
	var slicesColumn []string
	var bindingsSection []string

	if len(m.currentNode.info.APIExports) > 0 || len(m.currentNode.info.APIExportEndpointSlices) > 0 {
		availableWidth := m.width - apiDetailsWidthReduction
		columnWidth := (availableWidth - columnSpacing) / 2

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
			combinedLine := lipgloss.JoinHorizontal(lipgloss.Top, leftPadded, strings.Repeat(" ", columnSpacing), rightLine)
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

	if len(apiDetails) > 0 {
		separator = strings.Repeat("─", m.width-headerWidthReduction)
		apiDetailsSection = strings.Join(apiDetails, "\n")
	}

	return separator, apiDetailsSection
}
