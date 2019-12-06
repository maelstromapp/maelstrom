package config

import (
	"bytes"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestConfigFromEnv(t *testing.T) {
	env := `
# comment here
MAEL_PUBLIC_PORT=1
MAEL_PUBLIC_HTTPS_PORT=2
MAEL_PRIVATE_PORT=3

# ignore blank lines
MAEL_SQL_DRIVER=driver
MAEL_SQL_DSN=dsn Here

MAEL_CRON_REFRESH_SECONDS=50
MAEL_TOTAL_MEMORY=-200
MAEL_INSTANCE_ID=instid
MAEL_SHUTDOWN_PAUSE_SECONDS=3
MAEL_TERMINATE_COMMAND=term cmd
MAEL_AWS_TERMINATE_QUEUE_URL=q1
MAEL_AWS_TERMINATE_MAX_AGE_SECONDS=44
MAEL_AWS_SPOT_TERMINATE_POLL_SECONDS=55

MAEL_LOG_GC_SECONDS=66
MAEL_CPU_PROFILE_FILENAME=somefile
MAEL_PPROF=true

MAEL_DOCKER_PRUNE_MINUTES=123
MAEL_DOCKER_PRUNE_UNREG_IMAGES=true
MAEL_DOCKER_PRUNE_UNREG_KEEP=acme.org/*,hello-world

MAEL_NODE_LIVENESS_SECONDS=200
`

	expected := Config{
		InstanceId:                  "instid",
		PublicPort:                  1,
		PublicHTTPSPort:             2,
		PrivatePort:                 3,
		SqlDriver:                   "driver",
		SqlDsn:                      "dsn Here",
		CronRefreshSeconds:          50,
		LogGcSeconds:                66,
		CpuProfileFilename:          "somefile",
		Pprof:                       true,
		TotalMemory:                 -200,
		TerminateCommand:            "term cmd",
		ShutdownPauseSeconds:        3,
		AwsTerminateQueueUrl:        "q1",
		AwsTerminateMaxAgeSeconds:   44,
		AwsSpotTerminatePollSeconds: 55,
		NodeLivenessSeconds:         200,
		DockerPruneUnregImages:      true,
		DockerPruneMinutes:          123,
		DockerPruneUnregKeep:        "acme.org/*,hello-world",
		Cluster: ClusterOptions{
			Name:    "",
			MinSize: 1,
			MaxSize: 20,
		},
		DigitalOcean: &DigitalOceanOptions{
			AccessToken:    "",
			Region:         "nyc3",
			SSHFingerprint: "",
			DropletSize:    "s-1vcpu-1gb",
			ImageSlug:      "debian-9-x64",
			Backups:        true,
			IPV6:           false,
		},
	}

	assert.Nil(t, ReaderToEnv(bytes.NewBufferString(env)))
	conf, err := FromEnv()
	assert.Nil(t, err)
	assert.Equal(t, expected, conf)
}
