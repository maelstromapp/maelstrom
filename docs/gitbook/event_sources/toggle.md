
# Toggle On and Off

New event sources are enabled by default, but **maelstrom** provides a way to toggle event sources
on and off. This can be used to pause cron or SQS pollers during maintenance windows, for example.

Toggling the status of an event source does not modify the event source itself, so the modified 
time and version of the event source does not change.

The current status of an event source is included in the `mael es ls` output.

Enabled status is cached in memory, so changes to event source status will not take effect immediately.
The default cache interval is 1 minute for SQS and cron event sources and 1 second for HTTP event sources.

## Examples 

### Example 1: Basic usage. Disable or enable all.

#### Disable all event sources:
```
maelctl es disable
```

#### Enable all event sources:
```
maelctl es enable
```

### Example 2: Disable all event sources by type

```
# type can be: http, sqs, cron
maelctl es disable --type=http
```

### Example 3: Enable all event sources by name prefix

```
# only event sources whose names start with "foo" will be modified
maelctl es enable --prefix=foo
```

### Example 4: Disable all events by project

```
maelctl es disable --project=finance
```
