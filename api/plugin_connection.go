package api

import "code.cloudfoundry.org/cli/plugin"

// pluginConnection adapts plugin.CliConnection to the narrow Connection
// interface used by this package.
type pluginConnection struct {
	cliConnection plugin.CliConnection
}

// NewConnection wraps a plugin.CliConnection so that only the v2-free subset of
// its methods is reachable.
func NewConnection(cliConnection plugin.CliConnection) Connection {
	return pluginConnection{cliConnection: cliConnection}
}

func (p pluginConnection) ApiEndpoint() (string, error) {
	return p.cliConnection.ApiEndpoint()
}

func (p pluginConnection) AccessToken() (string, error) {
	return p.cliConnection.AccessToken()
}

func (p pluginConnection) IsSSLDisabled() (bool, error) {
	return p.cliConnection.IsSSLDisabled()
}

func (p pluginConnection) GetCurrentSpace() (Space, error) {
	space, err := p.cliConnection.GetCurrentSpace()
	if err != nil {
		return Space{}, err
	}
	return Space{Guid: space.Guid, Name: space.Name}, nil
}
