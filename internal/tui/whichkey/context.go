package whichkey

// KeyContextProvider is implemented by each view to supply its
// which-key command catalog. The which-key panel calls these
// methods when Tab is pressed on the view's screen.
type KeyContextProvider interface {
	WhichKeyCommands() []CommandGroup
	IsTextInputActive() bool // true when a text input has focus (Tab should not open which-key)
}
