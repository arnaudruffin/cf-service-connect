# Cloud Foundry CLI Service Connection Plugin [![Code Climate](https://codeclimate.com/github/18F/cf-service-connect/badges/gpa.svg)](https://codeclimate.com/github/18F/cf-service-connect)

This plugin makes it easy to connect to your databases or other Cloud Foundry service instances from your local machine. This condenses the steps listed in [Accessing Services with SSH](https://docs.cloudfoundry.org/devguide/deploy-apps/ssh-services.html) to a single command.

![demo screencast](demo.gif)

Requires Diego architecture with [SSH enabled](https://docs.cloudfoundry.org/running/config-ssh.html).

## Support

Currently supports (most) service brokers for the following:

* MongoDB (requires [`mongo` shell](https://docs.mongodb.com/getting-started/shell/installation/))
* MySQL (requires [`mysql` CLI](https://dev.mysql.com/doc/refman/8.0/en/installing.html))
* PostgreSQL (requires [`psql` CLI](https://postgresapp.com/documentation/cli-tools.html))
* Redis (requires [`redis-cli`](https://redis.io/topics/quickstart))

## Local installation

1. Install the [Cloud Foundry CLI](https://docs.cloudfoundry.org/cf-cli/install-go-cli.html) v8.0.0 or later. See [CF CLI and CAPI compatibility](#cf-cli-and-capi-compatibility) for why v8 is required.

    > **Warning**
    > On macOS, install the CLI from the official tap, **not** the
    > `cloudfoundry-cli` Homebrew formula:
    >
    > ```sh
    > brew install cloudfoundry/tap/cf-cli@8
    > ```
    >
    > The `cloudfoundry-cli` formula stamps a build date containing colons into
    > the CLI's version string, which is not valid semver. Any plugin command
    > then fails with `Invalid character(s) found in build meta data`. This is
    > [cloudfoundry/cli#3480](https://github.com/cloudfoundry/cli/issues/3480)
    > and affects all plugins, not just this one. Check with `cf --version`: if
    > the build metadata after the `+` contains a `:`, switch to the tap.
2. Install this plugin, using the appropriate binary URL from [the Releases page](https://github.com/18F/cf-service-connect/releases).

    ```sh
    cf install-plugin <binary_url>
    # will be of the format
    # https://github.com/cloud-gov/cf-service-connect/releases/download/<version>/cf-service-connect_<os>-<arch>
    # For non-M1 Macs, use `cf-service-connect_darwin_amd64`
    # For M1 Macs, use `cf-service-connect_darwin_arm64`
    ```

3. Install the CLI corresponding to your service type (see above).

## CF CLI and CAPI compatibility

This plugin uses only **CAPI v3**, so it works on foundations where the v2 API
has been disabled (`cc.temporary_enable_v2: false`, the default in
cf-deployment since v47.0.0 — see [RFC-0032: CF API v2 EOL](https://github.com/cloudfoundry/community/blob/main/toc/rfc/rfc-0032-cfapiv2-eol.md)).

Two consequences are worth knowing about:

**CF CLI v8 is required.** The plugin itself needs very little from the CLI — only
`ApiEndpoint`, `AccessToken`, `IsSSLDisabled` and `GetCurrentSpace`, all
available since v6 — but CF CLI v6 and v7 resolve their endpoints from
`/v2/info`, so `cf login` and `cf target` do not work at all against a
v2-disabled foundation.

The plugin does **not** declare a `MinCliVersion` to enforce this, deliberately:
declaring one makes the CLI parse its own version as semver, which fails on
Homebrew `cloudfoundry-cli` builds (see the warning under
[Local installation](#local-installation)). Since a user who cannot `cf login`
never reaches the plugin anyway, the requirement is documented here instead of
gated in code.

**The plugin creates its own SSH tunnel.** Earlier versions shelled out to
`cf ssh`. They cannot work on a v2-disabled foundation, because a plugin cannot
reach the CLI's v3 code paths: `CliCommandWithoutTerminalOutput` and helpers such
as `GetService` are dispatched through the CLI's *legacy* command registry, which
is hard-wired to `/v2/*` endpoints even in CF CLI v8. The tunnel is now
established in-process using the SSH proxy details from the CF API root document
(the v3-era replacement for `/v2/info`).

The plugin prefers the `app_ssh_ws` WebSocket endpoint when a foundation
advertises one, per [RFC-0029: CF SSH over WebSockets](https://github.com/cloudfoundry/community/blob/main/toc/rfc/rfc-0029-ssh-over-ws.md),
and falls back to the classic `app_ssh` TCP endpoint (port 2222) otherwise. This
means the plugin keeps working on foundations that have closed port 2222.


## Usage

> **Note**
> If you are using this tool to connect to a service on cloud.gov, your space must be configured with the `trusted_local_networks_egress` security group. Do this by running `cf bind-security-group trusted_local_networks_egress ORG --space SPACE` with your organization and space. Skipping this step will result in a `connection refused` error. For more, see [cloud.gov: Controlling egress traffic](https://cloud.gov/docs/management/space-egress/).

* `app_name` is the name of the app in your space you want to tunnel through.
* `service_instance_name` is the service instance you wish to connect to.

```shell
$ cf target --organization <org> --space <space>
$ cf connect-to-service <app_name> <service_instance_name>
Finding the service instance details...
Creating the service key...
Setting up SSH tunnel...
SSH tunnel created: localhost:38421 -> <service-host>:5432
Connecting client...
...
psql>
```

### Troubleshooting

**`Invalid character(s) found in build meta data`** — your `cf` binary reports a
version that is not valid semver. Install the CLI from
`cloudfoundry/tap/cf-cli@8` rather than the `cloudfoundry-cli` Homebrew formula;
see the warning under [Local installation](#local-installation).

**`This cf CLI plugin is not intended to be run on its own`, or
`dial tcp: lookup tcp/APP: unknown port`** — you ran the plugin binary directly.
Plugins are not standalone executables; the CLI passes them an RPC port as their
first argument. Install it and invoke it through `cf`:

```sh
go build -o cf-service-connect
cf install-plugin -f ./cf-service-connect
cf connect-to-service <app_name> <service_instance_name>
```

**`The SSH proxy rejected the one-time passcode`** — the SSH proxy asks the Cloud
Controller whether you may access that app instance, so this usually means the
authorization check failed rather than that the passcode was wrong. Check
`cf ssh-enabled APP`, `cf space-ssh-allowed SPACE`, that the app has a running
instance (`cf app APP`), and that you are targeting the right org and space.

**Service key creation remains in progress** — some service brokers report that
the key was created before its connection credentials are available. The plugin
retries incomplete credentials for up to five minutes before failing and
deleting the temporary key. Each credential request is limited to ten seconds,
so a blocked request does not consume the entire retry period. MongoDB
credentials that expose a `Hosts` array instead of a single `host` are also
supported; the tunnel targets the first listed host.

**`connection refused`, `error opening SSH connection`, or
`psql: could not connect to server: Connection refused`** — this is usually caused by being on a network that blocks the SSH port that this tool is trying to use. Try using a different network, or consider asking your network administrator to unblock the port (typically 22 and/or 2222). On foundations that advertise the `app_ssh_ws` endpoint the plugin tunnels SSH over `wss://` on port 443 instead, which avoids this class of problem.

### Optional: overriding `cf` CLI binary name

> **Note**
> `CF_BINARY_NAME` no longer has any effect. This plugin creates the SSH tunnel
> itself rather than running `cf ssh`, so no `cf` binary is invoked. The variable
> is still accepted, and the plugin prints a notice when it is set, so existing
> scripts keep working unchanged. You can remove it from your configuration.

Previously, in Windows or another environment where the Cloud Foundry CLI was installed as `cf7` or `cf8`, this variable told the plugin which binary name to use:

```shell
CF_BINARY_NAME=cf7 cf connect-to-service <app_name> <service_instance_name>
```

### Manual client connection

If you're using a non-default client (such as a GUI), run with the `-no-client` option to set up your client connection on your own.

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md)
