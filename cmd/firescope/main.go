package main

import (
	"fmt"
	"io"
	"os"
)

const version = "firescope 1.0.0"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" {
		writeHelp(stdout)
		return 0
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "process":
		return runProcess(args[1:], stdin, stdout, stderr)
	case "snapshot":
		return runSnapshot(args[1:], stdin, stdout, stderr)
	case "inspect":
		return runInspect(args[1:], stdout, stderr)
	case "list":
		return runList(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "firescope: unknown command %q\n", args[0])
		fmt.Fprintln(stderr, "Run 'firescope help' for usage.")
		return 2
	}
}

func writeHelp(w io.Writer) {
	fmt.Fprint(w, `FireScope - offline satellite thermal-anomaly processing

Usage:
  firescope process  [flags]   ingest, fuse, track, alert, and report
  firescope snapshot [flags]   atomically save an orbit-batch snapshot
  firescope inspect  [flags]   read and verify a snapshot
  firescope list     [flags]   list stored snapshots
  firescope version            print version
  firescope help               show this help

Use "firescope <command> --help" for command flags.
`)
}

func wantsHelp(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

func commandError(stderr io.Writer, command string, err error, usage bool) int {
	fmt.Fprintf(stderr, "firescope %s: %v\n", command, err)
	if usage {
		fmt.Fprintf(stderr, "Run 'firescope %s --help' for usage.\n", command)
		return 2
	}
	return 1
}
