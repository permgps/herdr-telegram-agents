package app

import (
	"fmt"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"html"
	"strings"
)

func renderHome() view {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>\n%s\n", panelTitle, panelPickGroup)
	groups := domain.OptionGroups()
	buttons := make([]domain.Button, 0, len(groups)+1)
	for i, g := range groups {
		fmt.Fprintf(&b, "\n<b>%s</b>: %s", html.EscapeString(g.Title), html.EscapeString(g.Description))
		buttons = append(buttons, domain.Button{Text: g.Title, Data: dataGroup(i)})
	}
	fmt.Fprint(&b, "\n\n<b>Plugin</b>: Check GitHub for a new release. Installation needs a second press.")
	buttons = append(buttons, domain.Button{Text: "Check for updates", Data: updateCheckData})
	buttons = append(buttons, domain.Button{Text: panelClose, Data: dataClose()})
	return view{b.String(), buttons}
}

// renderGroup lists one group's options with descriptions and values; the
// buttons toggle bools, open grids for choices, and navigate.
func renderGroup(gi int, opts domain.Options) view {
	g, ok := groupAt(gi)
	if !ok {
		return renderHome()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>\n<i>%s</i>\n", html.EscapeString(g.Title), html.EscapeString(g.Description))
	specs := domain.OptionsInGroup(g.Name)
	buttons := make([]domain.Button, 0, len(specs)+3)
	for _, spec := range specs {
		value := opts.String(spec.Key)
		fmt.Fprintf(&b, "\n<b>%s</b>: %s\n%s: %s", html.EscapeString(spec.Title), html.EscapeString(spec.Description),
			panelCurrent, html.EscapeString(displayValue(spec, value)))
		switch spec.Kind {
		case domain.KindBool:
			box := "☐"
			if value == "true" {
				box = "☑"
			}
			buttons = append(buttons, domain.Button{Text: box + " " + spec.Title, Data: dataToggle(spec.Key)})
		case domain.KindChoice:
			buttons = append(buttons, domain.Button{Text: domain.ChoiceLabel(spec, value) + " " + spec.Title, Data: dataGrid(spec.Key, 0)})
		default:
			buttons = append(buttons, domain.Button{Text: spec.Title + ": " + value, Data: dataText(spec.Key)})
		}
	}
	buttons = append(buttons,
		domain.Button{Text: panelReset, Data: dataReset(gi)},
		domain.Button{Text: panelBack, Data: dataHome()},
		domain.Button{Text: panelClose, Data: dataClose()})
	return view{b.String(), buttons}
}

// renderGrid draws the choice list for spec with the current value
// bracketed and a Back button: the paged emoji grid for the icon pack, one
// row of short labels for every other source.
func renderGrid(spec domain.OptionSpec, page int, opts domain.Options, pack []string) view {
	if spec.Choices == domain.ChoiceSourceIcons {
		return renderIconGrid(spec, page, opts, pack)
	}
	current := strings.TrimSpace(opts.String(spec.Key))
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>\n%s\n%s: %s", html.EscapeString(spec.Title), html.EscapeString(spec.Description),
		panelCurrent, html.EscapeString(domain.ChoiceLabel(spec, current)))
	buttons := make([]domain.Button, 0, len(pack)+1)
	for i, value := range pack {
		label := domain.ChoiceButton(spec, value)
		if strings.TrimSpace(value) == current {
			label = "[" + label + "]"
		}
		buttons = append(buttons, domain.Button{Text: label, Data: dataPick(spec.Key, i), Row: 1 + i/iconGridCols})
	}
	return view{b.String(), append(buttons, domain.Button{Text: panelBack, Data: dataGroup(groupIndex(spec.Group))})}
}

// renderIconGrid draws one page of the topic-icon pack, the current value
// bracketed, with a navigation row and Back.
func renderIconGrid(spec domain.OptionSpec, page int, opts domain.Options, pack []string) view {
	current := opts.String(spec.Key)
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s %s</b>\n%s\n%s\n%s: %s", panelIconFor, html.EscapeString(spec.Title),
		html.EscapeString(spec.Description), panelOnlyPackIcons, panelCurrent, html.EscapeString(current))
	back := domain.Button{Text: panelBack, Data: dataGroup(groupIndex(spec.Group))}
	if len(pack) == 0 {
		b.WriteString("\n\n" + panelPackUnavailable)
		return view{b.String(), []domain.Button{back}}
	}
	perPage := iconGridCols * iconGridRows
	pages := (len(pack) + perPage - 1) / perPage
	if page >= pages {
		page = pages - 1
	}
	start := page * perPage
	end := start + perPage
	if end > len(pack) {
		end = len(pack)
	}
	buttons := make([]domain.Button, 0, end-start+4)
	for i := start; i < end; i++ {
		label := pack[i]
		if sameEmoji(label, current) {
			label = "[" + label + "]"
		}
		buttons = append(buttons, domain.Button{Text: label, Data: dataPick(spec.Key, i), Row: 1 + (i-start)/iconGridCols})
	}
	if pages > 1 {
		nav := iconGridRows + 1
		prev := domain.Button{Text: "·", Data: dataNoop(), Row: nav}
		if page > 0 {
			prev = domain.Button{Text: "‹", Data: dataGrid(spec.Key, page-1), Row: nav}
		}
		next := domain.Button{Text: "·", Data: dataNoop(), Row: nav}
		if page < pages-1 {
			next = domain.Button{Text: "›", Data: dataGrid(spec.Key, page+1), Row: nav}
		}
		buttons = append(buttons, prev, domain.Button{Text: fmt.Sprintf("%d/%d", page+1, pages), Data: dataNoop(), Row: nav}, next)
	}
	return view{b.String(), append(buttons, back)}
}

// renderSummary is the text left behind when the panel is closed.
func renderSummary(opts domain.Options) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>", panelTitle)
	for _, g := range domain.OptionGroups() {
		fmt.Fprintf(&b, "\n\n<b>%s</b>", html.EscapeString(g.Title))
		for _, spec := range domain.OptionsInGroup(g.Name) {
			fmt.Fprintf(&b, "\n%s: %s", html.EscapeString(spec.Title), html.EscapeString(displayValue(spec, opts.String(spec.Key))))
		}
	}
	return b.String()
}

func displayValue(spec domain.OptionSpec, value string) string {
	if spec.Kind == domain.KindChoice {
		return domain.ChoiceLabel(spec, value)
	}
	if spec.Kind != domain.KindBool {
		return value
	}
	if value == "true" {
		return panelOn
	}
	return panelOff
}

func sameEmoji(a, b string) bool {
	return strings.ReplaceAll(a, "️", "") == strings.ReplaceAll(b, "️", "")
}
