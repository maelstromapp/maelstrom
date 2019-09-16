package maelstrom

import (
	"testing"
	"time"
)

func TestCronStartsContainerWhenTriggered(t *testing.T) {
	wrapTest(t, func() {
		GivenNoMaelstromContainers(t).
			WhenCronEventSourceRegistered("* * * * * *").
			WhenCronServiceStarted().
			ThenContainerStartsWithin(15 * time.Second)
	})
}
