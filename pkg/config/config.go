package config

import (
	"bufio"
	"fmt"
	"github.com/coopernurse/maelstrom/pkg/common"
	"github.com/ndrewnee/envconfig"
	"io"
	"os"
	"strings"
)

func FileToEnv(fname string) error {
	file, err := os.Open(fname)
	if err != nil {
		return fmt.Errorf("config: error opening env file: %s - %v", fname, err)
	}
	defer common.CheckClose(file, &err)
	err = ReaderToEnv(file)
	if err != nil {
		return fmt.Errorf("config: error scanning env file: %s - %v", fname, err)
	}
	return nil
}

func ReaderToEnv(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		pos := strings.Index(line, "=")
		if pos > 0 && !strings.HasPrefix(line, "#") {
			key := strings.TrimSpace(line[0:pos])
			val := strings.TrimSpace(line[pos+1:])
			if key != "" {
				err := os.Setenv(key, val)
				if err != nil {
					return fmt.Errorf("config: unable to set env var: %s - %v", key, err)
				}
			}
		}
	}
	return scanner.Err()
}

func FromEnvFile(fname string) (Config, error) {
	err := FileToEnv(fname)
	if err != nil {
		return Config{}, err
	}
	return FromEnv()
}

func FromEnv() (Config, error) {
	var c Config
	err := envconfig.ProcessX(&c, envconfig.Options{Prefix: "mael", SplitWords: true})
	if err != nil {
		return Config{}, err
	}
	return c, nil
}

type Config struct {
	// Server options

	// Identifier for the host machine
	// This should typically be set to the ID issued by the cloud provider creating the machine
	// (e.g. the EC2 instance id if running in AWS)
	InstanceId string

	// Port used for public reverse proxying
	PublicPort int `default:"80"`
	// HTTPS Port used for public reverse proxying
	PublicHTTPSPort int `default:"443" envconfig:"PUBLIC_HTTPS_PORT"`
	// Port used for private routing and management operations
	PrivatePort int `default:"8374"`
	// database/sql driver to use
	SqlDriver string
	// DSN for sql database - format is specific to each particular database driver
	SqlDsn string
	// Interval to refresh cron rules from db
	CronRefreshSeconds int `default:"60"`
	// If > 0, print gc stats every x seconds
	LogGcSeconds int

	// If set, log profile data to this filename
	CpuProfileFilename string
	// If true, bind go pprof endpoints to private gateway /_mael/pprof/
	Pprof bool

	// Memory (MiB) to make available to containers (if set to zero, maelstromd will simply act as a relay)
	TotalMemory int64 `default:"-1"`

	// Terminate command - if instance is told to terminate, run this command
	// Default: "systemctl disable maelstromd"
	TerminateCommand string `default:"systemctl disable maelstromd"`

	// If > 0, will sleep for this number of seconds after stopping background jobs
	// before shutting down HTTP listeners. In clustered environments this can be used
	// to give peers time to remove node from routing table, minimizing the chance of
	// dropping a request during shutdown
	ShutdownPauseSeconds int

	// If > 0, will prune exited containers and untagged images every x minutes
	// Similar to the "docker system prune" command
	DockerPruneMinutes int

	// If DockerPruneMinutes > 0 and this is true, when prune operation runs
	// maelstrom will load the list of components and remove any image that is
	// not registered to a maelstrom component. This is useful if your system
	// uses version tags. Set this option to true to remove old versions of images
	// no longer referenced by a component.
	DockerPruneUnregImages bool

	// Comma separated list of image tags to keep. Only relevant if
	// DockerPruneUnregImages=true and DockerPruneMinutes > 0
	// Supports * globs, so "myorg/*" would match "myorg/image1" and "myorg/image2" but
	// not "otherorg/myorg"
	DockerPruneUnregKeep string

	// AWS Options
	//
	// SQS Queue to poll for EC2 Auto Scaling Lifecycle Hooks
	// See: https://docs.aws.amazon.com/autoscaling/ec2/userguide/lifecycle-hooks.html
	// If this is non-empty, a SQS poller will listen for messages and will notify
	// cluster peers that a node is scheduled for termination
	AwsTerminateQueueUrl string

	// AWS lifecycle hook messages older than this many seconds will be deleted immediately
	// This avoids stuck messages in the queue
	AwsTerminateMaxAgeSeconds int `default:"600"`

	// If > 0, poll the EC2 spot/instance-action metadata endpoint every x seconds looking
	// for a 'stop' or 'terminate' message. If found, initiate a graceful shutdown
	// This setting should be enabled on any server running via a spot instance request,
	// although it's safe to use on any EC2 host
	AwsSpotTerminatePollSeconds int

	// If > 0, node status rows older than this many seconds will be removed from the database,
	// effectively removing the node from the cluster until it reports in again
	NodeLivenessSeconds int `default:"300"`

	// Currently unsupported - will dust these off in the future
	Cluster      ClusterOptions
	DigitalOcean *DigitalOceanOptions `envconfig:"DO"`
}

type ClusterOptions struct {
	Name    string
	MinSize int `default:"1"`
	MaxSize int `default:"20"`
}

type DigitalOceanOptions struct {
	AccessToken    string
	Region         string `default:"nyc3"`
	SSHFingerprint string
	DropletSize    string `default:"s-1vcpu-1gb"`
	ImageSlug      string `default:"debian-9-x64"`
	Backups        bool   `default:"true"`
	IPV6           bool
}
