# Pruning Images

Over time Maelstrom nodes may accumulate unused and unwanted Docker images.
Eventually this could consume all disk space on a node.

Maelstrom can be optionally configured to periodically remove untagged images and
images that are not associated with any components.  This feature is disabled by 
default, but can be easily enabled by setting a single environment variable.

## Examples

### Example 1: Remove exited containers and untagged images

This is similar in behavior to `docker system prune`. To enable this simply set
`MAEL_DOCKER_PRUNE_MINUTES` to specify the interval this job should run on each
Maelstrom node.

```
# run daily
MAEL_DOCKER_PRUNE_MINUTES=1440
```

### Example 2: Same as #1 plus all images not associated with a component

```
# run every 12 hours
MAEL_DOCKER_PRUNE_MINUTES=720
# remove all images NOT associated with a Maelstrom component
MAEL_DOCKER_PRUNE_UNREG_IMAGES=true
```

This example is a superset of Example 1. In addition to removing exited containers and untagged images
Maelstrom will remove all images not associated with a Maelstrom component. For example, if your system
has 5 components defined with these image names:

```
mycorp/web:v1
mycorp/accounting:v2
mycorp/intranet:v2
mycorp/hr
mycorp/api
```

And your maelstrom node has these images stored locally:

```
mycorp/web:v1
mycorp/accounting:v1
mycorp/accounting:v2
mycorp/intranet:v1
mycorp/intranet:v2
mycorp/hr
mycorp/api
othercorp/api
redis:3.2
```

Then these images would be removed:

```
mycorp/accounting:v1
mycorp/intranet:v1
othercorp/api
redis:3.2
```

### Example 3: Same as #2, but keep some images

What if you need the redis image and don't want it deleted? Maelstrom allows you to specify a list
of image name patterns to keep.

```
# run every 12 hours
MAEL_DOCKER_PRUNE_MINUTES=720
# remove all images NOT associated with a Maelstrom component
MAEL_DOCKER_PRUNE_UNREG_IMAGES=true
# comma separated list of names to retain. prefix and suffix * globs 
# are supported (but not full regexps)
MAEL_DOCKER_PRUNE_UNREG_KEEP=othercorp/*,redis*
```

In this configuration only these images would be removed:

```
mycorp/accounting:v1
mycorp/intranet:v1
```

