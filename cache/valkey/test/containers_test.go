package valkey_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

// valkeyImage is the server image, pinned so that a machine that has it
// never pulls another.
const valkeyImage = "valkey/valkey:8"

// requireDockerEnv, when set to any non-empty value, makes the container
// tests fail instead of skipping when Docker is unavailable.
const requireDockerEnv = "GAS_TEST_REQUIRE_DOCKER"

// The ACL users of the containers, with aclPassword. aclUser may run
// every command; limitedUser every command but GET; noClusterUser every
// command but CLUSTER.
const (
	aclUser       = "zqxuser"
	limitedUser   = "zqxlimited"
	noClusterUser = "zqxnocluster"
	aclPassword   = "zqxpass"
)

// server is the container the tests share, started on first use.
type server struct {
	// err is the error of starting it.
	err error
	// terminate stops it, or is nil.
	terminate func()
	// roots holds the certificate authority of its TLS certificate.
	roots *x509.CertPool
	// addr is its plain address, host:port.
	addr string
	// tlsAddr is its TLS address, host:port.
	tlsAddr string
	// once starts it once.
	once sync.Once
}

// shared is the server of the package, terminated by TestMain.
var shared server

// requireDocker skips the test when Docker is unavailable, or fails it
// when requireDockerEnv is set.
func requireDocker(t *testing.T) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping container test in short mode")
	}

	if os.Getenv(requireDockerEnv) == "" {
		testcontainers.SkipIfProviderIsNotHealthy(t)

		return
	}

	p, err := testcontainers.ProviderDocker.GetProvider()
	require.NoError(t, err, "%s is set but Docker is unavailable", requireDockerEnv)

	defer func() { require.NoError(t, p.Close()) }()

	require.NoError(t, p.Health(context.Background()),
		"%s is set but Docker is unavailable", requireDockerEnv)
}

// valkeyServer returns the shared server, which it starts on first use,
// skipping the test without Docker. The server listens in plain text and
// with TLS, has no password for the default user, and has the users
// aclUser, limitedUser and noClusterUser.
func valkeyServer(t *testing.T) *server {
	t.Helper()
	requireDocker(t)

	shared.once.Do(func() { shared.err = shared.start() })

	require.NoError(t, shared.err)

	return &shared
}

// start starts the container, with a fresh certificate authority and
// server certificate.
func (s *server) start() error {
	ctx := context.Background()

	caPEM, certPEM, keyPEM, roots, err := certificates()
	if err != nil {
		return err
	}

	ctr, err := testcontainers.Run(ctx, valkeyImage,
		testcontainers.WithExposedPorts("6379/tcp", "6380/tcp"),
		testcontainers.WithFiles(tlsFiles(caPEM, certPEM, keyPEM)...),
		testcontainers.WithCmd("valkey-server",
			"--port", "6379",
			"--tls-port", "6380",
			"--tls-cert-file", "/tls/server.crt",
			"--tls-key-file", "/tls/server.key",
			"--tls-ca-cert-file", "/tls/ca.crt",
			"--tls-auth-clients", "no",
			"--user", aclUser, "on", ">"+aclPassword, "~*", "&*", "+@all",
			"--user", limitedUser, "on", ">"+aclPassword, "~*", "&*", "+@all", "-get",
			"--user", noClusterUser, "on", ">"+aclPassword, "~*", "&*", "+@all", "-cluster",
		),
		testcontainers.WithWaitStrategyAndDeadline(time.Minute,
			wait.ForLog("Ready to accept connections"),
			wait.ForListeningPort("6379/tcp"),
			wait.ForListeningPort("6380/tcp"),
		),
	)
	if ctr != nil {
		s.terminate = func() { _ = ctr.Terminate(context.Background()) }
	}

	if err != nil {
		return fmt.Errorf("start valkey: %w", err)
	}

	s.addr, err = ctr.PortEndpoint(ctx, "6379/tcp", "")
	if err != nil {
		return fmt.Errorf("plain endpoint: %w", err)
	}

	s.tlsAddr, err = ctr.PortEndpoint(ctx, "6380/tcp", "")
	if err != nil {
		return fmt.Errorf("TLS endpoint: %w", err)
	}

	s.roots = roots

	return nil
}

