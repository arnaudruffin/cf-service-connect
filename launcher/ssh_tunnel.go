package launcher

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/cloud-gov/cf-service-connect/logger"
	"github.com/cloud-gov/cf-service-connect/models"
)

// keepAliveInterval matches the CF CLI's SSH keep-alive cadence. Without it a
// tunnel left idle while a user reads a query result can be dropped by
// intermediate load balancers.
const keepAliveInterval = 30 * time.Second

// forwardDrainTimeout bounds how long Close waits for in-flight forwarded
// connections to finish before dropping the SSH connection.
const forwardDrainTimeout = 2 * time.Second

// SSHTunnel is a local port forward to a service instance, carried over an SSH
// connection to an app container. Create one with NewSSHTunnel.
//
// The tunnel is established in-process over golang.org/x/crypto/ssh rather than
// by shelling out to `cf ssh`. Beyond removing a runtime dependency on the cf
// binary, this is what makes the plugin work on foundations with CAPI v2
// disabled: `cf ssh` in CLI v6/v7 resolves its endpoint from /v2/info, and a
// plugin cannot force the CLI to use its v3 code path.
type SSHTunnel struct {
	// LocalPort is the loopback port that forwards to the service instance.
	LocalPort int

	remoteAddress string
	target        SSHTarget

	client   *ssh.Client
	listener net.Listener

	// closeOnce guards against a double Close: connector.Connect defers Close
	// and manualConnect may also return after Wait observes a failure.
	closeOnce sync.Once

	// done is closed when the tunnel stops serving, for whatever reason.
	done chan struct{}

	// waitErr reports why the tunnel stopped. Guarded by waitErrMu.
	waitErrMu sync.Mutex
	waitErr   error

	// forwarding tracks in-flight forwarded connections so Close can wait for
	// them, avoiding "use of closed network connection" noise on shutdown.
	forwarding sync.WaitGroup
}

// NewSSHTunnel prepares (but does not open) a tunnel from a local port to the
// host and port in creds, via the app described by target.
//
// A pointer is returned because SSHTunnel carries a mutex and wait groups, which
// must not be copied.
func NewSSHTunnel(creds models.Credentials, target SSHTarget) *SSHTunnel {
	return &SSHTunnel{
		LocalPort:     getAvailablePort(),
		remoteAddress: net.JoinHostPort(creds.GetHost(), creds.GetPort()),
		target:        target,
		done:          make(chan struct{}),
	}
}

// Open connects to the SSH proxy and starts listening on the local port.
//
// Unlike the previous implementation, this does not sleep and hope: it returns
// only once the SSH handshake has succeeded and the local listener is bound, so
// a failure is reported immediately and with its real cause.
func (t *SSHTunnel) Open() error {
	client, err := dialSSH(t.target)
	if err != nil {
		return err
	}
	t.client = client

	// Bind explicitly to loopback. The forwarded port carries service
	// credentials, so it must not be reachable from off-host.
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", t.LocalPort))
	if err != nil {
		_ = t.client.Close()
		t.client = nil
		return fmt.Errorf("could not listen on local port %d: %w", t.LocalPort, err)
	}
	t.listener = listener

	go t.acceptLoop()
	go t.keepAlive()
	go t.watchConnection()

	fmt.Printf("SSH tunnel created: localhost:%d -> %s\n", t.LocalPort, t.remoteAddress)
	return nil
}

// Wait blocks until the tunnel stops, returning the reason if it failed.
func (t *SSHTunnel) Wait() error {
	<-t.done
	t.waitErrMu.Lock()
	defer t.waitErrMu.Unlock()
	return t.waitErr
}

