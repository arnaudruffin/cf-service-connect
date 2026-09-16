// Package api provides the CAPI v3 access this plugin needs.
//
// The plugin talks exclusively to CAPI v3 so that it keeps working on
// foundations where the v2 API has been disabled (capi-release's
// cc.temporary_enable_v2: false, which is the default as of cf-deployment
// v47.0.0 -- see cloudfoundry/community RFC-0032 "CF API v2 EOL").
package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cloudfoundry/go-cfclient/v3/client"
	"github.com/cloudfoundry/go-cfclient/v3/config"
	"github.com/cloudfoundry/go-cfclient/v3/resource"

	"github.com/cloud-gov/cf-service-connect/logger"
)

// keyBindingType is the CAPI v3 service credential binding type that
// corresponds to a v2 "service key".
const keyBindingType = "key"

// jobPollTimeout bounds how long we wait for an asynchronous bind/unbind job.
// Brokers that provision credentials can be slow, but an unbounded wait would
// hang a user's terminal indefinitely.
const jobPollTimeout = 5 * time.Minute

// jobPollInterval is how often an in-flight job is re-checked.
const jobPollInterval = 2 * time.Second

// Client provides the CAPI v3 operations required to connect to a service
// instance. Construct one with NewClient.
type Client struct {
	// tokenFunc returns a current "bearer <jwt>" credential. Calling it makes
	// the CF CLI refresh the token if needed, so it is the only supported way
	// to obtain one.
	tokenFunc func() (string, error)

	spaceGUID   string
	apiURL      string
	sslDisabled bool

	// pollInterval is how often an in-flight job is re-checked. It is a field
	// rather than a constant so that tests can shorten it.
	pollInterval time.Duration

	// mu guards the lazily (re)built underlying client.
	mu       sync.Mutex
	cf       *client.Client
	tokenExp time.Time
}

// NewClient builds a CAPI v3 client from the CF CLI's current session.
//
// The endpoint, OAuth token, TLS preference and targeted space all come from
// the CLI via conn, so the plugin never handles credentials itself and honours
// whatever the user targeted with `cf api` / `cf target`.
func NewClient(conn Connection) (*Client, error) {
	apiURL, err := conn.ApiEndpoint()
	if err != nil {
		return nil, fmt.Errorf("could not determine the CF API endpoint: %w", err)
	}
	if apiURL == "" {
		return nil, errors.New("no CF API endpoint is targeted; use `cf api` to target one")
	}

	space, err := conn.GetCurrentSpace()
	if err != nil {
		return nil, fmt.Errorf("could not determine the targeted space: %w", err)
	}
	if space.Guid == "" {
		return nil, errors.New("no space is targeted; use `cf target -o ORG -s SPACE` first")
	}

	sslDisabled, err := conn.IsSSLDisabled()
	if err != nil {
		return nil, fmt.Errorf("could not determine the TLS validation setting: %w", err)
	}

	c := &Client{
		tokenFunc:    conn.AccessToken,
		spaceGUID:    space.Guid,
		apiURL:       apiURL,
		sslDisabled:  sslDisabled,
		pollInterval: jobPollInterval,
	}

	// Build eagerly so that an unusable session (not logged in, unreachable
	// API) is reported before any user-visible work starts.
	if _, err := c.client(); err != nil {
		return nil, err
	}

	logger.Debugf("CAPI v3 client targeting %s, space %s (%s)\n", apiURL, space.Name, space.Guid)
	return c, nil
}

