package connector

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/template"
	"time"

	"code.cloudfoundry.org/cli/plugin"

	"github.com/cloud-gov/cf-service-connect/api"
	"github.com/cloud-gov/cf-service-connect/launcher"
	"github.com/cloud-gov/cf-service-connect/models"
	"github.com/cloud-gov/cf-service-connect/service"
)

// Options are the structured representation of the command-line
// flags/arguments.
type Options struct {
	App                 ResourceReference
	Service             ResourceReference
	AppName             string
	ServiceInstanceName string
	ConnectClient       bool
	KeepServiceKey      bool
}

// ResourceReference identifies an app or service, optionally outside the
// currently targeted organization and space.
type ResourceReference struct {
	Organization string
	Space        string
	Name         string
}

// ParseResourceReference parses name, space/name, or org/space/name.
func ParseResourceReference(value string) (ResourceReference, error) {
	parts := strings.SplitN(value, "/", 3)
	for _, part := range parts {
		if part == "" {
			return ResourceReference{}, fmt.Errorf("invalid resource reference %q: organization, space, and name must not be empty", value)
		}
	}

	switch len(parts) {
	case 1:
		return ResourceReference{Name: parts[0]}, nil
	case 2:
		return ResourceReference{Space: parts[0], Name: parts[1]}, nil
	default:
		return ResourceReference{Organization: parts[0], Space: parts[1], Name: parts[2]}, nil
	}
}

// startedAppState is the CF app state that indicates the operator wants the app
// running. It is a desired state, not a guarantee that an instance exists.
const startedAppState = "STARTED"

// The plugin tunnels through the app's first web instance, matching `cf ssh`'s
// default. These are named constants rather than literals so the intent is
// visible at the call site.
const (
	sshProcessType               = "web"
	sshInstanceIndex             = 0
	serviceKeyCredentialsTimeout = 5 * time.Minute
	serviceKeyCredentialsRequest = 10 * time.Second
	serviceKeyCredentialsPoll    = 2 * time.Second
)

const manualConnectInstructions = `Skipping call to client CLI. Connection information:

Host: localhost
Port: {{.Port}}
Username: {{.User}}
Password: {{.Pass}}
Name: {{.Name}}

Leave this terminal open while you want to use the SSH tunnel. Press Control-C to stop.
`

type localConnectionData struct {
	Port int
	User string
	Pass string
	Name string
}

func manualConnect(tunnel *launcher.SSHTunnel, creds models.Credentials) error {
	connectionData := localConnectionData{
		Port: tunnel.LocalPort,
		User: creds.GetUsername(),
		Pass: creds.GetPassword(),
		Name: creds.GetDBName(),
	}

	tmpl, err := template.New("").Parse(manualConnectInstructions)
	if err != nil {
		return err
	}
	if err := tmpl.Execute(os.Stdout, connectionData); err != nil {
		return err
	}

	// Wait for either a Control-C or the tunnel failing on its own. Previously
	// only the tunnel was watched, so a tunnel that died left the user staring
	// at instructions for a connection that no longer worked.
	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupted)

	tunnelClosed := make(chan error, 1)
	go func() {
		tunnelClosed <- tunnel.Wait()
	}()

	select {
	case <-interrupted:
		fmt.Println("\nClosing the SSH tunnel.")
		return nil
	case err := <-tunnelClosed:
		return err
	}
}

func handleClient(
	options Options,
	tunnel *launcher.SSHTunnel,
	si models.ServiceInstance,
	creds models.Credentials,
) error {
	if options.ConnectClient {
		srv, found := service.GetService(si)
		if found {
			fmt.Println("Connecting client...")
			return srv.Launch(tunnel.LocalPort, creds)
		}

		fmt.Printf("Unable to find matching client for service '%s' with plan '%s'. Falling back to `-no-client` behavior.\n", si.Service, si.Plan)
	}

	return manualConnect(tunnel, creds)
}

