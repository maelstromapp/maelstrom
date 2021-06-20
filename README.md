# Maelstrom

[![GitHub Actions](https://github.com/maelstromapp/maelstrom/workflows/test/badge.svg)](https://github.com/maelstromapp/maelstrom/actions)

## Overview

Maelstrom is a HTTP reverse proxy and container orchestrator that starts and scales containers
automatically as needed.

A Maelstrom cluster is composed of nodes each running `maelstromd` pointed at a shared database which
stores configuration state about the components and event sources in the system.

* A component is a docker image with a related run configuration and environment variables.
* An event source is something that generates requests for a component. HTTP requests, scheduled jobs, and 
  messages in queues are all types of events.  Maelstrom currently supports HTTP, cron, and AWS SQS events.

![screencast](https://maelstromapp.com/images/demo_screencast.svg)
  
## Documentation

Full docs including a getting started guide are available at:
[https://maelstromapp.com/docs/](https://maelstromapp.com/docs/)

## Support

I'm available on a contract basis to help your team begin using Maelstrom.

Please contact James Cooper at: james@bitmechanic.com.

## Development

Maelstrom is written in Go and uses the [Go 1.11 module system](https://github.com/golang/go/wiki/Modules).

### Build and Test

Maelstrom uses [Barrister RPC](http://barrister.bitmechanic.com/) for communication between `maelctl` and `maelstromd` 
and for communication between cluster peers.

```
# install Barrister and Go bindings
pip install --pre --user barrister
go get github.com/coopernurse/barrister-go
go install github.com/coopernurse/barrister-go/idl2go

# compile IDL to go
make idl
```

```
# run tests
make test

# build CLI
make maelctl

# build daemon
make maelstromd
```

### Contributions

Pull requests are very welcome. I'd suggest opening an issue first so we can all discuss the
solution before major work is done. But if you have an itch to scratch feel free to open a PR
directly.

* Keep documentation in `docs/gitbook` up to date if you add a new feature
* Make sure all code has been formatted with `gofmt`

### Dev notes

* [Docker Go API](https://docs.docker.com/develop/sdk/examples/#list-and-manage-containers)
