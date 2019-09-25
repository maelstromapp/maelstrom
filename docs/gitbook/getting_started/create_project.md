# Create Project

## Create maelstrom.yml

Open another shell and create a new file: `maelstrom.yml` with the following contents:

```yaml
# example project
---
name: hello-mael
components:
  hello:
    image: docker.io/coopernurse/go-hello-http
    httpport: 8080
    httphealthcheckpath: /
    reservememory: 128
    eventsources:
      hello_http:
        http:
          hostname: hello.localhost
```

## Register file

Use `maelctl` to register this project with `maelstromd`:

```
$ /usr/local/bin/maelctl project put
```

Or via docker (we volume mounted the current dir to `/app` so we can access it there in the container):

```
docker exec maelstromd maelctl project put
```

You should see this output:

```
Project saved: hello-mael from file: maelstrom.yml
Type         Name                               Action
Component    hello-mael_hello                   Added
EventSource  hello-mael_hello_http              Added
```

`maelctl project put` uses `maelstrom.yml` by default, but you can specify a different
path using the `--file` switch if desired.
