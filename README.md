# Maelstrom

[![Build status](https://gitlab.com/coopernurse/maelstrom/badges/master/build.svg)](https://gitlab.com/coopernurse/maelstrom/-/jobs)

## Build and Test

```
# run tests
make test

# build CLI
make maelctl

# build daemon
make maelstromd
```

## Registering a component

```
# Create a component named 'hello'
maelctl comp put --json='{"name":"hello", "docker": { "image": "coopernurse/go-hello-http", "httpPort": 8080, "httpHealthCheckPath": "/"}}'

# Same as above but including a volume mount and some custom environment variables
maelctl comp put --json='{"name":"hello", "docker": { "image": "coopernurse/go-hello-http", "httpPort": 8080, "httpHealthCheckPath": "/", "env": ["MY_ENV1=foo", "OTHER_VAR=baz"], "volumes": [{ "source": "/home/james/src/maelstrom/tmp/static", "target": "/static" }] }}'

# Or bind a component to a specific network
maelctl comp put --json='{"name":"hello-compose", "docker": { "image": "coopernurse/go-hello-http", "httpPort": 8080, "httpHealthCheckPath": "/", "networkName": "composetest_default"}}'
```

## Event Sources

An event source is a rule describing a condition that invokes a component.  Event sources have a globally unique name.
Components may have zero or more event sources.

### HTTP

Allows the component to be invoked by an inbound HTTP request to the public maelstrom HTTP endpoint.

```
# Bind component to hostname: hello.example.org
maelctl es put --json='{"name": "hello-web", "componentName": "hello", "http": { "hostname": "hello.example.org" } }'
```

### Schedule job (cron)

Invokes the component automatically on the specified schedule.

```
maelctl es put --json='{"name": "hello-hourly", "componentName": "hello", 
  "cron": { "schedule": "3 32 * * * *", "http": { "path": "/count", "method": "GET" } } }'
```


## Dev notes

* [Docker Go API](https://docs.docker.com/develop/sdk/examples/#list-and-manage-containers)