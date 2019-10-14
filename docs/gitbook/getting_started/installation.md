# Installation

## Download

Pre-built binaries are available for Linux x86_64. On other platforms you'll need to use a pre-compiled docker image.

```
# Use any directory you wish that's in your PATH
cd /usr/local/bin
# download binaries
curl -LO https://download.maelstromapp.com/latest/linux_x86_64/maelstromd
curl -LO https://download.maelstromapp.com/latest/linux_x86_64/maelctl
chmod 755 maelstromd maelctl
```

## Configure Environment

`maelstromd` may be configured by setting environment variables manually, or via a configuration file with `name=value`
lines.

Create a file called `mael.env` with the following content:

```
MAEL_SQL_DRIVER=sqlite3
MAEL_SQL_DSN=maelstrom.db?cache=shared&_journal_mode=MEMORY
MAEL_PUBLIC_PORT=8008
```

## Start maelstromd

Run `maelstromd` - if using the Linux binaries:

```
$ /usr/local/bin/maelstromd -f mael.env
```

Or if using the docker image:

```
# add a -d switch if you want to background this process
# otherwise it will run in the foreground
docker run --name maelstromd -p 8374:8374 -p 8008:8008 -v `pwd`:/app --privileged \
    -v /var/run/docker.sock:/var/run/docker.sock --env-file mael.env \
    coopernurse/maelstrom maelstromd
```

You should see output that looks like this:
```
13:02:29.778603 INF ~ maelstromd: starting
13:02:29.791538 INF ~ handler: creating DockerHandlerFactory maelstromUrl: http://172.18.0.1:8374
13:02:29.819370 INF ~ cluster: added node nodeId: H5ML:TQZ7:7TKL:DLUP:JB7A:C2VA:XYCW:TEOM:FE6L:65PS:FISY:YWOO
   remoteNode: H5ML:TQZ7:7TKL:DLUP:JB7A:C2VA:XYCW:TEOM:FE6L:65PS:FISY:YWOO
13:02:29.819441 INF ~ maelstromd: created NodeService
   nodeId: H5ML:TQZ7:7TKL:DLUP:JB7A:C2VA:XYCW:TEOM:FE6L:65PS:FISY:YWOO peerUrl: http://192.168.1.76:8374 numCPUs: 8
13:02:29.823934 INF ~ maelstromd: aws session initialized
13:02:29.823982 INF ~ maelstromd: starting HTTP servers publicPort: 8008 privatePort: 8374
13:02:29.824123 INF ~ cron: starting cron service refreshRate: 1m0s
13:02:29.825061 INF ~ cron: acquired role lock, starting cron
```

You may stop `maelstromd` at any time by pressing `control-c`, but let's keep it running and create our first project.


