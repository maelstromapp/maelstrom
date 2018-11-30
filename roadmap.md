# maelstrom roadmap

## m1

* Stub maelstromd
    * Docker container running in privileged mode
    * Listens on 80 externally (reverse proxy for components)
    * Listens on ?? for management requests
* Stub CLI to manage cluster - maelctl
    * Talks to maelstromd on the management port
    * Implement commands:
        * PutComponent
        * GetComponents
* Persistent state backend
    * SQL db only (gorp)
* Activate component when HTTP request received
    * Docker components only (no zip functions)
    * Pull image, start container, tag container
* Stop component after x seconds of inactivity
* Load state of existing components from docker if maelstromd restarted
* Project web site (hugo?)
    * Home page
    * Getting started page
    * Release history
    * maelctl reference
    * Contributor guidelines
    * Issue template (?)
* CI
    * Run tests on merge
    * Deploy web site on merge

## m2

* Security
    * SSL cert support for external port
    * LetsEncrypt support
    * Mandatory SSL cert for management port
        * If no real cert configured, use self-signed
            * maelctl: add insecure flag for this case
* Process limits
    * RAM
    * CPU share
    * Max request time
* Function support
    * API to pull task from maelstromd
    * API to invoke task
* Function ZIP file storage implementations
    * Filesystem
    * S3
    * DigitalOcean spaces
* Function API bindings
    * Go
    * Python
    * NodeJS

## m3

* Cluster support
    * Discover peers via persistent store
* Component autoscaling
    * Request rate per second (?)
    * Response time (?)
* Terraform templates
    * AWS
    * DigitalOcean

## m4

* Queue event sources
    * SQS
* Scheduled job support
* Deploy hooks (?)
    * Some solution for schema migrations
* Persistent state backend
    * Add Redis (?)
    * Add etcd (?)

