package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/coopernurse/barrister-go"
	"github.com/coopernurse/maelstrom/pkg/common"
	"github.com/coopernurse/maelstrom/pkg/config"
	"github.com/coopernurse/maelstrom/pkg/maelstrom"
	"github.com/coopernurse/maelstrom/pkg/v1"
	"github.com/coopernurse/maelstrom/pkg/vm"
	"github.com/docopt/docopt-go"
	"github.com/dustin/go-humanize"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

func loadConfig(args docopt.Opts) config.Config {
	envFile := argStr(args, "--env-file")
	if envFile != "" {
		err := config.FileToEnv(envFile)
		checkErr(err, "Failed to parse environment config from file: "+envFile)
	}
	cfg, err := config.FromEnv()
	checkErr(err, "Failed to initialize config")
	return cfg
}

func createClusterAdapter(cfg config.Config, ctx context.Context) vm.Adapter {
	if cfg.DigitalOcean != nil && cfg.DigitalOcean.AccessToken != "" {
		return vm.NewDOAdapter(cfg.DigitalOcean.AccessToken, &ctx)
	}
	fmt.Printf("ERROR: no cluster adapter configured\n")
	os.Exit(1)
	return nil
}

func clusterCreate(args docopt.Opts, ctx context.Context) {
	outDir := argStr(args, "<outdir>")
	cfg := loadConfig(args)
	adapter := createClusterAdapter(cfg, ctx)
	options := vm.CreateClusterOptions{
		SSHPublicKeyFile:  filepath.Join(outDir, fmt.Sprintf("%s_ssh_public", cfg.Cluster.Name)),
		SSHPrivateKeyFile: filepath.Join(outDir, fmt.Sprintf("%s_ssh_private.pem", cfg.Cluster.Name)),
	}
	fmt.Printf("Creating cluster. adapter=%s name=%s\n", adapter.Name(), cfg.Cluster.Name)
	out, err := vm.CreateCluster(cfg, options, adapter)
	checkErr(err, "CreateCluster failed for adapter: "+adapter.Name())
	fmt.Printf("Created cluster. Root VM id=%s publicIp=%s privateIp=%s\n", out.RootVM.Id,
		out.RootVM.PublicIpAddr, out.RootVM.PrivateIpAddr)
}

func clusterDestroy(args docopt.Opts, ctx context.Context) {

}

