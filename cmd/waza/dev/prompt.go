package dev

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// PromptConfirm asks a yes/no question. Returns true for yes.
func PromptConfirm(s *bufio.Scanner, w io.Writer, question string) bool {
	if _, err := fmt.Fprint(w, question+" [y/N]"); err != nil {
		panic("error writing prompt: " + err.Error())
	}

	if !s.Scan() {
		return false
	}

	input := strings.TrimSpace(s.Text())
	if len(input) == 0 {
		return false
	}

	return strings.ToLower(input[:1]) == "y"
}
