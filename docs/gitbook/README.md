
# Maelstrom Documentation

## Welcome!

Thanks for checking out **maelstrom**, the simple container orchestrator.
This book will explain how to install maelstrom, how to create your first project,
and how to configure different event sources.

The appendix sections provide a reference for `maelstromd` configuration variables
and the `maelstrom.yml` project YAML format.

## Concepts

**maelstrom** is built on top of [Docker](https://www.docker.com/) and provides a way to 
start and stop Docker containers in response to inbound requests.

### Docker concepts

| Term                 | Meaning                                                |
|----------------------|--------------------------------------------------------|
| [image](https://docs.docker.com/glossary/?term=image)| A named ordered collection of root filesystem layers  
| [container](https://docs.docker.com/glossary/?term=container) | Runtime instance of a docker image 

### Maelstrom concepts

| Term         | Meaning                                                              |
|--------------|----------------------------------------------------------------------|
| component    | Spec defining how to run a container. Specifies min/max limits, RAM requirements, logging.
| event source | Spec that defines an external request source to map to a component. Event sources include HTTP, Amazon SQS, and cron.
| project      | Collection of components and event sources. Defined in a YAML file.
| node         | A physical or virtual machine running the `maelstromd` daemon. 
| cluster      | A collection of 1..n maelstrom nodes that share a common database.

These concepts will be explained in more detail in later chapters.
