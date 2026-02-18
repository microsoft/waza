package dev

import (
	"io"
	"os"

	"github.com/charmbracelet/huh"
	"golang.org/x/term"
)

// promptConfirm is a test hook for replacing the confirmation prompt in tests.
// Takes reader, writer, and question string. Returns true for yes.
var promptConfirm = defaultPromptConfirm

func defaultPromptConfirm(in io.Reader, out io.Writer, question string) bool {
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return false
	}

	var confirmed bool
	err := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title(question).
				Affirmative("Yes").
				Negative("No").
				Value(&confirmed),
		),
	).WithInput(in).WithOutput(out).Run()

	if err != nil {
		return false
	}
	return confirmed
}
