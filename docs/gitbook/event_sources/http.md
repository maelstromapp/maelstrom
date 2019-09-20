
# HTTP

A HTTP event source activates your component when a request is received by `maelstromd` on the public HTTP port
with a `Host` header that matches the `hostname` value.

## Examples

```yaml
components:
  mywebapp:
    image: coopernurse/go-hello-http
    eventsources:
      # "myhttp_source" is the name of the event source
      # this name must be unique within a project yaml file
      myhttp_source:
        # http field indicates this is a HTTP event source
        http:
          # Hostname to match
          hostname: hello.example.org
```