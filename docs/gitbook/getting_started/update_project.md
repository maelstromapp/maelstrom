
# Update Project

## Add cron event source

Edit `maelstrom.yml` and add a `cron` event source. The full file should look like this:

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
      # cron event source here:
      hello_cron:
        cron:
          schedule: "@every 10s"
          http:
            method: GET
            path: /log
```

Then re-run `maelctl project put`

```
$ /usr/local/bin/maelctl project put
```

## Wait 10 seconds..

We just told `maelstromd` to make a `GET` request to `/log` on this component every 10 seconds.

If the component is not already running `maelstromd` will start it.

## Watch the logs

Run this to see the logs for the container. The `/count` endpoint prints the time to STDOUT each time it's invoked.

```
$ /usr/local/bin/maelctl logs
[hello-mael_hello]	Current time: 2019-09-17 21:12:11.001191209 +0000 UTC m=+942.075217013
[hello-mael_hello]	Current time: 2019-09-17 21:12:21.001580013 +0000 UTC m=+952.075605806
[hello-mael_hello]	Current time: 2019-09-17 21:12:31.001557084 +0000 UTC m=+962.075582883
``` 