// tlsFiles returns the files the server's TLS port reads: the
// authority, the certificate and the key, all in PEM.
func tlsFiles(caPEM, certPEM, keyPEM []byte) []testcontainers.ContainerFile {
	file := func(b []byte, path string) testcontainers.ContainerFile {
		return testcontainers.ContainerFile{
			Reader:            bytes.NewReader(b),
			ContainerFilePath: path,
			FileMode:          0o644,
		}
	}

	return []testcontainers.ContainerFile{
		file(caPEM, "/tls/ca.crt"),
		file(certPEM, "/tls/server.crt"),
		file(keyPEM, "/tls/server.key"),
	}
}

// certificates returns, in PEM, a new certificate authority, a server
// certificate it signed for `localhost`, 127.0.0.1 and
// `host.docker.internal`, the host the test container script points
// testcontainers at, and the server's key, with a pool holding the
// authority.
func certificates() (caPEM, certPEM, keyPEM []byte, roots *x509.CertPool, err error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("CA key: %w", err)
	}

	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "gas cache test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("CA certificate: %w", err)
	}

	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("CA parse: %w", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("server key: %w", err)
	}

	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost", "host.docker.internal"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("server certificate: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("server key encoding: %w", err)
	}

	roots = x509.NewCertPool()
	roots.AddCert(caCert)

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		roots, nil
}

// clusterNode is a node of the cluster container: the port it listens
// on, its cluster bus port, and the first and last slot it serves.
type clusterNode struct {
	port, bus, first, last string
}

// clusterNodes are the primary nodes of the cluster container, which
// split the slots between them.
var clusterNodes = []clusterNode{
	{port: "7000", bus: "17000", first: "0", last: "5460"},
	{port: "7001", bus: "17001", first: "5461", last: "10922"},
	{port: "7002", bus: "17002", first: "10923", last: "16383"},
}

// cluster is the Valkey cluster container the tests share, started on
// first use.
type cluster struct {
	// err is the error of starting it.
	err error
	// terminate stops it, or is nil.
	terminate func()
	// addr is the address of its first node, host:port.
	addr string
	// once starts it once.
	once sync.Once
}

// sharedCluster is the cluster of the package, terminated by TestMain.
var sharedCluster cluster

// valkeyCluster returns the shared cluster, which it starts on first use,
// skipping the test without Docker. It has three primary nodes and no
// replicas, no password for the default user, and the users aclUser and
// noClusterUser.
func valkeyCluster(t *testing.T) *cluster {
	t.Helper()
	requireDocker(t)

	sharedCluster.once.Do(func() { sharedCluster.err = sharedCluster.start() })

	require.NoError(t, sharedCluster.err)

	return &sharedCluster
}

// start starts the nodes in one container, where they reach each other on
// the loopback. Each node announces the address the tests reach it at,
// the container's host and the node's published port, so valkey-go
// connects to the nodes the cluster names. Docker picks the published
// ports, so they are known only once the container runs, and the nodes
// form the cluster after that.
func (c *cluster) start() error {
	ctx := context.Background()

	var script strings.Builder

	exposed := make([]string, 0, len(clusterNodes))
	waits := make([]wait.Strategy, 0, len(clusterNodes))

	for _, n := range clusterNodes {
		fmt.Fprintf(&script, "valkey-server --port %s --cluster-enabled yes"+
			" --cluster-config-file nodes-%s.conf --save '' --appendonly no"+
			" --user %s on '>%s' '~*' '&*' '+@all'"+
			" --user %s on '>%s' '~*' '&*' '+@all' -cluster --daemonize yes\n",
			n.port, n.port, aclUser, aclPassword, noClusterUser, aclPassword)

		exposed = append(exposed, n.port+"/tcp")
		waits = append(waits, wait.ForListeningPort(n.port+"/tcp"))
	}

	script.WriteString("exec sleep infinity\n")

	ctr, err := testcontainers.Run(ctx, valkeyImage,
		testcontainers.WithExposedPorts(exposed...),
		testcontainers.WithEntrypoint("sh", "-c", script.String()),
		testcontainers.WithWaitStrategyAndDeadline(time.Minute, waits...),
	)
	if ctr != nil {
		c.terminate = func() { _ = ctr.Terminate(context.Background()) }
	}

	if err != nil {
		return fmt.Errorf("start valkey cluster: %w", err)
	}

	host, err := c.form(ctx, ctr)
	if err != nil {
		return err
	}

	return awaitCluster(ctx, ctr, host)
}

