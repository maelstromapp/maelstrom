
# Project YAML Reference

* All YAML field names should be lowercase (See: https://github.com/go-yaml/yaml/issues/156)
* Default file name is `maelstrom.yml`
* YAML file will be bound to the [yamlProject struct](https://github.com/coopernurse/maelstrom/blob/master/pkg/maelstrom/project.go#L218) here (use this for canonical reference)

## Using maelctl

Use `maelctl project put` to apply changes in `maelstrom.yml` to the target `maelstromd` process

```
# running maelctl with no args outputs help
$ maelctl

# basic usage - assumes file is maelstrom.yml
$ maelctl project put

# use --file to specify a different yml file
$ maelctl project put --file=my-project.yml

# dry run mode - outputs diff of changes but doesn't make any changes
$ maelctl project put --diffonly
```

## Basic Structure

```yaml

---
# Project name
name: myproject
# If set, name=value environment variables will be loaded from this file and set in the 
# environment of runnning containers
# NOTE: this does NOT load variables for purposes of ${FOO} variable interpolation
#       in this YAML file. Interpolation is done based on env vars in scope in the shell
#       that invokes "maelctl project put".  If you wish to scope in env vars from a file
#       for purposes of interpolation, use the "--env=filename" switch. For example:
#       "maelctl project put --env=interpolate-vars.env" 
envfile: /path/to/file
# Environment variables that will be set in all component containers
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
    # If true, maxconcurrency will be used influence autoscaling, but will not
    # be used to queue requests over the limit. Requests to this component will
    # be load balanced, but will passed through with no limits.
    # Optional. Default = false
    softconcurrencylimit: true
    # Minimum instances of this component to run at any time
    # Optional. Default = 0
    mininstances: 1
    # Maximum instances of this component to run at any time across all nodes
    # Optional. Default is unlimited.
    maxinstances: 30
    # Maximum instances of this component to run on a single node
    # Optional. Default is unlimited.
    maxinstancespernode: 3
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

### Restart options

Use the `restartorder` and `startparallelism` features to control how rolling deploys
are executed.

```yaml

---
name: myproject
components:
  component_name_1:
    # restartorder
    #   Informs how updates to a new version should be performed.
    #   Valid values: startstop, stopstart
    #
    # If "startstop" a new container is started and health checked, then the old container 
    # is stopped.
    #
    # If "stopstart" the old container is stopped, then the new container is started.
    #
    # "startstop" will result in faster upgrades to new versions and in single instance 
    # cases will avoid request pauses during restarts, but it will lead to temporary
    # increased memory usage since the two containers (old and new) will briefly run
    # simultaneously.
    #
    # Default = stopstart
    restartorder: startstop
    #
    # startparallelism
    #   Valid values: parallel, series, seriesfirst   (Default = parallel)
    #
    #   parallel:
    #   Start (or restart) components fully parallel (no coordination) parallel
    #    
    #   series:
    #   Start component containers one at a time
    #
    #   seriesfirst:
    #   The first container to update to a new version must
    #   acquire a lock, but after the new version has been deployed
    #   once, all other instances may update in parallel.
    #
    #   This is useful for cases where the component performs some
    #   provisioning step that may not tolerate concurrent execution
    #   (e.g. a db schema migration, or creation of a queue).
    #   If seriesfirst is used, the first instance of a new version will
    #   run in isolation (creating the relevant resources), and then all
    #   other containers can start (which will no-op on the resource creation
    #   or schema migration)
    startparallelism: series
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

#### Floating tags

If you use a floating tag (e.g. "latest") you should set `pullimageonstart: true` or `pullimageonput: true` to
ensure that changes to the image are pulled. Otherwise maelstrom may continue to start containers with
a stale version of the image.

#### Pull before each start

Images are always pulled if they do not exist locally, but are not pulled if the image already exists.
If you want maelstrom to pull the image before starting any container for the component,
set `pullimageonstart: true`.

```yaml

---
name: myproject
components:
  component_name_1:
    pullimageonstart: true
```

#### Pull after component put

To pull an image immediately after a component put operation,
set `pullimageonput: true`.  This will pull the image immediately any time the component is modified, including
during a `project put` call that modifies the component.

```yaml

---
name: myproject
components:
  component_name_1:
    pullimageonput: true
```

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
    # If this number of failures is reached the container will be considered
    # non-responsive and will be restarted. Default = 1
    httphealthcheckmaxfailures: 2
```

### Other options

These are all optional properties.

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
      # add more volumes using a list
      - source: /some/other/path
        target: /otherpath
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
    #
    # DNS options - this allows you to override the DNS server to use in the container
    # See docs: https://docs.docker.com/v17.09/engine/userguide/networking/default_network/configure-dns/
    #
    # equivalent to docker run --dns
    dns:
      - 8.8.8.8
    # equivalent to docker run --dns-opt 
    dnsoptions:
      - "option 1"
      - "option 2"
    # equivalent to docker run --dns-search
    dnssearch:
      - example.com
      - example.org
    # equivalent to docker run --ulimit <name>:<soft>:<hard>
    ulimits:
      - nofile=20000:30000
      - nproc=3
    # runs container with init PID 1
    # equivalent to docker run --init (default is false)
    containerinit: true
```