// client returns the underlying go-cfclient, rebuilding it with a fresh token
// when the current one is at or near expiry.
//
// This matters because a connect-to-service session lasts as long as the user
// keeps their database client open, while UAA access tokens are short-lived
// (10 minutes on cloud.gov). go-cfclient is configured without a refresh token
// -- the plugin API does not expose one -- so it cannot refresh by itself and
// fails with "token expired and refresh token is not set". Asking the CLI for a
// new token and rebuilding avoids that, which is what makes the deferred
// service-key deletion work after a long session.
func (c *Client) client() (*client.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cf != nil && time.Until(c.tokenExp) > tokenRefreshMargin {
		return c.cf, nil
	}

	token, err := c.tokenFunc()
	if err != nil {
		return nil, fmt.Errorf("could not obtain a CF API access token: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("not logged in; use `cf login` first")
	}

	cf, err := c.buildClient(token)
	if err != nil {
		return nil, err
	}

	expiry, err := accessTokenExpiry(token)
	if err != nil {
		// Without an expiry we cannot tell when to refresh, so rebuild on every
		// call rather than risk using a token past its lifetime.
		logger.Debugf("could not determine access token expiry (%v); will refresh on each call\n", err)
		expiry = time.Time{}
	} else {
		logger.Debugf("access token valid for %s\n", time.Until(expiry).Round(time.Second))
	}

	c.cf = cf
	c.tokenExp = expiry
	return cf, nil
}

func (c *Client) buildClient(token string) (*client.Client, error) {
	options := []config.Option{
		// AccessToken() yields "bearer <jwt>"; go-cfclient strips the prefix.
		config.Token(token, ""),
		config.UserAgent(UserAgent()),
	}
	if c.sslDisabled {
		options = append(options, config.SkipTLSValidation())
	}

	cfg, err := config.New(c.apiURL, options...)
	if err != nil {
		return nil, fmt.Errorf("could not configure the CF API client: %w", err)
	}

	cf, err := client.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("could not create the CF API client: %w", err)
	}
	return cf, nil
}

// ServiceInstance identifies a service instance along with the offering and
// plan names, which are used to pick a matching database client.
type ServiceInstance struct {
	GUID     string
	Name     string
	Offering string
	Plan     string
}

// App identifies an application to tunnel through.
type App struct {
	GUID  string
	Name  string
	State string
}

// GetServiceInstance looks up a managed or user-provided service instance by
// name in the targeted space, along with its plan and offering names.
func (c *Client) GetServiceInstance(ctx context.Context, name string) (ServiceInstance, error) {
	cf, err := c.client()
	if err != nil {
		return ServiceInstance{}, err
	}

	opts := client.NewServiceInstanceListOptions()
	opts.Names.EqualTo(name)
	opts.SpaceGUIDs.EqualTo(c.spaceGUID)

	instances, err := cf.ServiceInstances.ListAll(ctx, opts)
	if err != nil {
		return ServiceInstance{}, fmt.Errorf("could not look up service instance %q: %w", name, err)
	}
	if len(instances) == 0 {
		return ServiceInstance{}, fmt.Errorf("service instance %q not found in the targeted space", name)
	}
	instance := instances[0]

	result := ServiceInstance{
		GUID: instance.GUID,
		Name: instance.Name,
	}

	// User-provided instances have no plan or offering. Leave both empty rather
	// than failing: the caller can still tunnel, falling back to -no-client
	// behaviour when no client matches.
	if instance.Relationships.ServicePlan == nil || instance.Relationships.ServicePlan.Data == nil {
		return result, nil
	}
	planGUID := instance.Relationships.ServicePlan.Data.GUID
	if planGUID == "" {
		return result, nil
	}

	// Resolve the plan, then the offering it belongs to.
	//
	// go-cfclient's ServicePlans.GetIncludeServiceOffering would save a request
	// but indexes included.service_offerings[0] without a bounds check, so a
	// response without that block would panic inside the library. Two guarded
	// requests are worth more than one saved round trip.
	plan, err := cf.ServicePlans.Get(ctx, planGUID)
	if err != nil {
		// The plan may be invisible to this user even when the instance is not.
		// That only costs client auto-detection, so warn and carry on rather
		// than aborting the connection.
		logger.Debugf("could not resolve plan %s for service instance %q: %v\n", planGUID, name, err)
		return result, nil
	}
	result.Plan = plan.Name

	offeringGUID := ""
	if plan.Relationships.ServiceOffering.Data != nil {
		offeringGUID = plan.Relationships.ServiceOffering.Data.GUID
	}
	if offeringGUID == "" {
		return result, nil
	}

	offering, err := cf.ServiceOfferings.Get(ctx, offeringGUID)
	if err != nil {
		logger.Debugf("could not resolve offering %s for service instance %q: %v\n", offeringGUID, name, err)
		return result, nil
	}
	result.Offering = offering.Name

	return result, nil
}

