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

func readJSON(args docopt.Opts, target interface{}) {
	var data []byte
	var err error
	j := argStr(args, "--json")
	if j == "" {
		fname := argStr(args, "--file")
		if fname == "" {
			fmt.Printf("ERROR: put requires the --json or --file argument\n")
			os.Exit(1)
		} else {
			data, err = ioutil.ReadFile(fname)
			checkErr(err, "Unable to read file: "+fname)
		}
	} else {
		data = []byte(j)
	}
	err = json.Unmarshal(data, target)
	checkErr(err, "Unable to parse JSON")
}

func componentPut(args docopt.Opts, svc v1.MaelstromService) {
	var comp v1.Component
	readJSON(args, &comp)

	// check for existing component and set previousVersion
	getInput := v1.GetComponentInput{Name: comp.Name}
	getOut, err := svc.GetComponent(getInput)
	if err == nil {
		fmt.Printf("Updating component: %s with current version: %d\n", comp.Name, getOut.Component.Version)
		comp.Version = getOut.Component.Version
	} else {
		rpcErr, ok := err.(*barrister.JsonRpcError)
		if ok && rpcErr.Code == 1003 {
			// ok - new component
			fmt.Printf("Registering new component: %s\n", comp.Name)
		} else {
			checkErr(err, "GetComponent failed for: "+comp.Name)
		}
	}

	// store new component data
	out, err := svc.PutComponent(v1.PutComponentInput{Component: comp})
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
				fmt.Printf("%-20s  %-30s  %-5s  %-10s\n", "Component", "Image", "Ver", "Last Modified")
				fmt.Printf("--------------------------------------------------------------------------\n")
			}
			mod := humanize.Time(time.Unix(0, c.ModifiedAt*1e6))
			imgName := ""
			if c.Docker != nil {
				imgName = c.Docker.Image
			}
			fmt.Printf("%-20s  %-30s  %-5d  %-13s\n", trunc(c.Name, 20), trunc(imgName, 30), c.Version, mod)
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

func eventSourcePut(args docopt.Opts, svc v1.MaelstromService) {
	var es v1.EventSource
	readJSON(args, &es)

	// check for existing component and set previousVersion
	getInput := v1.GetEventSourceInput{Name: es.Name}
	getOut, err := svc.GetEventSource(getInput)
	if err == nil {
		fmt.Printf("Updating event source: %s with current version: %d\n", es.Name, getOut.EventSource.Version)
		es.Version = getOut.EventSource.Version
	} else {
		rpcErr, ok := err.(*barrister.JsonRpcError)
		if ok && rpcErr.Code == 1003 {
			// ok - new event source
			fmt.Printf("Registering new event source: %s\n", es.Name)
		} else {
			checkErr(err, "GetEventSource failed for: "+es.Name)
		}
	}

	// store new event source data
	out, err := svc.PutEventSource(v1.PutEventSourceInput{EventSource: es})
	checkErr(err, "PutEventSource failed")
	fmt.Printf("Success event source: %s updated to version: %d\n", out.Name, out.Version)
}

func eventSourceLs(args docopt.Opts, svc v1.MaelstromService) {
	input := v1.ListEventSourcesInput{
		NamePrefix:    strings.TrimSpace(argStr(args, "--prefix")),
		ComponentName: strings.TrimSpace(argStr(args, "--component")),
	}

	eventSourceType := argStr(args, "--type")
	if eventSourceType != "" {
		input.EventSourceType = v1.EventSourceType(eventSourceType)
	}

	first := true
	var nextToken string
	for {
		input.NextToken = nextToken
		output, err := svc.ListEventSources(input)
		checkErr(err, "ListEventSources failed")
		for _, es := range output.EventSources {
			if first {
				first = false
				fmt.Printf("%-20s  %-20s  %-30s  %-5s  %-10s\n",
					"Event Source", "Component", "HTTP", "Ver", "Last Modified")
				fmt.Printf("------------------------------------------------------------------------------------------------\n")
			}
			mod := humanize.Time(time.Unix(0, es.ModifiedAt*1e6))
			httpInfo := ""
			if es.Http != nil {
				parts := make([]string, 0)
				if es.Http.Hostname != "" {
					parts = append(parts, es.Http.Hostname)
				}
				if es.Http.PathPrefix != "" {
					parts = append(parts, es.Http.PathPrefix)
				}
				httpInfo = strings.Join(parts, "/")
			}
			fmt.Printf("%-20s  %-20s  %-30s  %-5d  %-13s\n", trunc(es.Name, 20),
				trunc(es.ComponentName, 20), trunc(httpInfo, 30), es.Version, mod)
		}
		nextToken = output.NextToken
		if output.NextToken == "" {
			break
		}
	}
	if first {
		fmt.Println("No event sources found")
	}
}

func eventSourceRm(args docopt.Opts, svc v1.MaelstromService) {
	out, err := svc.RemoveEventSource(v1.RemoveEventSourceInput{Name: argStr(args, "<name>")})
	checkErr(err, "RemoveEventSource failed")
	if out.Found {
		fmt.Printf("Event source removed: %s\n", out.Name)
	} else {
		fmt.Printf("Event source not found: %s\n", out.Name)
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
  maelctl comp put [--file=<file>] [--json=<json>]
  maelctl comp ls [--prefix=<prefix>]
  maelctl comp rm <name>
  maelctl es put [--file=<file>] [--json=<json>]
  maelctl es ls [--prefix=<prefix>] [--component=<component>] [--type=<type>]
  maelctl es rm <name>
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
	} else if argBool(args, "es") && argBool(args, "put") {
		eventSourcePut(args, svc)
	} else if argBool(args, "es") && argBool(args, "ls") {
		eventSourceLs(args, svc)
	} else if argBool(args, "es") && argBool(args, "rm") {
		eventSourceRm(args, svc)
	} else {
		fmt.Printf("ERROR: unsupported command. args=%v\n", args)
		os.Exit(2)
	}
}
