package v1

func HealthCheckSeconds(d *DockerComponent) int64 {
	seconds := d.HttpStartHealthCheckSeconds
	if seconds <= 0 {
		seconds = 60
	}
	return seconds
}