// GetApp looks up an app by name in the targeted space.
func (c *Client) GetApp(ctx context.Context, name string) (App, error) {
	cf, err := c.client()
	if err != nil {
		return App{}, err
	}

	opts := client.NewAppListOptions()
	opts.Names.EqualTo(name)
	opts.SpaceGUIDs.EqualTo(c.spaceGUID)

	apps, err := cf.Applications.ListAll(ctx, opts)
	if err != nil {
		return App{}, fmt.Errorf("could not look up app %q: %w", name, err)
	}
	if len(apps) == 0 {
		return App{}, fmt.Errorf("app %q not found in the targeted space", name)
	}

	return App{
		GUID:  apps[0].GUID,
		Name:  apps[0].Name,
		State: apps[0].State,
	}, nil
}

// FindServiceKey returns the GUID of the service key with the given name on the
// given service instance. found is false when no such key exists.
func (c *Client) FindServiceKey(ctx context.Context, serviceInstanceGUID, keyName string) (guid string, found bool, err error) {
	cf, err := c.client()
	if err != nil {
		return "", false, err
	}

	opts := client.NewServiceCredentialBindingListOptions()
	opts.ServiceInstanceGUIDs.EqualTo(serviceInstanceGUID)
	opts.Type.EqualTo(keyBindingType)
	opts.Names.EqualTo(keyName)

	bindings, err := cf.ServiceCredentialBindings.ListAll(ctx, opts)
	if err != nil {
		return "", false, fmt.Errorf("could not list service keys for the service instance: %w", err)
	}
	if len(bindings) == 0 {
		return "", false, nil
	}
	return bindings[0].GUID, true, nil
}

// CreateServiceKey creates a service key (a v3 service credential binding of
// type "key") and returns its GUID.
//
// Unlike v2's POST /v2/service_keys, the v3 endpoint is asynchronous: it
// returns 202 with a job to poll. This method does not return until the job has
// completed, so the credentials are guaranteed to be readable afterwards.
func (c *Client) CreateServiceKey(ctx context.Context, serviceInstanceGUID, keyName string) (string, error) {
	cf, err := c.client()
	if err != nil {
		return "", err
	}

	create := resource.NewServiceCredentialBindingCreateKey(serviceInstanceGUID, keyName)

	jobGUID, binding, err := cf.ServiceCredentialBindings.Create(ctx, create)
	if err != nil {
		return "", fmt.Errorf("could not create service key %q: %w", keyName, err)
	}

	// A synchronous response returns the binding directly and no job.
	if jobGUID == "" {
		if binding == nil {
			return "", fmt.Errorf("creating service key %q returned neither a job nor a binding", keyName)
		}
		return binding.GUID, nil
	}

	logger.Debugf("waiting on job %s for service key creation\n", jobGUID)
	if err := c.pollJob(ctx, jobGUID); err != nil {
		return "", fmt.Errorf("service key %q was not created: %w", keyName, err)
	}

	// An asynchronous create response carries no GUID, so look it up.
	guid, found, err := c.FindServiceKey(ctx, serviceInstanceGUID, keyName)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("service key %q was reported created but cannot be found", keyName)
	}
	return guid, nil
}

// DeleteServiceKey deletes a service key by GUID, waiting for the asynchronous
// unbind job to finish.
func (c *Client) DeleteServiceKey(ctx context.Context, keyGUID string) error {
	cf, err := c.client()
	if err != nil {
		return err
	}

	jobGUID, err := cf.ServiceCredentialBindings.Delete(ctx, keyGUID)
	if err != nil {
		return fmt.Errorf("could not delete service key %s: %w", keyGUID, err)
	}
	if jobGUID == "" {
		return nil
	}

	logger.Debugf("waiting on job %s for service key deletion\n", jobGUID)
	if err := c.pollJob(ctx, jobGUID); err != nil {
		return fmt.Errorf("service key %s was not deleted: %w", keyGUID, err)
	}
	return nil
}

// GetServiceKeyCredentials returns the credentials object of a service key, as
// served by GET /v3/service_credential_bindings/:guid/details.
func (c *Client) GetServiceKeyCredentials(ctx context.Context, keyGUID string) (map[string]any, error) {
	cf, err := c.client()
	if err != nil {
		return nil, err
	}

	details, err := cf.ServiceCredentialBindings.GetDetails(ctx, keyGUID)
	if err != nil {
		return nil, fmt.Errorf("could not read the credentials of service key %s: %w", keyGUID, err)
	}
	if details == nil || len(details.Credentials) == 0 {
		return nil, fmt.Errorf("service key %s has no credentials; this service may not support service keys", keyGUID)
	}
	return details.Credentials, nil
}

