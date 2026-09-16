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
		// The plugin uses only CAPI v3 and creates its own SSH tunnel, so it
		// needs very little from the CLI: ApiEndpoint, AccessToken,
		// IsSSLDisabled and GetCurrentSpace, all of which are served from the
		// local CLI config. Those have been available since v6.
		//
		// v8 is required all the same. CF CLI v6 and v7 resolve endpoints from
		// /v2/info, so `cf login` and `cf target` -- which a user must run
		// before this plugin can do anything -- do not work on a foundation with
		// CAPI v2 disabled. Requiring v8 makes that a clear up-front message
		// instead of a confusing failure inside the plugin.
		MinCliVersion: plugin.VersionType{
			Major: 8,
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
