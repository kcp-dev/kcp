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
}

type model struct {
	tree              *treeNode
	currentNode       *treeNode
	selectedWorkspace *logicalcluster.Path
	width             int
	height            int
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
			}
			return m, nil

		case "left", "h", "backspace":
			if m.currentNode != nil && m.currentNode.expanded {
				m.currentNode.expanded = false
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

	var treeLines []string
	treeLines = append(treeLines, "")
	m.flattenTree(m.tree, 0, &treeLines)
	treeLines = append(treeLines, "")

	help := helpStyle.Render("↑/↓: navigate  →/←: expand/collapse  q: quit")

	var prompt string
	if m.currentNode != nil && m.currentNode.info != nil {
		prompt = lipgloss.NewStyle().
			Foreground(lipgloss.Color("34")).
			Render("\nPress Enter to switch to this workspace")
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		header,
		strings.Join(treeLines, "\n"),
		help,
		prompt,
	)

	return borderStyle.Render(content)
}

func (m *model) flattenTree(node *treeNode, depth int, lines *[]string) {
	if node == nil {
		return
	}

	prefix := strings.Repeat("  ", depth)

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

	*lines = append(*lines, line)

	if node.expanded {
		for _, child := range node.children {
			m.flattenTree(child, depth+1, lines)
		}
	}
}

func (m *model) moveToPrevious() {
	if m.currentNode == nil {
		return
	}

	var allNodes []*treeNode
	m.collectVisibleNodes(m.tree, &allNodes)

	for i, node := range allNodes {
		if node == m.currentNode && i > 0 {
			m.currentNode = allNodes[i-1]
			return
		}
	}
}

func (m *model) moveToNext() {
	if m.currentNode == nil {
		return
	}

	var allNodes []*treeNode
	m.collectVisibleNodes(m.tree, &allNodes)

	for i, node := range allNodes {
		if node == m.currentNode && i < len(allNodes)-1 {
			m.currentNode = allNodes[i+1]
			return
		}
	}
}

func (m *model) collectVisibleNodes(node *treeNode, nodes *[]*treeNode) {
	if node == nil {
		return
	}

	*nodes = append(*nodes, node)

	if node.expanded {
		for _, child := range node.children {
			m.collectVisibleNodes(child, nodes)
		}
	}
}
