package main

import "charm.land/bubbles/v2/key"

type keyMap struct {
	Search key.Binding
	Next   key.Binding
	Prev   key.Binding
	Follow key.Binding
	Top    key.Binding
	Bottom key.Binding
	Wrap   key.Binding
	Filter key.Binding
	Save   key.Binding
	Clear  key.Binding
	Close  key.Binding
	Help   key.Binding
	Quit   key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Search: key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
		Next:   key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next match")),
		Prev:   key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("⇧tab", "prev match")),
		Follow: key.NewBinding(key.WithKeys("F"), key.WithHelp("F", "follow")),
		Top:    key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "top")),
		Bottom: key.NewBinding(key.WithKeys("G"), key.WithHelp("G", "bottom")),
		Wrap:   key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "wrap")),
		Filter: key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "filter")),
		Save:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "save")),
		Clear:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear")),
		Close:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("↵", "close search")),
		Help:   key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:   key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

// Mode-specific help projections of the same keymap.
type normalKeys keyMap

func (k normalKeys) ShortHelp() []key.Binding {
	return []key.Binding{k.Search, k.Next, k.Filter, k.Follow, k.Help, k.Quit}
}
func (k normalKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Search, k.Next, k.Prev},
		{k.Filter, k.Follow, k.Top, k.Bottom},
		{k.Wrap, k.Save, k.Help, k.Quit},
	}
}

type searchKeys keyMap

func (k searchKeys) ShortHelp() []key.Binding {
	return []key.Binding{k.Next, k.Prev, k.Clear, k.Close, k.Help}
}
func (k searchKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Next, k.Prev},
		{k.Clear, k.Close, k.Help},
	}
}