func clusterInfo(args docopt.Opts, ctx context.Context) {
	cfg := loadConfig(args)
	adapter := createClusterAdapter(cfg, ctx)
	info, err := adapter.GetClusterInfo(cfg)
	checkErr(err, "GetClusterInfo failed for adapter: "+adapter.Name())
	fmt.Printf("Cluster name: %s\n", info.ClusterName)
	if info.LoadBalancer == nil {
		fmt.Println("No load balancer")
	} else {
		fmt.Println("Load balancer:")
		if info.LoadBalancer.PublicIpAddr != "" {
			fmt.Printf("  Public IP address: %s\n", info.LoadBalancer.PublicIpAddr)
		}
		if info.LoadBalancer.Hostname != "" {
			fmt.Printf("  Hostname: %s\n", info.LoadBalancer.Hostname)
		}
	}
	if len(info.VMs) == 0 {
		fmt.Println("No Virtual machines")
	} else {
		fmt.Println("Virtual machines:")
		for _, v := range info.VMs {
			created := v.CreatedAt.Format("2006-01-02 15:04")
			fmt.Printf("  publicIp=%s createdAt=%s id=%s\n", v.PublicIpAddr, created, v.Id)
		}
	}
	if len(info.Meta) > 0 {
		fmt.Println("Meta:")
		keys := common.SortedMapKeys(info.Meta)
		for _, k := range keys {
			fmt.Printf("  %s=%s\n", k, info.Meta[k])
		}
	}
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

func projectPut(args docopt.Opts, svc v1.MaelstromService) {
	fname := argStr(args, "--file")
	if fname == "" {
		fname = "maelstrom.yml"
	}
	proj, err := maelstrom.ParseYamlFileAndInterpolateEnv(fname)
	checkErr(err, "Unable to load project YAML file")
	out, err := svc.PutProject(v1.PutProjectInput{Project: proj})
	checkErr(err, "PutProject failed")
	if maelstrom.PutProjectOutputEmpty(out) {
		fmt.Printf("PutProject: No project changes detected for project: %s using file: %s", out.Name, fname)
	} else {
		fmt.Printf("Project saved: %s from file: %s\n", out.Name, fname)
		fmt.Printf("Type         Name                               Action\n")
		for _, c := range out.ComponentsAdded {
			fmt.Printf("%-11s  %-33s  %s\n", "Component", c.Name, "Added")
		}
		for _, c := range out.ComponentsUpdated {
			fmt.Printf("%-11s  %-33s  %s\n", "Component", c.Name, "Updated")
		}
		for _, c := range out.ComponentsRemoved {
			fmt.Printf("%-11s  %-33s  %s\n", "Component", c, "Removed")
		}
		for _, es := range out.EventSourcesAdded {
			fmt.Printf("%-11s  %-33s  %s\n", "EventSource", es.Name, "Added")
		}
		for _, es := range out.EventSourcesUpdated {
			fmt.Printf("%-11s  %-33s  %s\n", "EventSource", es.Name, "Updated")
		}
		for _, es := range out.EventSourcesRemoved {
			fmt.Printf("%-11s  %-33s  %s\n", "EventSource", es, "Removed")
		}
	}
}

func projectRm(args docopt.Opts, svc v1.MaelstromService) {
	out, err := svc.RemoveProject(v1.RemoveProjectInput{Name: argStr(args, "<name>")})
	checkErr(err, "RemoveProject failed")
	if out.Found {
		fmt.Printf("Project removed: %s\n", out.Name)
	} else {
		fmt.Printf("Project not found: %s\n", out.Name)
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
				fmt.Printf("%-20s  %-20s  %-5s  %-30s  %-5s  %-10s\n",
					"Event Source", "Component", "Type", "Description", "Ver", "Last Modified")
				fmt.Printf("--------------------------------------------------------------------------------------------------------\n")
			}
			mod := humanize.Time(time.Unix(0, es.ModifiedAt*1e6))
			esType := "??"
			description := "??"
			if es.Http != nil {
				esType = "http"
				parts := make([]string, 0)
				if es.Http.Hostname != "" {
					parts = append(parts, es.Http.Hostname)
				}
				if es.Http.PathPrefix != "" {
					parts = append(parts, es.Http.PathPrefix)
				}
				description = strings.Join(parts, "/")
			} else if es.Cron != nil {
				esType = "cron"
				description = fmt.Sprintf("sched='%s' path=%s", es.Cron.Schedule, es.Cron.Http.Path)
			} else if es.Sqs != nil {
				esType = "sqs"
				description = es.Sqs.QueueName
				if es.Sqs.NameAsPrefix {
					description += "*"
				}
			}
			fmt.Printf("%-20s  %-20s  %-5s  %-30s  %-5d  %-13s\n", trunc(es.Name, 20),
				trunc(es.ComponentName, 20), esType, trunc(description, 30), es.Version, mod)
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

func logsGet(args docopt.Opts, baseUrl string) {
	qs := url.Values{}
	components := argStr(args, "--components")
	if components != "" {
		qs.Add("components", components)
	}
	since := argStr(args, "--since")
	if since != "" {
		qs.Add("since", since)
	}
	logUrl := fmt.Sprintf("%s/_mael/logs?%s", baseUrl, qs.Encode())

	client := &http.Client{
		Timeout: 0,
	}
	resp, err := client.Get(logUrl)
	checkErr(err, fmt.Sprintf("Request to %s failed", logUrl))
	defer common.CheckClose(resp.Body, &err)

	// print response line by line
	scanner := bufio.NewScanner(resp.Body)
	scanner.Split(common.ScanCRLF)
	var logmsg common.LogMsg
	for scanner.Scan() {
		msg := scanner.Text()
		if msg != common.PingLogMsg {
			err := json.Unmarshal([]byte(scanner.Text()), &logmsg)
			if err == nil {
				out := os.Stdout
				if logmsg.Stream == "stderr" {
					out = os.Stderr
				}
				_, _ = fmt.Fprintf(out, "%s\n", logmsg.Format())
			} else {
				fmt.Printf("maelctl ERROR: unable to unmarshal msg: %v\n", err)
			}
		}
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
  maelctl cluster create [--out=<outdir>] [--env-file=<envfile>]
  maelctl cluster destroy [--env-file=<envfile>]
  maelctl cluster info [--env-file=<envfile>]
  maelctl comp put [--file=<file>] [--json=<json>]
  maelctl comp ls [--prefix=<prefix>]
  maelctl comp rm <name>
  maelctl es put [--file=<file>] [--json=<json>]
  maelctl es ls [--prefix=<prefix>] [--component=<component>] [--type=<type>]
  maelctl es rm <name>
  maelctl logs [--components=<components>] [--since=<since>]
  maelctl project put [--file=<file>]
  maelctl project rm <name>
`
	args, err := docopt.ParseDoc(usage)
	if err != nil {
		fmt.Printf("ERROR parsing arguments: %v\n", err)
		os.Exit(1)
	}

	baseUrl := os.Getenv("MAELSTROM_PRIVATE_URL")
	if baseUrl == "" {
		baseUrl = "http://127.0.0.1:8374"
	}
	apiUrl := fmt.Sprintf("%s/_mael/v1", baseUrl)
	svc := newMaelstromServiceClient(apiUrl)

	if argBool(args, "cluster") && argBool(args, "create") {
		clusterCreate(args, context.Background())
	} else if argBool(args, "cluster") && argBool(args, "destroy") {
		clusterDestroy(args, context.Background())
	} else if argBool(args, "cluster") && argBool(args, "info") {
		clusterInfo(args, context.Background())
	} else if argBool(args, "comp") && argBool(args, "put") {
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
	} else if argBool(args, "logs") {
		logsGet(args, baseUrl)
	} else if argBool(args, "project") && argBool(args, "put") {
		projectPut(args, svc)
	} else if argBool(args, "project") && argBool(args, "rm") {
		projectRm(args, svc)
	} else {
		fmt.Printf("ERROR: unsupported command. args=%v\n", args)
		os.Exit(2)
	}
}
