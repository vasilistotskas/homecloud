package main

import (
	"bufio"
	"errors"
	"fmt"

	"golang.org/x/term"
)

// readSecret reads a secret the owner pastes: without echo from a terminal,
// after prompt, or as a line of standard input. what names it in errors.
func readSecret(c *call, prompt, what string) (string, error) {
	if !term.IsTerminal(stdinFD()) {
		sc := bufio.NewScanner(c.stdin)
		if !sc.Scan() {
			return "", errors.Join(fmt.Errorf("no %s on standard input", what), sc.Err())
		}
		return sc.Text(), nil
	}
	if err := printf(c, "%s (not shown): ", prompt); err != nil {
		return "", err
	}
	b, err := term.ReadPassword(stdinFD())
	if err != nil {
		return "", fmt.Errorf("read %s: %w", what, err)
	}
	return string(b), printf(c, "\n")
}
