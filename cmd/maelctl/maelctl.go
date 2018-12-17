package main

import (
	"encoding/json"
	"fmt"
	"github.com/coopernurse/barrister-go"
	"github.com/docopt/docopt-go"
	"github.com/dustin/go-humanize"
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"io/ioutil"
	"os"
	"strings"
	"time"
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
		fmt.Printf("Updating component: %s with current version: %d\n", input.Name, getOut.Component.Version)
		input.PreviousVersion = getOut.Component.Version
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

func componentLs(args docopt.Opts, svc v1.MaelstromService) {
	input := v1.ListComponentsInput{
		NamePrefix: strings.TrimSpace(argStr(args, "--prefix")),
	}

	first := true
	var nextToken string
	for {
		input.NextToken = nextToken
		output, err := svc.ListComponents(input)
		checkErr(err, "ListComponents failed")
		for _, c := range output.Components {
			if first {
				first = false
				fmt.Printf("%-20s  %-30s  %-5s  %-10s\n", "Component Name", "Image", "Ver", "Last Modified")
				fmt.Printf("--------------------------------------------------------------------------\n")
			}
			mod := humanize.Time(time.Unix(0, c.ModifiedAt*1e6))
			fmt.Printf("%-20s  %-30s  %-5d  %-13s\n", trunc(c.Name, 20), trunc(c.Docker.Image, 20), c.Version, mod)
		}
		nextToken = output.NextToken
		if output.NextToken == "" {
			break
		}
	}
	if first {
		fmt.Println("No components found")
	}
}

func componentRm(args docopt.Opts, svc v1.MaelstromService) {
	out, err := svc.RemoveComponent(v1.RemoveComponentInput{Name: argStr(args, "<name>")})
	checkErr(err, "RemoveComponent failed")
	if out.Found {
		fmt.Printf("Component removed: %s\n", out.Name)
	} else {
		fmt.Printf("Component not found: %s\n", out.Name)
	}

}

////////////////////////////////////

func trunc(s string, max int) string {
	if len(s) > max {
		return s[0:max]
	}
	return s
}

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
		componentLs(args, svc)
	} else if argBool(args, "comp") && argBool(args, "rm") {
		componentRm(args, svc)
	} else {
		fmt.Printf("ERROR: unsupported command. args=%v\n", args)
		os.Exit(2)
	}
}
