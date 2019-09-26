
# HTTP

A HTTP event source activates your component when a request is received by `maelstromd` on the public HTTP port
with a `Host` header that matches the `hostname` and `pathprefix` value.

## Fields

| Field        | Description                                                                            | Default   
|--------------|----------------------------------------------------------------------------------------|-------------
| hostname     | Must match hostname on request (or Host header)                                        | None
| pathprefix   | Must match the beginning of the request path                                           | None
| stripprefix  | If true and pathprefix is set, pathprefix is removed from path before proxying request | false

## Rule sorting

Rules are sorted from most to least specific. Rules without hostnames sort below all rules with hostnames.
Rules with hostnames and paths sort above rules with only hostnames. Rules with both hostname and path are 
sorted by path length (longest first).  Here's an example of a sorted list of rules.

```
hostname     path
------------------------
foo.com      /aaaa
bar.com      /aaa
bar.com      /aa
foo.com      /a
foo.com
bar.com    
             /aaaa
             /aaa
             /a
```

## Examples

**Example 1: Match all requests to hello.example.org**

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
          # Either hostname or pathprefix must be provided
          # if both are provided, then request must match both
          hostname: hello.example.org
```

**Example 2: Match all requests to hello.example.org/foo/**

This doesn't strip the prefix, so requests to:  
http://hello.example.org/foo/ goes to http://x.y.z:port/foo/

```yaml
components:
  mywebapp:
    image: coopernurse/go-hello-http
    eventsources:
      myhttp_source:
        http:
          hostname: hello.example.org
          pathprefix: /foo/
```

**Example 3: Same as 2 but strip prefix**
 
http://hello.example.org/foo/ goes to http://x.y.z:port/

```yaml
components:
  mywebapp:
    image: coopernurse/go-hello-http
    eventsources:
      myhttp_source:
        http:
          hostname: hello.example.org
          pathprefix: /foo/
          stripprefix: true
```

**Example 4: Match all requests to /someapi**
 
Be careful with this as it will match any hostname.

```yaml
components:
  mywebapp:
    image: coopernurse/go-hello-http
    eventsources:
      myhttp_source:
        http:
          pathprefix: /someapi
```