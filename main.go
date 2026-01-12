package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"

	l "github.com/sprisa/x/log"
)

func main() {
	if len(os.Args) < 2 {
		printAppHelp()
		return
	}
	cmd := os.Args[1]
	ctx := context.Background()

	err := handleCmd(ctx, cmd)
	if err != nil {
		l.Log.Error().Msg(err.Error())
		defer os.Exit(1)
	}
}

func handleCmd(ctx context.Context, cmd string) error {
	switch cmd {
	case "up", "down", "restart":
		if len(os.Args) >= 3 {
			arg := os.Args[2]
			if arg == "help" {
				printCmdHelp(cmd)
				return nil
			}
		}

		return runComposeCmd(ctx, cmd, os.Args[2:])
	case "help":
		printAppHelp()
		return nil
	default:
		return fmt.Errorf("command `%s` not supported", cmd)
	}
}

func printAppHelp() {
	fmt.Print(`Composure - Calm docker compose deployments

Usage: composure <command>

Commands:
  up       - Start services
  down     - Stop services
  restart  - Restart services
  help     - Show this help message
`)
}

func printCmdHelp(cmd string) {
	shell := exec.Command("docker", "compose", cmd, "--help")
	out, _ := shell.CombinedOutput()

	firstNewline := max(bytes.IndexRune(out, '\n'), 0)

	fmt.Printf(`Usage: composure %s [docker compose options]

%s
`, cmd, bytes.TrimSpace(out[firstNewline:]))
}
