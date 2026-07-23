package whichkey

// whichKeyState tracks the panel's current state in its state machine.
type whichKeyState int

const (
	stateIdle   whichKeyState = iota // panel closed
	stateRoot                        // panel open at root level
	statePrefix                      // panel open in a prefix sub-menu
)
