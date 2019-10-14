
# maelstromd Environment Variables

* `maelstromd` configuration is done via environment variables.
* All variables (except LOGXI vars) are prefixed with `MAEL_`.
* All variables are upper case
* Variables are bound to the `Config` struct in [config.go](https://github.com/coopernurse/maelstrom/blob/master/pkg/config/config.go#L59) using [envconfig](https://github.com/kelseyhightower/envconfig)

## Logging

`maelstromd` uses [mgutz/logxi](https://github.com/mgutz/logxi) for logging, which has a set of environment variables
that control the logging format. Please read the logxi docs for more details.

| Variable         | Description                                  | Example
|------------------|----------------------------------------------|-----------------------------------|
| LOGXI            | Sets log levels                              | `LOGXI=*=DBG`
| LOGXI_FORMAT     | Sets format for logger                       | `LOGXI_FORMAT=text`
| LOGXI_COLORS     | Color schema for log levels                  | `LOGXI_COLORS=TRC,DBG,WRN=yellow,INF=green,ERR=red`

## Ports

| Variable                    | Description                                                               | Required? | Default |
|-----------------------------|---------------------------------------------------------------------------|-----------|---------|
| MAEL_PUBLIC_PORT            | HTTP port to bind to for external HTTP reqs                               | No        | 80      |
| MAEL_PUBLIC_HTTPS_PORT      | HTTP port to bind to for external HTTPS reqs                              | No        | 443     |
| MAEL_PRIVATE_PORT           | HTTP port to bind to for internal HTTP reqs (node to node and RPC calls)  | No        | 8374    |

## Database

| Variable                        | Description                                  | Required? | Default |
|---------------------------------|----------------------------------------------|-----------|---------|
| MAEL_SQL_DRIVER                 | sql db driver to use (sqlite3, mysql)        | Yes       | None    |
| MAEL_SQL_DSN                    | DSN for maelstrom sql db                     | Yes       | None    |

#### Example DSNs:

| Driver   | Project                                                       | Example DSN 
|----------|---------------------------------------------------------------|----------------------
| sqlite3  | [go-sqlite3](https://github.com/mattn/go-sqlite3)             | `file:test.db?cache=shared&mode=memory`
| mysql    | [go-sql-driver/mysql](https://github.com/go-sql-driver/mysql) | `user:passwd@(hostname:3306)/mael`
| postgres | [lib/pq](https://godoc.org/github.com/lib/pq)                 | `postgres://user:passwd@host:port/mael`

## Refresh Intervals

| Variable                        | Description                                  | Required? | Default |
|---------------------------------|----------------------------------------------|-----------|---------|
| MAEL_CRON_REFRESH_SECONDS       | Interval to reload cron rules from db        | No        | 60      |

## System Resources

| Variable                  | Description                                  | Required? | Default                 |
|---------------------------|----------------------------------------------|-----------|-------------------------|
| MAEL_TOTAL_MEMORY         | Memory (MiB) to make available to containers | No        | System total memory     |

## System Management

| Variable                             | Description                                             | Required? | Default                 
|--------------------------------------|---------------------------------------------------------|-----------|-----------
| MAEL_INSTANCE_ID                     | ID of instance with VM provider (e.g. EC2 instance id)  | No <sup>[1](#awslifecycle)</sup> | None
| MAEL_SHUTDOWN_PAUSE_SECONDS          | Seconds to pause before stopping containers at shutdown | No        | 0    
| MAEL_TERMINATE_COMMAND               | Command to run if instance terminated. Only invoked if AWS lifecycle termination runs, not if SIGTERM/SIGINT received.      | No        | `systemctl disable maelstromd`  
| MAEL_AWS_TERMINATE_QUEUE_URL         | SQS queue URL for lifecycle hook termination queue      | No <sup>[1](#awslifecycle)</sup> | None  
| MAEL_AWS_TERMINATE_MAX_AGE_SECONDS   | SQS messages older than this many seconds will be automatically deleted. This prevents stale messages from getting stuck in the queue. | No        | 600  
| MAEL_AWS_SPOT_TERMINATE_POLL_SECONDS | If > 0, maelstromd will poll EC2 metadata endpoint checking for spot termination requests. If action=stop or terminate, maelstromd will shutdown gracefully. Value of setting sets the polling interval in seconds. | No        | 0  

<a name="awslifecycle">1</a>: Required for AWS Auto Scale Lifecycle Hook support

## Debugging

| Variable                        | Description                                  | Required? | Default |
|---------------------------------|----------------------------------------------|-----------|---------|
| MAEL_LOG_GC_SECONDS             | If set, print GC stats every x seconds       | No        | None    |
| MAEL_CPU_PROFILE_FILENAME       | If set, write Go profiling info to this file | No        | None    |
