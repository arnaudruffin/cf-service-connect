package api

// Connection is the subset of plugin.CliConnection that this plugin relies on.
//
// Every method listed here is served by the CF CLI's plugin RPC server directly
// from the local CLI config -- none of them issue a CAPI request, so none of
// them depend on CAPI v2. This is deliberate and load-bearing: the richer
// helpers on plugin.CliConnection (GetService, GetApp, ...) and
// CliCommandWithoutTerminalOutput are routed through the CLI's *legacy* command
// registry (cf/commandsloader), which is hard-wired to /v2/* endpoints even in
// CF CLI v8. Depending on any of those would reintroduce a v2 dependency that
// cannot be removed by upgrading the CLI.
//
// Narrowing the surface to this interface also keeps the plugin testable
// without standing up an RPC server.
type Connection interface {
	// ApiEndpoint returns the targeted CF API endpoint, e.g.
	// "https://api.example.com".
	ApiEndpoint() (string, error)

	// AccessToken returns a refreshed UAA token, prefixed with its type, e.g.
	// "bearer eyJ...". The CLI refreshes the token as a side effect of this
	// call, so it is the correct way to obtain a usable token.
	AccessToken() (string, error)

	// IsSSLDisabled reports whether the user targeted the API with
	// --skip-ssl-validation.
	IsSSLDisabled() (bool, error)

	// GetCurrentSpace returns the targeted space. Only the GUID is used.
	GetCurrentSpace() (Space, error)

	// GetCurrentOrg returns the targeted organization.
	GetCurrentOrg() (Organization, error)
}

// Organization mirrors the fields of plugin_models.Organization that this
// plugin needs.
type Organization struct {
	Guid string //nolint:revive // field name matches plugin_models.Organization
	Name string
}

// Space mirrors the fields of plugin_models.Space that this plugin needs.
type Space struct {
	Guid string //nolint:revive // field name matches plugin_models.Space
	Name string
}
