package launcher

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/net/websocket"

	"github.com/cloud-gov/cf-service-connect/logger"
)

// sshDialTimeout bounds the TCP/TLS connect and SSH handshake.
const sshDialTimeout = 30 * time.Second

// SSHTarget describes how to reach the Diego SSH proxy and authenticate to it.
type SSHTarget struct {
	// Address is the "host:port" of the legacy TCP SSH listener (the root
	// document's app_ssh link). May be empty if the operator has closed it.
	Address string

	// WebSocketURL is the wss:// URL of the SSH proxy's WebSocket listener (the
	// root document's app_ssh_ws link). May be empty if not offered.
	WebSocketURL string

	// HostKeyFingerprint pins the SSH proxy's host key.
	HostKeyFingerprint string

	// User is the SSH username, which for CF is "cf:<app-guid>/<index>".
	User string

	// Passcode is the one-time SSH authorization code from UAA.
	Passcode string

	// TLSConfig is applied to wss:// connections. nil means Go's defaults.
	TLSConfig *tls.Config
}

// dialSSH establishes an SSH connection to the CF SSH proxy.
//
// RFC-0029 ("CF SSH over WebSockets") states that a client MUST prefer the
// app_ssh_ws link over app_ssh when present, and MAY fall back to app_ssh if
// the WebSocket connection fails and both are advertised. That is what this
// does, so the plugin keeps working on foundations that have closed port 2222.
func dialSSH(target SSHTarget) (*ssh.Client, error) {
	clientConfig := &ssh.ClientConfig{
		User:            target.User,
		Auth:            []ssh.AuthMethod{ssh.Password(target.Passcode)},
		HostKeyCallback: hostKeyCallback(target.HostKeyFingerprint),
		Timeout:         sshDialTimeout,
	}

	if target.WebSocketURL != "" {
		client, err := dialSSHOverWebSocket(target, clientConfig)
		if err == nil {
			logger.Debugf("SSH connected over WebSocket to %s\n", target.WebSocketURL)
			return client, nil
		}

		if target.Address == "" {
			return nil, fmt.Errorf("could not connect to the SSH proxy at %s: %w", target.WebSocketURL, err)
		}

		// Both are advertised, so fall back rather than fail. Report the
		// WebSocket failure: if the fallback also fails, the user needs both
		// causes to diagnose it, and if the fallback succeeds this explains
		// why the connection took longer than expected.
		fmt.Printf("Could not connect to the SSH proxy over WebSocket (%v); falling back to %s.\n", err, target.Address)
	}

	if target.Address == "" {
		return nil, fmt.Errorf("the CF API advertises no usable SSH endpoint")
	}

	client, err := dialSSHOverTCP(target, clientConfig)
	if err != nil {
		return nil, fmt.Errorf("could not connect to the SSH proxy at %s: %w", target.Address, err)
	}
	logger.Debugf("SSH connected over TCP to %s\n", target.Address)
	return client, nil
}

func dialSSHOverTCP(target SSHTarget, clientConfig *ssh.ClientConfig) (*ssh.Client, error) {
	return ssh.Dial("tcp", target.Address, clientConfig)
}

// dialSSHOverWebSocket tunnels the SSH protocol inside a WebSocket connection,
// per RFC-0029.
func dialSSHOverWebSocket(target SSHTarget, clientConfig *ssh.ClientConfig) (*ssh.Client, error) {
	wsConfig, err := websocket.NewConfig(target.WebSocketURL, target.WebSocketURL)
	if err != nil {
		return nil, fmt.Errorf("invalid SSH WebSocket URL %q: %w", target.WebSocketURL, err)
	}
	wsConfig.TlsConfig = target.TLSConfig
	wsConfig.Dialer = &net.Dialer{Timeout: sshDialTimeout}
	wsConfig.Header = http.Header{}
	wsConfig.Header.Set("User-Agent", "cf-service-connect")

	wsConn, err := websocket.DialConfig(wsConfig)
	if err != nil {
		return nil, err
	}

	// SSH is a binary protocol. websocket.Conn defaults to text frames, which
	// would corrupt the stream, so switch to binary frames explicitly.
	wsConn.PayloadType = websocket.BinaryFrame

	// Set a deadline for the handshake only; it is cleared once the SSH
	// connection is up so that a long-lived idle tunnel is not torn down.
	if err := wsConn.SetDeadline(time.Now().Add(sshDialTimeout)); err != nil {
		_ = wsConn.Close()
		return nil, fmt.Errorf("could not set a handshake deadline on the WebSocket connection: %w", err)
	}

	address := hostForSSHConn(target)
	sshConn, channels, requests, err := ssh.NewClientConn(wsConn, address, clientConfig)
	if err != nil {
		_ = wsConn.Close()
		return nil, err
	}

	if err := wsConn.SetDeadline(time.Time{}); err != nil {
		_ = sshConn.Close()
		return nil, fmt.Errorf("could not clear the handshake deadline on the WebSocket connection: %w", err)
	}

	return ssh.NewClient(sshConn, channels, requests), nil
}

// hostForSSHConn produces the "host:port" string that ssh.NewClientConn reports
// in errors and passes to the host key callback.
func hostForSSHConn(target SSHTarget) string {
	if target.Address != "" {
		return target.Address
	}
	parsed, err := url.Parse(target.WebSocketURL)
	if err != nil {
		return target.WebSocketURL
	}
	if parsed.Port() != "" {
		return parsed.Host
	}
	return net.JoinHostPort(parsed.Hostname(), "443")
}
