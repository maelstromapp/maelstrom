
# Graceful Shutdown

Maelstrom attempts to shutdown gracefully when a SIGTERM or SIGINT is received.
The basic shutdown sequence is:

* Stop all background jobs
    * Cron service
    * Autoscaling loop
    * Event source pollers (including SQS)
    * Docker event monitor
* Remove node from `nodestate` table in database
* Notify cluster peers that node is leaving
* Stop HTTP listeners gracefully, draining any in flight requests
* Drain internal queues of any inflight requests
* Stop running containers

## AWS Auto Scale Lifecycle Hooks

In AWS you'll probably run Maelstrom using an Auto Scaling Group.
Auto Scaling Groups support a feature called Lifecycle Hooks which 
allows systems to receive notification when a machine is added or 
removed from the group.

Maelstrom has native support for Lifecycle Group termination events.
We highly recommend configuring this feature to provide nodes with
ample time to shutdown.

When this feature is enabled Maelstrom will poll the given SQS queue for
termination messages and broadcast them to all nodes in the cluster. The
matching node will perform the graceful shutdown steps listed above, then
acknowledge the message by making the `autoscaling:CompleteLifecycleAction`
call back to AWS.

If your ASG is associated with a load balancer, AWS will automatically remove
the instance from the load balancer when the SQS message is queued, so external
traffic to the host will stop before the shutdown sequence begins.

See the [EC2 Lifecycle Hooks docs](https://docs.aws.amazon.com/autoscaling/ec2/userguide/lifecycle-hooks.html)
for complete information on how this feature works.

Briefly the steps required to support this feature are:

1. Create a SQS queue for the termination event messages
2. Create an IAM role that grants the ASG service permission to send messages to the queue
3. Register a lifecycle hook specification with the ASG, which will cause termination events to be written to SQS
4. Configure `maelstromd` with the EC2 instance id and SQS queue URL
5. Ensure that Maelstrom nodes have proper IAM permissions

### CloudFormation Example

Here's a snippet of CloudFormation YAML that creates the queue and role (steps 1 and 2).

```yaml
  MaelASGTerminateQueue:
    Type: AWS::SQS::Queue
    Properties:
      QueueName: !Sub "${AWS::StackName}-MaelASG-terminate"

  MaelASGTerminateRole:
    Type: AWS::IAM::Role
    Properties:
      RoleName: MaelASGTerminateRole
      AssumeRolePolicyDocument:
        Version: "2012-10-17"
        Statement:
          - Effect: "Allow"
            Principal:
              Service:
                - "autoscaling.amazonaws.com"
            Action:
              - "sts:AssumeRole"
      Policies:
        - PolicyName: "MaelASGTerminatePolicy"
          PolicyDocument:
            Version: "2012-10-17"
            Statement:
              - Effect: "Allow"
                Action:
                  - sqs:SendMessage
                  - sqs:GetQueueUrl
                Resource: !Sub ${MaelASGTerminateQueue.Arn}
```

Here's an example of how to integrate that with your ASG:

```yaml
  MaelASG:
    Type: AWS::AutoScaling::AutoScalingGroup
    Properties:
      # <OTHER PROPERTIES HERE>
      LifecycleHookSpecificationList:
        - DefaultResult: CONTINUE
          HeartbeatTimeout: 600
          LifecycleHookName: "MaelASGTerminateHook"
          LifecycleTransition: "autoscaling:EC2_INSTANCE_TERMINATING"
          NotificationTargetARN: !Sub ${MaelASGTerminateQueue.Arn}
          RoleARN: !Sub ${MaelASGTerminateRole.Arn}
```

And the IAM permissions your Maelstrom nodes need in order to dequeue messages
and send the acknowledgement that the hook has completed.

```yaml
  MaelASGRole:
    Type: AWS::IAM::Role
    Properties:
      RoleName: MaelASGRole
      AssumeRolePolicyDocument:
        Version: "2012-10-17"
        Statement:
          - Effect: "Allow"
            Principal:
              Service:
                - "ec2.amazonaws.com"
            Action:
              - "sts:AssumeRole"
      Policies:
        - PolicyName: "MaelASGSQS"
          PolicyDocument:
            Version: "2012-10-17"
            Statement:
              - Effect: "Allow"
                Action:
                  - autoscaling:CompleteLifecycleAction
                Resource: "*"
              - Effect: "Allow"
                Action:
                  - sqs:ReceiveMessage
                  - sqs:DeleteMessage
                Resource: !Sub ${MaelASGTerminateQueue.Arn}
```

Finally, when starting `maelstromd` make sure to set these variables.

```bash
# required - if set, Maelstrom will internally poll this queue for termination messages
export MAEL_INSTANCEID=`curl -s http://169.254.169.254/latest/meta-data/instance-id`
export MAEL_AWSTERMINATEQUEUEURL="${MaelASGTerminateQueue}"
# optional, but recommended - this provides time for cluster members to notify each other
export MAEL_SHUTDOWNPAUSESECONDS=10
```
