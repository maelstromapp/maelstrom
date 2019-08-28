package gateway

import (
	"testing"
)

func TestHandlerStartsContainerOnFirstRequest(t *testing.T) {
	wrapTest(t, func() {
		GivenNoMaelstromContainers(t).
			WhenHTTPRequestReceived().
			ThenContainerIsStarted()
	})
}

func TestHandlerUsesExistingContainerIfAlreadyStarted(t *testing.T) {
	wrapTest(t, func() {
		GivenExistingContainer(t).
			WhenHTTPRequestReceived().
			ThenNoNewContainerStarted()
	})
}

func TestHealthCheckStopsContainerOnFailure(t *testing.T) {
	wrapTest(t, func() {
		GivenExistingContainerWithBadHealthCheckPath(t).
			WhenHealthCheckTimeoutElapses().
			ThenContainerIsStopped()
	})
}

func TestHealthCheckKeepsContainerOnSuccess(t *testing.T) {
	wrapTest(t, func() {
		GivenExistingContainer(t).
			WhenHTTPRequestReceived().
			WhenHealthCheckTimeoutElapses().
			ThenContainerIsStarted()
	})
}

func TestStopDrainsRequestsBeforeStoppingContainers(t *testing.T) {
	wrapTest(t, func() {
		GivenExistingContainer(t).
			WhenContainerIsHealthy().
			WhenNLongRunningRequestsMade(5).
			WhenStopRequestReceived().
			ThenContainerIsStopped().
			ThenSuccessfulRequestCountEquals(5)
	})
}

func TestRestartsContainerIfRequestArrivesAfterStopping(t *testing.T) {
	wrapTest(t, func() {
		GivenExistingContainer(t).
			WhenStopRequestReceived().
			ThenContainerIsStopped().
			WhenHTTPRequestReceived().
			ThenContainerIsStarted()
	})
}

func TestStopsContainerIfIdle(t *testing.T) {
	wrapTest(t, func() {
		GivenExistingContainerWithIdleTimeout(t, 1).
			WhenIdleTimeoutElapses().
			WhenAutoscaleRuns().
			ThenContainerIsStopped()
	})
}

func TestRestartsContainerWhenComponentUpdated(t *testing.T) {
	wrapTest(t, func() {
		GivenExistingContainer(t).
			WhenComponentIsUpdated().
			WhenHTTPRequestReceived().
			ThenContainerIsStartedWithNewVersion()
	})
}
