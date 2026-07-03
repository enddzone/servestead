package main

import tea "charm.land/bubbletea/v2"

func keyRunes(value string) tea.KeyMsg {
	return tea.KeyPressMsg(tea.Key{Text: value, Code: []rune(value)[0]})
}

func keyCode(code rune) tea.KeyMsg {
	return tea.KeyPressMsg(tea.Key{Code: code})
}

func keyMod(code rune, mod tea.KeyMod) tea.KeyMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Mod: mod})
}

func keyCtrl(code rune) tea.KeyMsg {
	return keyMod(code, tea.ModCtrl)
}
