package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// pollInterval paces the OpList refresh while the model waits for an awaiting
// subject to appear, so the TUI works whether the agent asks before or after the
// human opens it.
const pollInterval = time.Second

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	headerStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	reasonStyle  = lipgloss.NewStyle().Faint(true)
	cursorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
	descStyle    = lipgloss.NewStyle().Faint(true)
	diffStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	feedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("117"))
	urgentStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	doneStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("46"))
	hintStyle    = lipgloss.NewStyle().Faint(true)
	waitingStyle = lipgloss.NewStyle().Faint(true)
	machineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("111")).Bold(true)
)

// render assembles the whole frame: header, the focused question (prompt,
// reasoning, options, diff, notes), the notification feed, and key hints.
func (m Model) render() string {
	if m.err != nil {
		return titleStyle.Render("cc-runtime") + "\n\n" +
			urgentStyle.Render("error: "+m.err.Error()) + "\n"
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("cc-runtime"))
	b.WriteString("  ")
	if !m.resolved {
		if m.waitNote != "" {
			b.WriteString(urgentStyle.Render(m.waitNote))
		} else {
			b.WriteString(waitingStyle.Render("waiting for an awaiting subject…"))
		}
		b.WriteString("\n\n")
		b.WriteString(m.renderRoster())
		b.WriteString(m.renderFeed())
		b.WriteString(m.renderHints())
		return b.String()
	}

	open := m.openCount()
	if m.meshEnabled() {
		b.WriteString(hintStyle.Render(fmt.Sprintf("%d open · %s · subject %s", open, m.machineLabel(), short(m.res.SubjectID))))
	} else {
		b.WriteString(hintStyle.Render(fmt.Sprintf("%d open · subject %s", open, short(m.res.SubjectID))))
	}
	b.WriteString("\n\n")

	q, ok := m.focused()
	if !ok {
		b.WriteString(doneStyle.Render(m.doneLine()))
		b.WriteString("\n\n")
		b.WriteString(m.renderFeed())
		b.WriteString(m.renderHints())
		return b.String()
	}

	b.WriteString(m.renderQuestion(q))
	b.WriteString("\n")
	b.WriteString(m.renderFeed())
	b.WriteString(m.renderHints())
	return b.String()
}

// renderQuestion draws one open ask: header chip, prompt, reasoning, the option
// list with [x]/[ ] selection markers, the diff hunk, and the notes field.
func (m Model) renderQuestion(q question) string {
	var b strings.Builder
	p := q.Payload
	if p.Header != "" {
		b.WriteString(headerStyle.Render("["+p.Header+"]") + " ")
	}
	if p.Prompt != "" {
		b.WriteString(p.Prompt)
	}
	b.WriteString("\n")
	if p.Reasoning != "" {
		b.WriteString(reasonStyle.Render(p.Reasoning))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	cursor := m.cursor[q.ID]
	set := m.selected[q.ID]
	for i, opt := range p.Options {
		mark := "[ ]"
		if set[opt.Label] {
			mark = "[x]"
		}
		line := fmt.Sprintf("%s %s", mark, opt.Label)
		if i == cursor && !m.notesFocused {
			line = cursorStyle.Render("› " + line)
		} else {
			line = "  " + line
		}
		b.WriteString(line)
		b.WriteString("\n")
		if opt.Description != "" {
			b.WriteString("    " + descStyle.Render(opt.Description) + "\n")
		}
	}

	if p.Diff != "" {
		b.WriteString("\n")
		b.WriteString(diffStyle.Render(p.Diff))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(m.notes.View())
	b.WriteString("\n")
	return b.String()
}

// doneLine is the all-answered banner; a remote subject that idled shows the
// release of its owning machine's gate.
func (m Model) doneLine() string {
	if m.remoteResolved() && m.idled {
		return "answered — released " + m.machineLabel()
	}
	return "all questions answered"
}

// renderRoster draws the mesh awaiting list while unresolved: each machine's
// subjects prefixed by its node label, unreachable peers marked. Empty (and so
// invisible) on the local-only path, keeping that render untouched.
func (m Model) renderRoster() string {
	if len(m.roster) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(hintStyle.Render("— mesh —"))
	b.WriteString("\n")
	for _, h := range m.roster {
		node := h.Node()
		if h.Err != nil {
			b.WriteString(machineStyle.Render(node) + "  " + descStyle.Render("unreachable"))
			b.WriteString("\n")
			continue
		}
		for _, s := range h.Subjects {
			row := fmt.Sprintf("%s  %s  %s", machineStyle.Render(node), short(s.SubjectID), s.Status)
			if s.Pending > 0 {
				row += fmt.Sprintf("  (%d open)", s.Pending)
			}
			b.WriteString(row)
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	return b.String()
}

// renderFeed draws the notification feed below the question, newest last.
func (m Model) renderFeed() string {
	if len(m.feed) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(hintStyle.Render("— notifications —"))
	b.WriteString("\n")
	for _, n := range m.feed {
		style := feedStyle
		if n.Urgency == "high" {
			style = urgentStyle
		}
		b.WriteString(style.Render("• " + n.Message))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

func (m Model) renderHints() string {
	_, hasQ := m.focused()
	if m.notesFocused {
		return hintStyle.Render("tab options · enter submit · ctrl+c quit")
	}
	if hasQ {
		return hintStyle.Render("↑/↓ move · space select · tab notes · enter submit · q quit")
	}
	return hintStyle.Render("q quit")
}

// short trims a subject id to its leading segment for the header.
func short(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}