// form tells each node of ctr the address it is reached at and the slots
// it serves, and has the first node meet the others. It returns the host
// the nodes are reached at.
func (c *cluster) form(ctx context.Context, ctr testcontainers.Container) (string, error) {
	var host string

	for i, n := range clusterNodes {
		endpoint, err := ctr.PortEndpoint(ctx, n.port+"/tcp", "")
		if err != nil {
			return "", fmt.Errorf("node endpoint: %w", err)
		}

		h, published, err := net.SplitHostPort(endpoint)
		if err != nil {
			return "", fmt.Errorf("node endpoint: %w", err)
		}

		if i == 0 {
			c.addr, host = endpoint, h
		}

		cmds := [][]string{
			{
				n.port, "config", "set",
				"cluster-announce-hostname", h,
				"cluster-preferred-endpoint-type", "hostname",
				"cluster-announce-port", published,
				"cluster-announce-bus-port", n.bus,
			},
			{n.port, "cluster", "addslotsrange", n.first, n.last},
		}

		// The first node meets each of the others.
		if i > 0 {
			cmds = append(cmds, []string{
				clusterNodes[0].port, "cluster", "meet", "127.0.0.1", n.port, n.bus,
			})
		}

		for _, cmd := range cmds {
			_, err = nodeCLI(ctx, ctr, cmd[0], cmd[1:]...)
			if err != nil {
				return "", err
			}
		}
	}

	return host, nil
}

// errNodeCLI is a valkey-cli run that failed or got an error reply.
var errNodeCLI = errors.New("valkey-cli failed")

// nodeCLI runs valkey-cli with args in ctr against the node on port and
// returns its output.
func nodeCLI(
	ctx context.Context,
	ctr testcontainers.Container,
	port string,
	args ...string,
) (string, error) {
	cmd := append([]string{"valkey-cli", "-p", port}, args...)

	code, out, err := ctr.Exec(ctx, cmd, tcexec.Multiplexed())
	if err != nil {
		return "", fmt.Errorf("valkey-cli %v: %w", args, err)
	}

	b, err := io.ReadAll(out)
	if err != nil {
		return "", fmt.Errorf("valkey-cli %v: %w", args, err)
	}

	if code != 0 || strings.HasPrefix(string(b), "ERR") {
		return "", fmt.Errorf("%w: %v: exit %d: %s", errNodeCLI, args, code, b)
	}

	return string(b), nil
}

// awaitCluster waits until every node of ctr reports the cluster ok and
// names every node by host in CLUSTER SHARDS, so the topology valkey-go
// reads from any node is the whole cluster.
func awaitCluster(ctx context.Context, ctr testcontainers.Container, host string) error {
	deadline := time.Now().Add(30 * time.Second)

	for {
		formed, err := clusterFormed(ctx, ctr, host)
		if err != nil || formed {
			return err
		}

		if time.Now().After(deadline) {
			return errClusterNotFormed
		}

		time.Sleep(100 * time.Millisecond)
	}
}

// clusterFormed reports whether every node of ctr reports the cluster ok
// and names every node by host.
func clusterFormed(ctx context.Context, ctr testcontainers.Container, host string) (bool, error) {
	for _, n := range clusterNodes {
		info, err := nodeCLI(ctx, ctr, n.port, "cluster", "info")
		if err != nil {
			return false, err
		}

		shards, err := nodeCLI(ctx, ctr, n.port, "cluster", "shards")
		if err != nil {
			return false, err
		}

		if !strings.Contains(info, "cluster_state:ok") ||
			endpoints(shards, host) != len(clusterNodes) {
			return false, nil
		}
	}

	return true, nil
}

// errClusterNotFormed is a cluster whose nodes did not agree in time.
var errClusterNotFormed = errors.New("valkey cluster did not form")

// endpoints counts the nodes of valkey-cli's CLUSTER SHARDS output whose
// endpoint is host.
func endpoints(shards, host string) int {
	lines := strings.Split(strings.ReplaceAll(shards, "\r", ""), "\n")
	n := 0

	for i := 0; i+1 < len(lines); i++ {
		if lines[i] == "endpoint" && lines[i+1] == host {
			n++
		}
	}

	return n
}
