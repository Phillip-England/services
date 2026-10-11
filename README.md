# services

`services` is a small Go service manager with an interactive terminal UI and command-line commands. It creates units, lists them, shows their effective configuration, edits settings through drop-ins, clones units, controls them, and reads logs.

## Install

On a Linux machine with systemd and Go installed:

```sh
go build -o services .
sudo install -m 0755 services /usr/local/bin/services
```

System services are stored in `/etc/systemd/system` and normally require `sudo`. Put `--user` immediately after `services` to manage units in `~/.config/systemd/user` instead.

## Terminal UI

```sh
services tui 'YOUR_SUDO_PASSWORD'
# No sudo password is needed for your own systemd user services:
services --user tui
```

The system TUI checks the password once and opens an elevated session. The opening list focuses on running, failed, and enabled services. Press `Tab` to show every service, `/` to search by name or description, arrow keys or `j`/`k` to move, `Enter` for status, `?` for all shortcuts, and `q` to quit. Press `Esc` to cancel a prompt. The UI asks for confirmation before stopping, restarting, disabling, masking, or removing a service. Removal stops and disables the service and deletes its local unit file.

Putting a password on the command line can expose it in shell history and process listings. If that matters on your machine, run `sudo services tui` instead; an already elevated TUI needs no password argument. The application installed above is named `services`. If you build it under the name `service`, the same subcommand works as `service tui 'YOUR_SUDO_PASSWORD'`.

## Common tasks

```sh
# Create, enable, and start a service in one command.
sudo services new api --exec '/opt/api/bin/server --port 8080' \
  --workdir /opt/api --user api --env APP_ENV=production --enable --start

# Or run `sudo services new api` and enter the command at the prompt.
sudo services ls
sudo services status api
sudo services show api
sudo services logs api -n 100
sudo services logs api -f
sudo services restart api

# Edit common settings without opening a unit file.
sudo services edit api
sudo services set api restart=always workdir=/srv/api env.PORT=8080
sudo services set api Service.TimeoutStartSec=30s
sudo services unset api env.PORT
sudo services restart api

# Copy an existing unit and its drop-ins, then customize it.
sudo services clone api api-staging
sudo services set api-staging exec='/opt/api/bin/server --port 8081'
sudo services enable api-staging
sudo services start api-staging

# Remove a local unit and CLI-created drop-ins.
sudo services rm api-staging --yes
```

`set` accepts short names for common settings: `exec`, `workdir`, `user`, `group`, `restart`, `restart-sec`, `description`, `after`, `wants`, `wanted-by`, and `type`. Any other systemd directive can be written as `Section.Key`, such as `Service.MemoryMax=512M`. Environment variables use `env.NAME=value`. Run `services edit NAME` for a guided prompt. `unset` removes settings written by this CLI; it does not remove values from the original unit.

New units and configuration changes trigger `systemctl daemon-reload`. Changing a running service's settings still requires `services restart NAME` to apply them. `rm` stops and disables the service before removing its local unit file. Package owned unit files outside the local unit directory cannot be removed with this command.

Run `services help` for the command summary.

## Development

```sh
go test ./...
go vet ./...
```

The tests use a temporary unit directory and a fake `systemctl` runner, so they also run on non-Linux development machines.
