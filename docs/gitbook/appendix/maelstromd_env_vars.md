
# maelstromd Environment Variables

* `maelstromd` configuration is done via environment variables.
* All variables are prefixed with `MAEL_`.
* All variables are upper case
* Variables are bound to the `Config` struct in [config.go](https://github.com/coopernurse/maelstrom/blob/master/pkg/config/config.go#L59) using [envconfig](https://github.com/kelseyhightower/envconfig)

## Ports

| Variable                    | Description                                                               | Required? | Default |
|-----------------------------|---------------------------------------------------------------------------|-----------|---------|
| MAEL_PUBLICPORT             | HTTP port to bind to for external HTTP reqs                               | No        | 80      |
| MAEL_PUBLICHTTPSPORT        | HTTP port to bind to for external HTTPS reqs                              | No        | 443     |
| MAEL_PRIVATEPORT            | HTTP port to bind to for internal HTTP reqs (node to node and RPC calls)  | No        | 8374    |

## Database

| Variable                        | Description                                  | Required? | Default |
|---------------------------------|----------------------------------------------|-----------|---------|
| MAEL_SQLDRIVER                  | sql db driver to use (sqlite3, mysql)        | Yes       | None    |
| MAEL_SQLDSN                     | DSN for maelstrom sql db                     | Yes       | None    |

## Refresh Intervals

| Variable                        | Description                                  | Required? | Default |
|---------------------------------|----------------------------------------------|-----------|---------|
| MAEL_CRONREFRESHSECONDS         | Interval to reload cron rules from db        | No        | 60      |

## System Resources

| Variable                  | Description                                  | Required? | Default                 |
|---------------------------|----------------------------------------------|-----------|-------------------------|
| MAEL_TOTALMEMORY          | Memory (MiB) to make available to containers | No        | System total memory     |

## Debugging

| Variable                        | Description                                  | Required? | Default |
|---------------------------------|----------------------------------------------|-----------|---------|
| MAEL_LOGGCSECONDS               | If set, print GC stats every x seconds       | No        | None    |
| MAEL_CPUPROFILEFILENAME         | If set, write Go profiling info to this file | No        | None    |