func selectCredentialHost(creds models.Credentials, in io.Reader, out io.Writer) (models.Credentials, error) {
	hosts := creds.GetHosts()
	if len(hosts) <= 1 {
		return creds, nil
	}

	fmt.Fprintln(out, "Multiple remote hosts found:")
	for index, host := range hosts {
		fmt.Fprintf(out, "  %d) %s\n", index+1, host)
	}
	fmt.Fprintf(out, "Choose host [1-%d] (default 1): ", len(hosts))

	choice, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("could not read host selection: %w", err)
	}

	choice = strings.TrimSpace(choice)
	if choice == "" {
		return models.CredentialsWithHost(creds, hosts[0]), nil
	}

	selected, err := strconv.Atoi(choice)
	if err != nil || selected < 1 || selected > len(hosts) {
		return nil, fmt.Errorf("invalid host selection %q: choose a number between 1 and %d", choice, len(hosts))
	}

	return models.CredentialsWithHost(creds, hosts[selected-1]), nil
}

// Connect performs the primary action of the plugin: providing an SSH tunnel
// and launching the appropriate client, if desired.
func Connect(cliConnection plugin.CliConnection, options Options) error {
	return connect(context.Background(), api.NewConnection(cliConnection), options)
}

func connect(ctx context.Context, conn api.Connection, options Options) (err error) {
	launcher.WarnIfCFBinaryNameSet()

	client, err := api.NewClient(conn)
	if err != nil {
		return err
	}

	appReference := options.App
	if appReference.Name == "" {
		appReference.Name = options.AppName
	}
	serviceReference := options.Service
	if serviceReference.Name == "" {
		serviceReference.Name = options.ServiceInstanceName
	}

	serviceSpaceGUID, err := client.ResolveSpaceGUID(
		ctx,
		serviceReference.Organization,
		serviceReference.Space,
	)
	if err != nil {
		return fmt.Errorf("could not resolve service location: %w", err)
	}
	appSpaceGUID, err := client.ResolveSpaceGUID(
		ctx,
		appReference.Organization,
		appReference.Space,
	)
	if err != nil {
		return fmt.Errorf("could not resolve app location: %w", err)
	}

	fmt.Println("Finding the service instance details...")
	instance, err := client.GetServiceInstanceInSpace(ctx, serviceReference.Name, serviceSpaceGUID)
	if err != nil {
		return err
	}
	serviceInstance := models.ServiceInstance{
		GUID:    instance.GUID,
		Name:    instance.Name,
		Service: instance.Offering,
		Plan:    instance.Plan,
	}

	app, err := client.GetAppInSpace(ctx, appReference.Name, appSpaceGUID)
	if err != nil {
		return err
	}
	if app.State != startedAppState {
		return fmt.Errorf("app %q is not started (state: %s); an SSH tunnel needs a running app instance.\nStart it with: cf start %s",
			app.Name, app.State, app.Name)
	}

	// Resolve everything the SSH session needs *before* creating a service key.
	// All of these can fail for reasons the user must fix (SSH disabled, no
	// running instance, no SSH endpoint), and there is no point provisioning
	// credentials we are then going to throw away.
	target, err := sshTarget(ctx, client, app)
	if err != nil {
		return err
	}

	serviceKey := models.NewServiceKey(serviceInstance)

	serviceKey.GUID, err = prepareServiceKey(ctx, client, serviceKey, options.KeepServiceKey)
	if err != nil {
		return err
	}
	if !options.KeepServiceKey {
		defer func() {
			fmt.Println("Deleting the service key...")
			if deleteErr := client.DeleteServiceKey(ctx, serviceKey.GUID); deleteErr != nil {
				// Report but do not mask the primary error: a leaked key is worth
				// telling the user about, since they may need to remove it by hand
				// before the next run.
				fmt.Fprintf(os.Stderr,
					"Warning: could not delete the temporary service key %q: %v\nRemove it with: cf delete-service-key %s %s\n",
					serviceKey.Name, deleteErr, serviceKey.Instance.Name, serviceKey.Name)
				if err == nil {
					err = deleteErr
				}
			}
		}()
	}

	fmt.Println("Waiting for the service key credentials...")
	creds, err := waitForServiceKeyCredentials(
		ctx,
		client,
		serviceKey.GUID,
		serviceKeyCredentialsTimeout,
		serviceKeyCredentialsRequest,
		serviceKeyCredentialsPoll,
	)
	if err != nil {
		return err
	}

	creds, err = selectCredentialHost(creds, os.Stdin, os.Stdout)
	if err != nil {
		return err
	}

	fmt.Println("Setting up SSH tunnel...")
	tunnel := launcher.NewSSHTunnel(creds, target)
	if err := tunnel.Open(); err != nil {
		return err
	}
	defer func() {
		if closeErr := tunnel.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	return handleClient(options, tunnel, serviceInstance, creds)
}

func prepareServiceKey(
	ctx context.Context,
	client *api.Client,
	serviceKey models.ServiceKey,
	keep bool,
) (string, error) {
	if keep {
		guid, found, err := client.FindServiceKey(ctx, serviceKey.Instance.GUID, serviceKey.Name)
		if err != nil {
			return "", err
		}
		if found {
			fmt.Printf("Reusing the service key %q...\n", serviceKey.Name)
			return guid, nil
		}
	} else if err := deleteExistingServiceKey(ctx, client, serviceKey); err != nil {
		return "", err
	}

	fmt.Println("Creating the service key...")
	return client.CreateServiceKey(ctx, serviceKey.Instance.GUID, serviceKey.Name)
}

func waitForServiceKeyCredentials(
	ctx context.Context,
	client *api.Client,
	keyGUID string,
	timeout time.Duration,
	requestTimeout time.Duration,
	pollInterval time.Duration,
) (models.Credentials, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var credentialsErr error
	for {
		requestCtx, cancelRequest := context.WithTimeout(ctx, requestTimeout)
		rawCredentials, err := client.GetServiceKeyCredentials(requestCtx, keyGUID)
		requestErr := requestCtx.Err()
		cancelRequest()

		if err != nil {
			if ctx.Err() != nil {
				return nil, serviceKeyCredentialsContextError(ctx, timeout, err)
			}
			if errors.Is(requestErr, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
				credentialsErr = err
			} else if errors.Is(err, api.ErrServiceKeyCredentialsNotReady) {
				credentialsErr = err
			} else {
				return nil, err
			}
		} else {
			credentials, err := models.CredentialsFromMap(rawCredentials)
			if err == nil {
				return credentials, nil
			}
			if !errors.Is(err, models.ErrIncompleteCredentials) {
				return nil, err
			}
			credentialsErr = err
		}

		select {
		case <-ctx.Done():
			return nil, serviceKeyCredentialsContextError(ctx, timeout, credentialsErr)
		case <-time.After(pollInterval):
		}
	}
}

func serviceKeyCredentialsContextError(
	ctx context.Context,
	timeout time.Duration,
	credentialsErr error,
) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf(
			"timed out after %s waiting for service key credentials: %w",
			timeout,
			credentialsErr,
		)
	}
	return ctx.Err()
}

