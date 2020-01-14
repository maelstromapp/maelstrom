
# AWS Step Functions

A step function event source tells `maelstromd` to poll AWS for [activity tasks](https://docs.aws.amazon.com/step-functions/latest/dg/concepts-activities.html) using the `GetActivityTask` API. If a task is received, the related
component is activated and the task input is sent to the component via HTTP POST. The task input is sent as the
POST body. If the component responds with a 200 status code, `SendTaskSuccess` is called using the response body as
the `output` field. If a non-200 response is returned, or if the request times out, `SendTaskFailure` is called.

## Errors

If a non-200 response is returned, `SendTaskFailure` is called. You may optionally
set response HTTP headers for the "cause" and "error" to be sent back to AWS. These values will be
displayed in the step function UI.

See the [SendTaskFailure docs](https://docs.aws.amazon.com/step-functions/latest/apireference/API_SendTaskFailure.html) for more info on these fields.

`taskToken` is automatically set on the `SendTaskFailure` call and cannot be
overridden.

| HTTP Header      | SendTaskFailure Field    | Max Length                
|------------------|--------------------------|----------------------
| step-func-error  | error                    | 256
| step-func-cause  | cause                    | 32768

## Activity creation

`maelstromd` will call `CreateActivity` to resolve the ARN associated with the activity name, but you must 
create the state machine and reference the appropriate ARNs in the activity states.

Activity state ARNs have the following naming convention:

`arn:aws:states:<aws region>:<aws account id>:activity:<activity name>`

## Concurrency

You can control the number of tasks `maelstromd` will dequeue at a time per activity.  
For example, if you want your system to process no more than 10 tasks concurrently, set 
`maxconcurrency: 10` on the step function event source.

This is different than the `maxconcurrency` setting on the component.  The component setting specifies max concurrency
per instance of a component across all event sources, where the step function setting specifies the max concurrency 
across ALL instances of the component that will originate from this event source.

## Properties

| Property             |   Description                                                                 |  Default        
|----------------------|-------------------------------------------------------------------------------|-----------------
| activityname         | Name of step function activity to poll                                        | None (required)
| path                 | Request path to POST to on component                                          | None (required)
| maxconcurrency       | Max tasks to process at a time across all maelstromd instances                | 1
| concurrencyperpoller | Concurrent messages to process per polling process                            | 1

## Example

In the example below we register two activities: `split` and `sum`. `split` uses the default concurrency settings, so
it will only process a single task at a time across the cluster. `sum` specifies higher concurrency limits and will
process up to 10 tasks concurrently (5 per poller, for a max of 2 pollers).

```yaml
components:
  mywebapp:
    image: example/myapp
    eventsources:
      split:
        awsstepfunc:
          activityname: split
          path: /step/split
      sum:
        awsstepfunc:
          activityname: sum
          path: /step/sum
          maxconcurrency: 10
          concurrencyperpoller: 5
```
