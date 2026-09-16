package main

import (
	"errors"
	"flag"
	"log"

	"code.cloudfoundry.org/cli/plugin"

	"github.com/cloud-gov/cf-service-connect/connector"
	"github.com/cloud-gov/cf-service-connect/version"
)

const subcommand = "connect-to-service"

// ServiceConnectPlugin is the struct implementing the interface defined by the core CLI. It can
// be found at  "code.cloudfoundry.org/cli/plugin/plugin.go"
type ServiceConnectPlugin struct{}

func (c *ServiceConnectPlugin) parseOptions(args []string) (options connector.Options, err error) {
	metadata := c.GetMetadata()
	command := metadata.Commands[0]
	flags := flag.NewFlagSet(command.Name, flag.ExitOnError)
	option := "no-client"
	noClient := flags.Bool(option, false, command.UsageDetails.Options[option])

	err = flags.Parse(args[1:])
	if err != nil {
		return
	}

	nonFlagArgs := flags.Args()
	if len(nonFlagArgs) != 2 {
		err = errors.New("Wrong number of arguments")
		return
	}

	options = connector.Options{
		AppName:             nonFlagArgs[0],
		ServiceInstanceName: nonFlagArgs[1],
		ConnectClient:       !(*noClient),
	}
	return
}

// Run is the entry point when the core CLI is invoking a command defined
// by the plugin. The first parameter, plugin.CliConnection, is a struct that can
// be used to invoke cli commands. The second paramter, args, is a slice of
// strings. args[0] will be the name of the command, and will be followed by
// any additional arguments a cli user typed in.
func (c *ServiceConnectPlugin) Run(cliConnection plugin.CliConnection, args []string) {
	// check to ensure it's the right subcommand, not others like CLI-MESSAGE-UNINSTALL
	if args[0] != subcommand {
		return
	}

	opts, err := c.parseOptions(args)
	if err != nil {
		log.Fatalln(err)
	}

	err = connector.Connect(cliConnection, opts)
	if err != nil {
		log.Fatalln(err)
	}
}

// GetMetadata returns the plugin information for the CLI to consume.
func (c *ServiceConnectPlugin) GetMetadata() plugin.PluginMetadata {
	return plugin.PluginMetadata{
		Name: "ServiceConnect",
		Version: plugin.VersionType{
			Major: version.Major,
			Minor: version.Minor,
			Build: version.Build,
		},
		// MinCliVersion is deliberately left unset (0.0.0), which makes the CLI
		// skip its version check entirely.
		//
		// CF CLI v8 *is* required in practice, and the README says so: v6 and v7
		// resolve endpoints from /v2/info, so `cf login` and `cf target` do not
		// work at all against a foundation with CAPI v2 disabled. But declaring
		// it here does more harm than good.
		//
		// Declaring any non-zero MinCliVersion makes the CLI parse *its own*
		// version as semver (plugin/rpc/cli_rpc_server.go IsMinCliVersion). The
		// Homebrew `cloudfoundry-cli` formula stamps an RFC3339 build date into
		// that version, and the colons are illegal in semver build metadata, so
		// the parse fails and every plugin command aborts with:
		//
		//	Invalid character(s) found in build meta data "2026-08-28T19:04:31Z"
		//
		// That is upstream cloudfoundry/cli#3480, closed as "use
		// cloudfoundry/tap/cf-cli@8 instead of the cloudfoundry-cli formula".
		// Since the plugin only uses CLI methods available since v6, and a user
		// who cannot `cf login` never reaches the plugin at all, the version gate
		// blocks nothing we actually need while breaking a common install path.
		//
		// Do not set this without first confirming the upstream bug is fixed.
		MinCliVersion: plugin.VersionType{
			Major: 0,
			Minor: 0,
			Build: 0,
		},
		Commands: []plugin.Command{
			{
				Name:     subcommand,
				HelpText: "Open a shell that's connected to a database service instance",
				UsageDetails: plugin.Usage{
					Usage: "\n   cf " + subcommand + " [-no-client] <app_name> <service_instance_name>",
					Options: map[string]string{
						"no-client": "If this param is passed, the CLI client for the service won't be started, and the connection information will be printed to the console. Useful for connecting to the service through a GUI.",
					},
				},
			},
		},
	}
}

func main() {
	plugin.Start(new(ServiceConnectPlugin))
}
