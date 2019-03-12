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

# Bind component to hostname: hello.example.org
maelctl es put --json='{"name": "hello-web", "componentName": "hello", "http": { "hostname": "hello.example.org" } }'
```

## Dev notes

* [Docker Go API](https://docs.docker.com/develop/sdk/examples/#list-and-manage-containers)