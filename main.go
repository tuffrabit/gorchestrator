package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/tuffrabit/gorchestrator/internal/cli"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: gorchestrator <command> [args]")
		fmt.Fprintln(os.Stderr, "Commands: run, resume, serve, version")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		runCmd := flag.NewFlagSet("run", flag.ExitOnError)
		if err := cli.Run(runCmd, os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "run failed: %v\n", err)
			os.Exit(1)
		}
	case "resume":
		resumeCmd := flag.NewFlagSet("resume", flag.ExitOnError)
		if err := cli.Resume(resumeCmd, os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "resume failed: %v\n", err)
			os.Exit(1)
		}
	case "serve":
		serveCmd := flag.NewFlagSet("serve", flag.ExitOnError)
		if err := cli.Serve(serveCmd, os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "serve failed: %v\n", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println("gorchestrator v0.1.0")
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}