// SSHEndpoint describes how to reach the SSH proxy for app containers.
type SSHEndpoint struct {
	// Address is the "host:port" of the legacy TCP SSH proxy, from the root
	// document's app_ssh link. Empty when the operator has disabled it.
	Address string

	// WebSocketURL is the wss:// URL of the SSH proxy's WebSocket listener,
	// from the root document's app_ssh_ws link (cloudfoundry/community
	// RFC-0029). Empty when the foundation does not offer it.
	WebSocketURL string

	// HostKeyFingerprint is the expected SSH host key fingerprint.
	HostKeyFingerprint string

	// OAuthClient is the UAA client used to mint one-time SSH passcodes,
	// normally "ssh-proxy".
	OAuthClient string
}

// GetSSHEndpoint reads the SSH proxy details from the CF API root document.
//
// The root document is the v3-era replacement for /v2/info, which is why this
// works on foundations with v2 disabled.
func (c *Client) GetSSHEndpoint(ctx context.Context) (SSHEndpoint, error) {
	root, err := c.getRootDocument(ctx)
	if err != nil {
		return SSHEndpoint{}, err
	}

	endpoint := SSHEndpoint{
		Address:            root.Links.AppSSH.HREF,
		WebSocketURL:       root.Links.AppSSHWS.HREF,
		HostKeyFingerprint: root.Links.AppSSH.Meta.HostKeyFingerprint,
		OAuthClient:        root.Links.AppSSH.Meta.OAuthClient,
	}

	// When only the WebSocket listener is advertised, its meta block carries
	// the fingerprint and OAuth client.
	if endpoint.HostKeyFingerprint == "" {
		endpoint.HostKeyFingerprint = root.Links.AppSSHWS.Meta.HostKeyFingerprint
	}
	if endpoint.OAuthClient == "" {
		endpoint.OAuthClient = root.Links.AppSSHWS.Meta.OAuthClient
	}

	if endpoint.Address == "" && endpoint.WebSocketURL == "" {
		return SSHEndpoint{}, errors.New("this CF API advertises no SSH endpoint (neither app_ssh nor app_ssh_ws); SSH may be disabled on this foundation")
	}

	return endpoint, nil
}

// SSHPasscode obtains a one-time SSH authorization code from UAA.
//
// This is the exchange `cf ssh-code` performs: an authorization-code request
// against UAA for the SSH proxy's OAuth client, whose 302 Location carries the
// code. No CAPI endpoint is involved, so it is unaffected by the v2/v3 split.
func (c *Client) SSHPasscode(ctx context.Context) (string, error) {
	cf, err := c.client()
	if err != nil {
		return "", err
	}

	code, err := cf.SSHCode(ctx)
	if err != nil {
		return "", fmt.Errorf("could not obtain a one-time SSH passcode: %w", err)
	}
	return code, nil
}

// HTTPClient exposes the unauthenticated HTTP client so that callers can reuse
// its TLS configuration for non-CAPI connections.
func (c *Client) HTTPClient() *http.Client {
	cf, err := c.client()
	if err != nil {
		// Callers use this only for the unauthenticated root document, and a
		// failure there is reported on its own terms.
		return http.DefaultClient
	}
	return cf.Config.HTTPClient()
}

// TLSConfig returns the TLS configuration in force for this session, or nil if
// Go's defaults apply. Callers that dial outside net/http (the SSH tunnel) need
// this to honour --skip-ssl-validation.
func (c *Client) TLSConfig() *tls.Config {
	transport, ok := c.HTTPClient().Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil {
		return nil
	}
	return transport.TLSClientConfig.Clone()
}

// APIEndpoint returns the targeted CF API endpoint.
func (c *Client) APIEndpoint() string {
	return c.apiURL
}

func (c *Client) pollJob(ctx context.Context, jobGUID string) error {
	cf, err := c.client()
	if err != nil {
		return err
	}

	opts := client.NewPollingOptions()
	opts.Timeout = jobPollTimeout
	opts.CheckInterval = c.pollInterval

	if err := cf.Jobs.PollComplete(ctx, jobGUID, opts); err != nil {
		if errors.Is(err, client.ErrAsyncProcessTimeout) {
			return fmt.Errorf("timed out after %s waiting for the CF API job to finish", jobPollTimeout)
		}
		return err
	}
	return nil
}
