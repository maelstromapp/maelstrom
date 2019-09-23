
# Project YAML Reference

* All YAML field names should be lowercase (See: https://github.com/go-yaml/yaml/issues/156)
* Default file name is `maelstrom.yml`
* YAML file will be bound to the [yamlProject struct](https://github.com/coopernurse/maelstrom/blob/master/pkg/maelstrom/project.go#L218) here (use this for canonical reference)

## Basic Structure

```yaml

---
# Project name
name: myproject
# If set, name=value environment variables will be loaded from this file
envfile: /path/to/file
# Environment variables that will be set in all components
environment:
  - ENV_VAR1=value1
  - ENV_VAR2=value2
components:
  # component name:
  component_name_1:
    # docker image to use
    image: foo/bar:mytag
    # command to run
    command: ["python", "-u", "/app/detector.py"]
    # env vars for this component. if any overlap with project, component values win
    environment:
      - MORE_ENV=here
      - ENV_VAR2=this_overrides_project_setting
    # event sources for this component
    eventsources:
      component1_http:
        # only specify one block (http, cron, or sqs)
        http:
          hostname: component1.example.com
```

## Component

### Scaling and resource options

```yaml

---
name: myproject
components:
  component_name_1:
    # Soft limit - MiB of RAM to reserve per container (docker run: --memory-reservation)
    # Optional. Default = 128 
    reservememory: 2048
    # Hard limit - MiB of RAM. (docker run: --memory)
    # Optional. If not set, no memory limit is set on the container.
    limitmemory: 4096
    # CPU share to give each container (docker run: --cpu-shares)
    # Optional. If not set, then this flag is not specified on the container.
    # Docker default is 1024, so a value of 2048 would be 2x weight
    cpushares: 512
    # Maximum concurrent requests to send to a single container
    # Optional. Default = 1
    maxconcurrency: 5
    # Minimum instances of this component to run at any time
    # Optional. Default = 0
    mininstances: 1
    # Maximum instances of this component to run at any time
    # Optional. Default is unlimited.
    maxinstances: 30
    # Maximum seconds a request may take to complete, including wait time if 
    # max concurrency is reached. Optional. Default = 300
    maxdurationseconds: 60
    # Seconds a container must be idle before it is eligible to scale to 
    # zero running instances. Optional. Default = 300
    idletimeoutseconds: 120
    # If the % of maxConcurrency of all instances of this component is lower than this
    # value, instances of the component will be stopped (respecting mininstances if > 0).
    # Value should be 0..1 (e.g. .5 = 50%). Default = 0.25
	scaledownconcurrencypct: 0.25
    # If the % of maxConcurrency of all instances of this component exceeds this value,
    # more instances of the component will be started (respecting maxinstances if > 0).
    # Value should be 0..1 (e.g. .5 = 50%). Default = 0.75
	scaleupconcurrencypct: 0.75
```

### Logging options

By default logs go to the default Docker `json` logger and can be viewed via `docker logs` on the host running the container.
In most cases you'll want to specify a different `logdriver` so that logs are aggregated somewhere.

```yaml

---
name: myproject
components:
  component_name_1:
    # Docker log driver to use. Optional.
    # For all options, see: 
    # https://docs.docker.com/config/containers/logging/configure/
    logdriver: syslog
    # options are specific to the log driver specified above
    # See docker documentation for details
    logdriveroptions:
      syslog-address: udp://192.168.1.30:514
      syslog-facility: local4
```

### Docker pull options

By default docker images are pulled without authentication. Each component may optionally specify registry auth
credentials or provide a completely custom command to run on the host to pull the image.

#### Registry credentials

```yaml

---
name: myproject
components:
  component_name_1:
    pullusername: scott
    pullpassword: tiger
```

#### Custom command

The `<image>` string will be replaced with the docker image configured on the component.

```yaml

---
name: myproject
components:
  component_name_1:
    pullcommand: ["/usr/local/bin/mypull", "<image>"]
```

### HTTP options

```yaml

---
name: myproject
components:
  component_name_1:
    # Port that the component's web server binds to. Default = 8080
    httpport: 8080
    # Path to make HTTP GET to for health checks. Default = /
    httphealthcheckpath: /health_check
    # Max seconds before health check must pass during container startup. 
    # If this deadline is reached before the health check passes the container is stopped.
    # Default = 60
    httpstarthealthcheckseconds: 90
    # Interval in seconds to perform health check. Default = 10
    httphealthcheckseconds: 15
```

### Other options

```yaml

---
name: myproject
components:
  component_name_1:
    # Volumes to bind into the container
    # If this is used in production make sure you have some mechanism
    # to provision the source path on each maelstrom node before starting maelstromd
    volumes:
      - # Source path on the host. Required.
        source: ${HOME}/foo
        # Target path of the volume in the container. Required.
        target: /opt/foo
        # Type of mount (bind, volume, or tmpfs). Optional. Default = bind
        type: bind
        # Whether volume is read only. Default = false
        readonly: false
    # Expose ports from container to host
    # Probably only useful during local development (e.g. for debugger ports)
    # Note that if you statically bind a port here and maxinstances > 1
    # You could run into port conflicts if maelstrom tries to deploy the same
    # component twice on the same host. Use with care.
    ports:
      - 6001:6001
    # Network name to attach container to. Optional.
    # This is sometimes useful when developing locally and you wish to access 
    # resources from a docker-compose stack that are placed on a non-default network.
    networkname: mynet
```