package app

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// panelAction is one decoded callback. Kinds: h home, g group, t toggle a
// bool, c open a choice grid page, v pick a choice by index, r reset a
// group, e a text option (no editor yet), x close, n no-op.
type panelAction struct {
	kind  byte
	group int
	key   string
	page  int
	index int
}

func dataHome() string             { return panelPrefix + "h" }
func dataClose() string            { return panelPrefix + "x" }
func dataNoop() string             { return panelPrefix + "n" }
func dataGroup(gi int) string      { return fmt.Sprintf("%sg:%d", panelPrefix, gi) }
func dataReset(gi int) string      { return fmt.Sprintf("%sr:%d", panelPrefix, gi) }
func dataToggle(key string) string { return panelPrefix + "t:" + key }
func dataText(key string) string   { return panelPrefix + "e:" + key }
func dataGrid(key string, page int) string {
	return fmt.Sprintf("%sc:%s:%d", panelPrefix, key, page)
}
func dataPick(key string, index int) string {
	return fmt.Sprintf("%sv:%s:%d", panelPrefix, key, index)
}

var errPanelData = errors.New("malformed panel data")

// parsePanelData decodes callback data written by the data* helpers.
func parsePanelData(data string) (panelAction, error) {
	if !strings.HasPrefix(data, panelPrefix) {
		return panelAction{}, errPanelData
	}
	parts := strings.Split(strings.TrimPrefix(data, panelPrefix), ":")
	if len(parts) == 0 || len(parts[0]) != 1 {
		return panelAction{}, errPanelData
	}
	a := panelAction{kind: parts[0][0]}
	num := func(s string) (int, error) {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return 0, errPanelData
		}
		return n, nil
	}
	var err error
	switch a.kind {
	case 'h', 'x', 'n':
		if len(parts) != 1 {
			return panelAction{}, errPanelData
		}
	case 'g', 'r':
		if len(parts) != 2 {
			return panelAction{}, errPanelData
		}
		a.group, err = num(parts[1])
	case 't', 'e':
		if len(parts) != 2 || parts[1] == "" {
			return panelAction{}, errPanelData
		}
		a.key = parts[1]
	case 'c':
		if len(parts) != 3 || parts[1] == "" {
			return panelAction{}, errPanelData
		}
		a.key = parts[1]
		a.page, err = num(parts[2])
	case 'v':
		if len(parts) != 3 || parts[1] == "" {
			return panelAction{}, errPanelData
		}
		a.key = parts[1]
		a.index, err = num(parts[2])
	default:
		return panelAction{}, errPanelData
	}
	if err != nil {
		return panelAction{}, err
	}
	return a, nil
}
