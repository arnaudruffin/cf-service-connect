package launcher

import (
	"bufio"
	"crypto/ed25519"
	"crypto/sha1" //nolint:gosec // required by the RFC 6455 handshake
	"crypto/sha256"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/websocket"
)

const (
	testSSHUser     = "cf:11111111-2222-3333-4444-555555555555/0"
	testSSHPasscode = "one-time-code"
)

// testSSHServer is a minimal SSH server that accepts one password and serves
// direct-tcpip channel requests by echoing, which is enough to exercise both
// the handshake and the port-forwarding path.
type testSSHServer struct {
	config      *ssh.ServerConfig
	fingerprint string
}

func newTestSSHServer(t *testing.T) *testSSHServer {
	t.Helper()

	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = 0x42
	}
	signer, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(seed))
	require.NoError(t, err)

	config := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if meta.User() != testSSHUser {
				return nil, assert.AnError
			}
			if string(password) != testSSHPasscode {
				return nil, assert.AnError
			}
			return &ssh.Permissions{}, nil
		},
	}
	config.AddHostKey(signer)

	sum := sha256.Sum256(signer.PublicKey().Marshal())

	return &testSSHServer{
		config:      config,
		fingerprint: base64.RawStdEncoding.EncodeToString(sum[:]),
	}
}

// serve completes a server-side handshake on conn and discards channels.
func (s *testSSHServer) serve(conn net.Conn) {
	serverConn, channels, requests, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		_ = conn.Close()
		return
	}
	go ssh.DiscardRequests(requests)
	go func() {
		for newChannel := range channels {
			_ = newChannel.Reject(ssh.Prohibited, "not needed for this test")
		}
		_ = serverConn.Close()
	}()
}

// listenTCP starts the SSH server on a loopback TCP port.
func (s *testSSHServer) listenTCP(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			s.serve(conn)
		}
	}()

	return listener.Addr().String()
}

