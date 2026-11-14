package creds

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// TTYPrompter asks on the controlling terminal with a structured form and
// hidden input. It works even when stdin/stdout are redirected, as long as
// the process has a terminal.
type TTYPrompter struct{}

// TTYAvailable reports whether a controlling terminal can be opened.
func TTYAvailable() bool {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

func (TTYPrompter) Prompt(ctx context.Context, target string, req Request) (string, bool, bool, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", false, false, err
	}
	defer tty.Close()

	fmt.Fprintf(tty, "\n┌─ 🔑 Credential needed to keep testing %s\n", target)
	fmt.Fprintf(tty, "│  %s\n", req.Name)
	if req.Description != "" {
		fmt.Fprintf(tty, "│  %s\n", oneLine(req.Description))
	}
	if req.URL != "" {
		fmt.Fprintf(tty, "│  Get one: %s\n", req.URL)
	}
	fmt.Fprintf(tty, "│  It is set only inside the throwaway test container and redacted from logs and reports.\n")
	fmt.Fprintf(tty, "└─ Value (input hidden, Enter to skip): ")

	value, err := readHidden(ctx, tty)
	fmt.Fprintln(tty)
	if err != nil {
		return "", false, false, err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		fmt.Fprintf(tty, "   Skipped %s; steps that need it will be reported as blocked.\n", req.Name)
		return "", false, false, nil
	}
	fmt.Fprintf(tty, "   Save %s for future runs? [y/N]: ", req.Name)
	ans, _ := bufio.NewReader(tty).ReadString('\n')
	save := strings.HasPrefix(strings.ToLower(strings.TrimSpace(ans)), "y")
	return value, save, true, nil
}

// readHidden reads a line from tty with echo turned off via stty.
func readHidden(ctx context.Context, tty *os.File) (string, error) {
	off := exec.CommandContext(ctx, "stty", "-echo")
	off.Stdin = tty
	echoOff := off.Run() == nil
	if echoOff {
		defer func() {
			on := exec.Command("stty", "echo")
			on.Stdin = tty
			_ = on.Run()
		}()
	}
	line, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return line, nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
