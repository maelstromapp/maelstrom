package main

import (
	"fmt"
	"github.com/docopt/docopt-go"
	"os"
)

////////////////////////////////////

func componentPut(args docopt.Opts) {
	fmt.Println("componentPut here", args)
}

func componentLs(args docopt.Opts) {
	fmt.Println("componentLs here", args)
}

func componentRm(args docopt.Opts) {
	fmt.Println("componentRm here", args)
}

////////////////////////////////////

func argBool(args docopt.Opts, key string) bool {
	b, _ := args.Bool(key)
	return b
}

func main() {
	usage := `maelctl - Maelstrom Command Line Tool

Usage:
  maelctl comp put <name> [--file=<file>] [--json=<json>]
  maelctl comp ls [--prefix=<prefix>]
  maelctl comp rm <name>
`
	args, err := docopt.ParseDoc(usage)
	if err != nil {
		fmt.Printf("ERROR parsing arguments: %v\n", err)
		os.Exit(1)
	}

	if argBool(args, "comp") && argBool(args, "put") {
		componentPut(args)
	} else if argBool(args, "comp") && argBool(args, "ls") {
		componentLs(args)
	} else if argBool(args, "comp") && argBool(args, "rm") {
		componentRm(args)
	} else {
		fmt.Printf("ERROR: unsupported command. args=%v\n", args)
		os.Exit(2)
	}
}
