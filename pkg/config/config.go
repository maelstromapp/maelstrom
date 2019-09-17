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