func deleteExistingServiceKey(ctx context.Context, client *api.Client, serviceKey models.ServiceKey) error {
	guid, found, err := client.FindServiceKey(ctx, serviceKey.Instance.GUID, serviceKey.Name)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}

	fmt.Printf("Removing the service key %q left over from a previous run...\n", serviceKey.Name)
	if err := client.DeleteServiceKey(ctx, guid); err != nil {
		return fmt.Errorf("could not remove the existing service key %q: %w", serviceKey.Name, err)
	}
	return nil
}

// sshTarget assembles everything needed to open an SSH session to the app: the
// proxy endpoint from the CF API root document, the process instance to target,
// and a one-time passcode from UAA.
func sshTarget(ctx context.Context, client *api.Client, app api.App) (launcher.SSHTarget, error) {
	endpoint, err := client.GetSSHEndpoint(ctx)
	if err != nil {
		return launcher.SSHTarget{}, err
	}

	if err := client.CheckSSHEnabled(ctx, app); err != nil {
		return launcher.SSHTarget{}, err
	}

	// The app being STARTED does not mean an instance is running, and the SSH
	// username needs the process GUID rather than the app GUID.
	process, err := client.GetSSHProcess(ctx, app, sshProcessType, sshInstanceIndex)
	if err != nil {
		return launcher.SSHTarget{}, err
	}

	passcode, err := client.SSHPasscode(ctx)
	if err != nil {
		return launcher.SSHTarget{}, err
	}
	if passcode == "" {
		return launcher.SSHTarget{}, errors.New("UAA returned an empty SSH passcode")
	}

	return launcher.SSHTarget{
		Address:            endpoint.Address,
		WebSocketURL:       endpoint.WebSocketURL,
		HostKeyFingerprint: endpoint.HostKeyFingerprint,
		User:               process.SSHUsername(),
		Passcode:           passcode,
		TLSConfig:          client.TLSConfig(),
	}, nil
}