// Close tears down the local listener and the SSH connection. It is safe to
// call more than once.
func (t *SSHTunnel) Close() error {
	var err error
	t.closeOnce.Do(func() {
		t.stop(nil)

		if t.listener != nil {
			if closeErr := t.listener.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
				err = closeErr
			}
		}

		// Give in-flight forwarded connections a brief chance to drain so that
		// a client mid-write is not truncated. This is deliberately bounded:
		// waiting unconditionally would hang on exit whenever a peer holds a
		// connection open (a database client that never sends EOF, for
		// instance), and closing the SSH connection below terminates the
		// stragglers anyway.
		t.waitForForwardsToDrain(forwardDrainTimeout)

		if t.client != nil {
			if closeErr := t.client.Close(); closeErr != nil && !errors.Is(closeErr, io.EOF) && err == nil {
				err = closeErr
			}
		}
	})
	return err
}

// waitForForwardsToDrain waits up to timeout for forwarded connections to
// finish, returning early once they all have.
func (t *SSHTunnel) waitForForwardsToDrain(timeout time.Duration) {
	drained := make(chan struct{})
	go func() {
		t.forwarding.Wait()
		close(drained)
	}()

	select {
	case <-drained:
	case <-time.After(timeout):
		logger.Debugf("forwarded connections still open after %s; closing anyway\n", timeout)
	}
}

// stop records why the tunnel is stopping and unblocks Wait exactly once.
func (t *SSHTunnel) stop(err error) {
	t.waitErrMu.Lock()
	select {
	case <-t.done:
		// Already stopped; keep the first cause.
	default:
		t.waitErr = err
		close(t.done)
	}
	t.waitErrMu.Unlock()
}

func (t *SSHTunnel) acceptLoop() {
	for {
		conn, err := t.listener.Accept()
		if err != nil {
			select {
			case <-t.done:
				// Expected: we are shutting down.
			default:
				t.stop(fmt.Errorf("stopped accepting local connections: %w", err))
			}
			return
		}

		t.forwarding.Add(1)
		go func() {
			defer t.forwarding.Done()
			t.forward(conn)
		}()
	}
}

// forward pipes one local connection to the remote address over SSH.
func (t *SSHTunnel) forward(local net.Conn) {
	defer func() {
		_ = local.Close()
	}()

	remote, err := t.client.Dial("tcp", t.remoteAddress)
	if err != nil {
		// Report on stderr but keep the tunnel up: one refused connection
		// (e.g. a client that connects before the DB is reachable) should not
		// tear down the session.
		fmt.Fprintf(os.Stderr, "Could not reach %s through the SSH tunnel: %v\n", t.remoteAddress, err)
		return
	}
	defer func() {
		_ = remote.Close()
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go copyStream(&wg, remote, local)
	go copyStream(&wg, local, remote)
	wg.Wait()
}

func copyStream(wg *sync.WaitGroup, dst io.Writer, src io.Reader) {
	defer wg.Done()
	if _, err := io.Copy(dst, src); err != nil {
		logger.Debugf("tunnel copy ended: %v\n", err)
	}
	// Half-close so the peer sees EOF rather than waiting for the whole tunnel.
	if closer, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()
	}
}

// keepAlive sends periodic requests so idle tunnels are not reaped.
func (t *SSHTunnel) keepAlive() {
	ticker := time.NewTicker(keepAliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-t.done:
			return
		case <-ticker.C:
			if _, _, err := t.client.SendRequest("keepalive@cloudfoundry.org", true, nil); err != nil {
				logger.Debugf("keep-alive failed: %v\n", err)
				return
			}
		}
	}
}

// watchConnection reports an SSH connection that drops on its own, so that a
// user waiting at a shell prompt learns the tunnel died rather than seeing
// silent connection refusals.
func (t *SSHTunnel) watchConnection() {
	err := t.client.Wait()
	select {
	case <-t.done:
		// Already shutting down.
	default:
		if err == nil || errors.Is(err, io.EOF) {
			t.stop(errors.New("the SSH connection to the app container closed unexpectedly"))
			return
		}
		t.stop(fmt.Errorf("the SSH connection to the app container failed: %w", err))
	}
}
