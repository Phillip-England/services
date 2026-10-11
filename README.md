# services

`services` is a Bun and Ink terminal app for managing systemd services. It opens the TUI by default. You can browse live and boot state, inspect status, logs, and unit files, and create, configure, clone, start, stop, or remove local services.

## Install

Install Bun on the Linux machine where you manage services, then run:

```sh
make install
services
```

`make install` installs dependencies, compiles a standalone `services` executable, and copies it to `/usr/local/bin` by default. Use `sudo make install` if that directory needs administrator access. To install elsewhere, set `PREFIX`, for example `make install PREFIX="$HOME/.local"`.

To run from the checkout:

```sh
make run
```

Both commands launch the TUI without a subcommand. System mode asks for your sudo password without showing it on screen. It verifies the password before opening the TUI, then starts one elevated process for the session so later actions do not depend on an expiring sudo timestamp. If you already started as root, there is no second password prompt.

To manage your own user services without sudo, run `services --user` (or `bun src/index.tsx --user` from the checkout). System services use `/etc/systemd/system`; user services use `~/.config/systemd/user`.

## Keys

- `↑` / `↓` or `j` / `k`: select a service
- `Enter`: inspect status
- `a`: open the selected service's action menu
- `n`: create a service
- `/`: search names and descriptions
- `Tab`: switch between focus and all services
- `r`: refresh
- `?`: show help
- `Esc`: cancel or go back; `q`: quit

The focus list shows running, failed, and enabled services. The action menu includes status, logs, unit file, start/stop/restart, boot and mask controls, settings, clone, and removal. Changes require confirmation. Removing a service stops and disables it and deletes only a regular unit file in the local unit directory.

Settings use aliases such as `exec`, `workdir`, `restart`, and `env.PORT`, or a systemd key such as `Service.TimeoutStartSec`. Changing a running service's settings requires a restart to apply the runtime change.

## Development

```sh
make test
make build
```

Tests use a temporary unit directory and a fake systemd runner, so they run on a development machine without systemd.
