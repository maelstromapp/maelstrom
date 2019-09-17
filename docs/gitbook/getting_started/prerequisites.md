
# Prerequisites

## Docker

You must have Docker installed and running before you can install `maelstromd`.
If you don't have Docker running, follow the 
[Docker Engine installation guide](https://docs.docker.com/install/)

Test your Docker installation by running the `hello-world` image.  You should see something like this:

```
$ docker run hello-world

Hello from Docker!
This message shows that your installation appears to be working correctly.
```

Once this is working you're ready to install maelstrom.

## Root access?

Recent Docker installations create a `docker` group. Members of this group can connect to the local Docker daemon
without being root.

If you try the above `docker run hello-world` and get a permission denied error you might try running the command
via sudo `sudo docker run hello-world`.  If this works, you'll need to run `maelstromd` via sudo as well since it
needs the ability to communicate with the Docker daemon.
