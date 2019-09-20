
# AWS SQS

A SQS event source tells `maelstromd` to poll a SQS queue for messages. If a message is received, the related
component is activated and the message body is sent to the component via HTTP POST. The message body is sent as the
POST body. If the component responds with a 200 status code, the SQS message is deleted from the queue. Otherwise
the message will stay in the queue and become eligible for redelivery when the visibility timeout elapses.

While SQS has no native notion of priority queueing, this event source optionally supports polling a set of queues
that share a common name prefix. For example, given an event source configured with `nameasprefix: true` and
a set of queues named:

```
resize-image-0
resize-image-1
resize-image-2
```

`maelstromd` will poll the queues in name order, draining the "0" queue completely, then "1", then "2".

While driaining lower priority queues, `maelstromd` will periodically reset and poll from the head of the list
in order to re-check the higher priority queues.

## Concurrency

You can control the number of messages `maelstromd` will dequeue at a time.  For example, if you want your system
to process no more than 10 messages concurrently, set `maxconcurrency: 10` on the SQS event source.

This is different than the `maxconcurrency` setting on the component.  The component setting specifies max concurrency
per instance of a component across all event sources, where the SQS setting specified the max concurrency across ALL
instances of the component that will originate from this event source.

If you are using a SQS FIFO queue you may also wish to set `concurrencyperpoller: 1` to ensure that messages are 
processed in the strict order they are dequeued.

The max number of pollers =  `ceil(sqs-maxconcurrency / concurrencyperpoller)`

## Properties

| Property             |   Description                                                                 |  Default        
|----------------------|-------------------------------------------------------------------------------|-----------------
| queuename            | Name of queue (or queue prefix) to poll                                       | None (required) 
| nameasprefix         | If true, treat queuename as prefix. Poll all queues starting with that name   | false 
| path                 | Request path to POST to on component                                          | None (required)
| maxconcurrency       | Max concurrency messages to process via SQS across all instances              | 10
| messagesperpoll      | Messages to receive per polling attempt (1..10)                               | 1
| concurrencyperpoller | Concurrent messages to process per polling process                            | messagesperpoll
| visibilitytimeout    | Seconds to mark message invisible before it is eligible to dequeue again      | 300


## Examples

### Example 1: Poll a single queue and process up to 15 messages concurrently

```yaml
components:
  mywebapp:
    image: example/myapp
    eventsources:
      process_order_sqs:
        sqs:
          queuename: pending-orders
          path: /queues/pendingorder
          maxconcurrency: 15
          visibilitytimeout: 600
```

### Example 2: Poll a set of queues with a name prefix. Pull 10 messages at a time per poller.

```yaml
components:
  mywebapp:
    image: example/myapp
    eventsources:
      process_order_sqs:
        sqs:
          queuename: resize-image-
          nameasprefix: true
          path: /queues/resizeimage
          maxconcurrency: 50
          messagesperpoll: 10
          concurrencyperpoller: 10
```