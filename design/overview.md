
# Maelstrom Design

## Overview 

```
+----------------------+      +--------------------+      +--------------------------+
|                      |      |                    |      |                          |
|  http-event-source   |      |  sqs-event-source  |      |  scheduled-event-source  |
|                      |      |                    |      |                          |
+----------+-----------+      +---------+----------+      +------------+-------------+
           |                            |                              |
           |     events activate        |                              |
           |     a component            |                              |
           |                            |                              |
           |                            |                              |
           |                    +-------v-------+                      |
           +-------------------->               <----------------------+
                                |   component   |
                                |               +---------------+
                                +---------------+               |
                                                                |  container is an
                                                                |  instance of a component
                                                                |
                                   +----------+          +------+--------+
                                   |          |          |               |
                                   |   node   +----------+   container   |
                                   |          |          |               |
                                   +----------+          +---------------+

                                          nodes start/stop containers
                                          nodes proxy http requests to containers
                                          a collection of nodes forms a cluster
```

| Entity                  | Description          
| ----------------------- | ----------------
| component               | HTTP service or function
| http-event-source       | Config specifying HTTP hostname/path to reverse proxy to component
| scheduled-event-source  | Cron-like scheduling rule that triggers a component
| sqs-event-source        | SQS queue(s) to poll to trigger a component
| node                    | Machine instance running maelstromd
| container               | Instance of component running on a node

## CLI commands

`maelctl` command list

| Command                 | Description          
| ----------------------- | ----------------
| comp put                | Registers a component by name, replacing the previous version with the same name
| comp ls                 | Lists all registered components
| comp rm                 | Removes a component. This will also remove any related event sources.
| es put                  | Registers an event source for a component
| es ls                   | Lists event sources
| es rm                   | Removes an event source
| cfg put                 | ?? is config a file? a single name/value?
| node ls                 | Lists all nodes in the cluster
