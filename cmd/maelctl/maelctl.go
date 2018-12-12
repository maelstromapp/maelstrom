package main

import (
	"encoding/json"
	"fmt"
	"github.com/coopernurse/barrister-go"
	"github.com/docopt/docopt-go"
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"io/ioutil"
	"os"
)

////////////////////////////////////

func componentPut(args docopt.Opts, svc v1.MaelstromService) {
	var input v1.PutComponentInput
	var data []byte
	var err error
	j := argStr(args, "--json")
	if j == "" {
		fname := argStr(args, "--file")
		if fname == "" {
			fmt.Printf("ERROR: comp put requires the --json or --file argument\n")
			os.Exit(1)
		} else {
			data, err = ioutil.ReadFile(fname)
			checkErr(err, "Unable to read file: "+fname)
		}
	} else {
		data = []byte(j)
	}

	err = json.Unmarshal(data, &input)
	checkErr(err, "Unable to parse JSON for PutComponentInput")
	input.Name = argStr(args, "<name>")

	// check for existing component and set previousVersion
	getInput := v1.GetComponentInput{Name: input.Name}
	getOut, err := svc.GetComponent(getInput)
	if err == nil {
		fmt.Printf("Updating component: %s with current version: %d\n", input.Name, getOut.Version)
		input.PreviousVersion = getOut.Version
	} else {
		rpcErr, ok := err.(*barrister.JsonRpcError)
		if ok && rpcErr.Code == 1003 {
			// ok - new component
			fmt.Printf("Registering new component: %s\n", input.Name)
		} else {
			checkErr(err, "GetComponent failed for: "+input.Name)
		}
	}

	// store new component data
	out, err := svc.PutComponent(input)
	checkErr(err, "PutComponent failed")
	fmt.Printf("Success  component: %s updated to version: %d\n", out.Name, out.Version)
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

func argStr(args docopt.Opts, key string) string {
	s, _ := args.String(key)
	return s
}

func checkErr(err error, msg string) {
	if err != nil {
		fmt.Printf("ERROR: %s - %v\n", msg, err)
		os.Exit(1)
	}
}

func newMaelstromServiceClient(url string) v1.MaelstromService {
	trans := &barrister.HttpTransport{Url: url}
	client := barrister.NewRemoteClient(trans, true)
	return v1.NewMaelstromServiceProxy(client)
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

	url := os.Getenv("MAEL_ADMIN_URL")
	if url == "" {
		url = "http://127.0.0.1:8374/v1"
	}
	svc := newMaelstromServiceClient(url)

	if argBool(args, "comp") && argBool(args, "put") {
		componentPut(args, svc)
	} else if argBool(args, "comp") && argBool(args, "ls") {
		componentLs(args)
	} else if argBool(args, "comp") && argBool(args, "rm") {
		componentRm(args)
	} else {
		fmt.Printf("ERROR: unsupported command. args=%v\n", args)
		os.Exit(2)
	}
}
