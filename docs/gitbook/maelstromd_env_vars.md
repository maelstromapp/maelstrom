
# maelstromd Environment Variables

`maelstromd` configuration is done via environment variables.
All variables are prefixed with `MAEL_`.

| Variable                        | Description                                  | Required? | Default |
|---------------------------------|----------------------------------------------|-----------|---------|
| MAEL_SQLDRIVER                  | sql db driver to use (sqlite3, mysql)        | Yes       | None    |
| MAEL_SQLDSN                     | DSN for maelstrom sql db                     | Yes       | None    |
| MAEL_PUBLICPORT                 | HTTP port to bind to for external HTTP reqs  | No        | 80      |