// listenWebSocket starts the SSH server behind a WebSocket handler, mirroring
// the ssh-proxy WebSocket listener described in RFC-0029.
func (s *testSSHServer) listenWebSocket(t *testing.T) string {
	t.Helper()

	handler := websocket.Handler(func(conn *websocket.Conn) {
		conn.PayloadType = websocket.BinaryFrame
		done := make(chan struct{})
		serverConn, channels, requests, err := ssh.NewServerConn(conn, s.config)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(requests)
		go func() {
			for newChannel := range channels {
				_ = newChannel.Reject(ssh.Prohibited, "not needed for this test")
			}
			close(done)
		}()
		// Block so the handler (and therefore the connection) stays open.
		_ = serverConn.Wait()
		<-done
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return "ws://" + server.Listener.Addr().String()
}

func TestDialSSHOverTCP(t *testing.T) {
	server := newTestSSHServer(t)
	address := server.listenTCP(t)

	client, err := dialSSH(SSHTarget{
		Address:            address,
		HostKeyFingerprint: server.fingerprint,
		User:               testSSHUser,
		Passcode:           testSSHPasscode,
	})

	require.NoError(t, err)
	require.NotNil(t, client)
	assert.NoError(t, client.Close())
}

func TestDialSSHPrefersWebSocketWhenBothAdvertised(t *testing.T) {
	server := newTestSSHServer(t)
	wsURL := server.listenWebSocket(t)

	// Point the TCP address at a closed port: if the WebSocket path were not
	// preferred, this test would fail rather than silently pass.
	closedListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	closedAddress := closedListener.Addr().String()
	require.NoError(t, closedListener.Close())

	client, err := dialSSH(SSHTarget{
		Address:            closedAddress,
		WebSocketURL:       wsURL,
		HostKeyFingerprint: server.fingerprint,
		User:               testSSHUser,
		Passcode:           testSSHPasscode,
	})

	require.NoError(t, err)
	require.NotNil(t, client)
	assert.NoError(t, client.Close())
}

func TestDialSSHFallsBackToTCPWhenWebSocketFails(t *testing.T) {
	server := newTestSSHServer(t)
	address := server.listenTCP(t)

	// A WebSocket URL that will refuse the connection.
	deadListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	deadURL := "ws://" + deadListener.Addr().String()
	require.NoError(t, deadListener.Close())

	client, err := dialSSH(SSHTarget{
		Address:            address,
		WebSocketURL:       deadURL,
		HostKeyFingerprint: server.fingerprint,
		User:               testSSHUser,
		Passcode:           testSSHPasscode,
	})

	require.NoError(t, err)
	require.NotNil(t, client)
	assert.NoError(t, client.Close())
}

// When only the WebSocket endpoint is advertised there is nothing to fall back
// to, so the failure must be reported rather than masked.
func TestDialSSHFailsWhenOnlyWebSocketAdvertisedAndUnreachable(t *testing.T) {
	deadListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	deadURL := "ws://" + deadListener.Addr().String()
	require.NoError(t, deadListener.Close())

	_, err = dialSSH(SSHTarget{
		WebSocketURL:       deadURL,
		HostKeyFingerprint: "irrelevant",
		User:               testSSHUser,
		Passcode:           testSSHPasscode,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not connect to the SSH proxy")
}

func TestDialSSHRejectsWrongHostKey(t *testing.T) {
	server := newTestSSHServer(t)
	address := server.listenTCP(t)

	sum := sha256.Sum256([]byte("some other key"))
	wrongFingerprint := base64.RawStdEncoding.EncodeToString(sum[:])

	_, err := dialSSH(SSHTarget{
		Address:            address,
		HostKeyFingerprint: wrongFingerprint,
		User:               testSSHUser,
		Passcode:           testSSHPasscode,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "host key verification failed")
}

func TestDialSSHRejectsBadPasscode(t *testing.T) {
	server := newTestSSHServer(t)
	address := server.listenTCP(t)

	_, err := dialSSH(SSHTarget{
		Address:            address,
		HostKeyFingerprint: server.fingerprint,
		User:               testSSHUser,
		Passcode:           "wrong",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unable to authenticate")
}

func TestDialSSHFailsWithNoEndpoint(t *testing.T) {
	_, err := dialSSH(SSHTarget{
		HostKeyFingerprint: "irrelevant",
		User:               testSSHUser,
		Passcode:           testSSHPasscode,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no usable SSH endpoint")
}

func TestHostForSSHConn(t *testing.T) {
	tests := map[string]struct {
		target   SSHTarget
		expected string
	}{
		"prefers the TCP address": {
			target:   SSHTarget{Address: "ssh.example.com:2222", WebSocketURL: "wss://ssh.example.com"},
			expected: "ssh.example.com:2222",
		},
		"defaults the WebSocket port to 443": {
			target:   SSHTarget{WebSocketURL: "wss://ssh.example.com"},
			expected: "ssh.example.com:443",
		},
		"honours an explicit WebSocket port": {
			target:   SSHTarget{WebSocketURL: "wss://ssh.example.com:9443"},
			expected: "ssh.example.com:9443",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, test.expected, hostForSSHConn(test.target))
		})
	}
}

// Guard the binary-frame requirement at the wire level: golang.org/x/net/websocket
// defaults to text frames, and a text frame would silently corrupt the SSH byte
// stream. This performs a raw RFC 6455 server handshake and inspects the opcode
// of the client's first data frame, rather than trusting a library field.
func TestWebSocketDialUsesBinaryFrames(t *testing.T) {
	opcodes := make(chan byte, 1)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		reader := bufio.NewReader(conn)
		request, err := http.ReadRequest(reader)
		if err != nil {
			return
		}

		accept := sha1.Sum([]byte(request.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11")) //nolint:gosec // required by RFC 6455
		_, err = conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(accept[:]) + "\r\n\r\n"))
		if err != nil {
			return
		}

		// First byte of a frame: FIN flag plus a 4-bit opcode.
		header, err := reader.ReadByte()
		if err != nil {
			return
		}
		opcodes <- header & 0x0f
	}()

	target := SSHTarget{
		WebSocketURL:       "ws://" + listener.Addr().String(),
		HostKeyFingerprint: "irrelevant",
		User:               testSSHUser,
		Passcode:           testSSHPasscode,
	}

	// The handshake cannot complete (the peer is not an SSH server), but the
	// client sends its version banner first, which is all this asserts on.
	_, _ = dialSSHOverWebSocket(target, &ssh.ClientConfig{
		User:            target.User,
		Auth:            []ssh.AuthMethod{ssh.Password(target.Passcode)},
		HostKeyCallback: hostKeyCallback(target.HostKeyFingerprint),
	})

	select {
	case opcode := <-opcodes:
		assert.Equal(t, byte(0x2), opcode,
			"SSH must be tunnelled in binary WebSocket frames (opcode 0x2); text frames (0x1) corrupt the stream")
	case <-time.After(10 * time.Second):
		t.Fatal("the WebSocket server received no data frame from the client")
	}
}
