
# Architecture

## The Basics

* A maelstrom **node** is a machine running the `maelstromd` process
* A maelstrom **cluster** is a collection of nodes that share a common database.
    * Nodes in a cluster should be able to connect to each other on their private port (8374 by default)
* A maelstrom **component** is a name mapped to configuration that specifies how to start a docker container that 
  runs a HTTP server. Requests for the component will be reverse proxied to that container on demand.
* Nodes periodically write their current state to the database (default: once per minute)
* All nodes are peers and should be identically configured.
* Nodes acquire locks from the database before performing certain tasks. This ensures that only
  one node is performing critical operations such as autoscaling or cron triggering.
* `maelctl` makes JSON-RPC requests to a maelstrom node.
    * The `MAELSTROM_PRIVATE_URL` env var tells `maelctl` where to connect. (default=`http://127.0.0.1:8374`)
* New nodes that join the cluster will be automatically assigned components to run within 90 seconds of joining.

## Routing

* All requests and responses in maelstrom are HTTP
* If multiple instances of a component are running, requests are distributed across using round-robin scheduling.
* Each node maintains a routing table and can route requests to its peers if the component is not running locally.

```
                                  +-----------------+
                                  |                 |
               +------------------+  Load Balancer  +-------------------+
               |                  |    (optional)   |                   |
               |                  +--------+--------+                   |
               |                           |                            |
               |                           |                            |
               | 80/443                    | 80/443                     | 80/443
      +--------v--------+         +--------v--------+         +---------v-------+
      |node-a           |         |node-b           |         |node-c           |
      |                 +---------+                 +---------+                 |
      |10.0.0.2         |  8374   |10.0.0.3         |  8374   |10.0.0.4         |
      +--------+--------+         +--------+--------+         +---------+-------+
               |                           |                            |
               |                           |                            |
               |                           |                            |
               |                  +--------v--------+                   |
               |                  |                 |                   |
               +----------------->+      MySQL      +<------------------+
                                  |   or Postgres   |
                                  +-----------------+
```