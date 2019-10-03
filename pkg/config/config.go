package config

import (
	"bufio"
	"fmt"
	"github.com/coopernurse/maelstrom/pkg/common"
	"github.com/kelseyhightower/envconfig"
	"os"
	"strings"
)

func FileToEnv(fname string) error {
	file, err := os.Open(fname)
	if err != nil {
		return fmt.Errorf("config: error opening env file: %s - %v", fname, err)
	}
	defer common.CheckClose(file, &err)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		pos := strings.Index(line, "=")
		if pos > 0 && !strings.HasPrefix(line, "#") {
			key := strings.TrimSpace(line[0:pos])
			val := strings.TrimSpace(line[pos+1:])
			if key != "" {
				err = os.Setenv(key, val)
				if err != nil {
					return fmt.Errorf("config: unable to set env var: %s - %v", key, err)
				}
			}
		}
	}

	err = scanner.Err()
	if err != nil {
		return fmt.Errorf("config: error scanning env file: %s - %v", fname, err)
	}
	return nil
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
	err := envconfig.Process("mael", &c)
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
	PublicHTTPSPort int `default:"443"`
	// Port used for private routing and management operations
	PrivatePort int `default:"8374"`
	// database/sql driver to use
	SqlDriver string
	// DSN for sql database - format is specific to each particular database driver
	SqlDSN string
	// Interval to refresh cron rules from db
	CronRefreshSeconds int `default:"60"`
	// If > 0, print gc stats every x seconds
	LogGCSeconds int
	// If set, log profile data to this filename
	CpuProfileFilename string
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
