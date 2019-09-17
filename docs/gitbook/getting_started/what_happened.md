
# What Happened??

## Container placement and activation

Let's take a moment to dig into what happened when you made this `curl` request.

1. You run `curl http://hello.localhost:8008/env`
1. `maelstromd` tries to resolve the request to a _component_
    1. The list of `http` event sources is loaded
    1. Each event source's hostname is compared with the request hostname
    1. If a match is found the component is returned
1. `maelstromd` asks the internal router for a reverse proxy endpoint for the component
    1. If a component is running somewhere in the cluster, the endpoint is returned.
    1. If no component is running, a _placement request_ is made and the request is internally queued
       until a container for that component is started somewhere in the cluster.
1. In this case no component was running so a placement request was made:
    1. The node attempts to acquire the `placement` role lock
    1. If it acquires the lock it runs the placement sequence locally. Otherwise it makes a RPC call to the node
       that owns the lock.
    1. The placement node decides which node in the cluster should run the component and notifies them.
    1. The target node pulls the docker image and starts the container
    1. The target node writes updated state to the db and broadcasts its state to its cluster peers
    1. The original node receives the updated state and proxies the request to that node
    1. The node proxies the request to the local container

Note that the above steps are performed regardless of cluster size. In our case our cluster has a single node, so
all operations were performed locally, but the exact same steps occur in clusters with multiple nodes.

## Unsuccessful request

Try making a request with a different hostname. No event source will map to the hostname and an error will be
returned:

```
$ curl http://bogus.localhost:8008/
No component matches the request
```

Congratulations, you've successfully run your first maelstrom project.  Let's try updating it and adding a cron
event source.
