
# Cron

A cron event source activates your component at a timed interval specified in the `schedule` field.
When a cron event occurs, a HTTP request is made to your component on the path specified.

This allows you to run periodic jobs without an additional scheduler.

In a cluster with more than one node, a single node is responsible for triggering cron events at any 
time.

The cron system is implementing using [robfig/cron](https://godoc.org/github.com/robfig/cron), so 
full documentation on the different schedule string formats is available in that package's documentation.

NOTE: Make sure to use quotes around the `schedule` value to avoid YAML parsing issues with `*` characters.

## Examples

### Example 1: Run every hour - make GET request

```yaml
components:
  mywebapp:
    image: coopernurse/go-hello-http
    eventsources:
      backup_db_hourly:
        cron:
          schedule: "@every 1h"
          http:
            method: GET
            path: /jobs/backup_db
```

### Example 2: Make a POST request nightly with custom headers

```yaml
components:
  mywebapp:
    image: coopernurse/go-hello-http
    eventsources:
      some_job_nightly:
        cron:
          schedule: "30 * * * *"
          http:
            method: POST
            path: /jobs/some_job
            data: '{"key": "value", "key2", "value2}'
            headers:
              - name: Content-Type
                value: application/json
```