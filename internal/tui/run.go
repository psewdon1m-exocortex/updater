package tui

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"updater/internal/console"
)

func Run(arguments []string, socket string) error {
	flags := flag.NewFlagSet("updater tui", flag.ContinueOnError)
	demo := flags.Bool("demo", false, "Explore synthetic data without service access")
	noColor := flags.Bool("no-color", os.Getenv("NO_COLOR") != "", "Use monochrome rendering")
	flags.StringVar(&socket, "socket", socket, "Local operator socket")
	flags.SetOutput(os.Stderr)
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: updater tui [--demo] [--no-color] [--socket PATH]")
	}
	if !*demo && os.Geteuid() != 0 {
		return errors.New("run sudo updater tui for host administration, or updater tui --demo to preview")
	}
	if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) || os.Getenv("TERM") == "dumb" {
		return errors.New("TUI requires an interactive terminal. Open an SSH terminal (ssh -t) or use updater status/jobs")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var backend console.Backend
	if *demo {
		backend = NewDemo()
	} else {
		client := console.NewClient(socket)
		defer client.Close()
		backend = client
	}
	_, err := tea.NewProgram(New(backend, ctx, *noColor, *demo), tea.WithContext(ctx), tea.WithAltScreen(), tea.WithFPS(15), tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout)).Run()
	if err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		return fmt.Errorf("terminal console: %w", err)
	}
	_, _ = io.WriteString(os.Stdout, "Console closed. Accepted operations continue in Updater.\n")
	return nil
}
