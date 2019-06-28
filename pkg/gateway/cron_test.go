package gateway

import (
	docker "github.com/docker/docker/client"
	. "github.com/franela/goblin"
	"testing"
	"time"
)

func TestCron(t *testing.T) {
	g := Goblin(t)

	dockerClient, err := docker.NewEnvClient()
	if err != nil {
		panic(err)
	}

	g.Describe("Cron Event Source", func() {

		g.BeforeEach(func() {
			resetDefaults()
		})

		g.AfterEach(func() {
			stopCronService()
			stopMaelstromContainers(g, dockerClient)
		})

		g.It("Starts container when cron fires", func() {
			g.Timeout(20 * time.Second)
			GivenNoMaelstromContainers(g, dockerClient).
				WhenCronEventSourceRegistered("* * * * * *").
				WhenCronServiceStarted().
				ThenContainerStartsWithin(15 * time.Second)
		})
	})
}
