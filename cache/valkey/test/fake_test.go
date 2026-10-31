package valkey_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeHello is the RESP3 reply of the fake server to HELLO.
const fakeHello = "%3\r\n$6\r\nserver\r\n$6\r\nvalkey\r\n$7\r\nversion\r\n$5\r\n8.0.0\r\n" +
	"$5\r\nproto\r\n:3\r\n"

// fakeServer returns the address of a loopback server that speaks just
// enough RESP3 for the store: it answers the handshake and PING, and
// every GET with getReply, raw RESP. It is closed with its connections
// when the test ends.
func fakeServer(t *testing.T, getReply string) string {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	var (
		mu    sync.Mutex
		conns []net.Conn
		wg    sync.WaitGroup
	)

	wg.Go(func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}

			mu.Lock()

			conns = append(conns, c)
			mu.Unlock()

			wg.Go(func() { fakeServe(c, getReply) })
		}
	})

	t.Cleanup(func() {
		_ = l.Close()

		mu.Lock()

		for _, c := range conns {
			_ = c.Close()
		}

		mu.Unlock()
		wg.Wait()
	})

	return l.Addr().String()
}

// fakeServe answers the commands of one connection until it closes.
func fakeServe(c net.Conn, getReply string) {
	r := bufio.NewReader(c)

	for {
		cmd, err := readCommand(r)
		if err != nil {
			return
		}

		var reply string

		switch strings.ToUpper(cmd[0]) {
		case "HELLO":
			reply = fakeHello
		case "PING":
			reply = "+PONG\r\n"
		case "GET":
			reply = getReply
		default:
			reply = "+OK\r\n"
		}

		_, err = io.WriteString(c, reply)
		if err != nil {
			return
		}
	}
}

// errBadCommand is a command that is not a RESP array of bulk strings.
var errBadCommand = errors.New("bad command")

// readCommand reads one command, a RESP array of bulk strings.
func readCommand(r *bufio.Reader) ([]string, error) {
	n, err := readLength(r, '*')
	if err != nil {
		return nil, err
	}

	out := make([]string, n)

	for i := range out {
		size, err := readLength(r, '$')
		if err != nil {
			return nil, err
		}

		buf := make([]byte, size+2)

		_, err = io.ReadFull(r, buf)
		if err != nil {
			return nil, err
		}

		out[i] = string(buf[:size])
	}

	if len(out) == 0 {
		return nil, errBadCommand
	}

	return out, nil
}

// readLength reads a line of kind, `*` or `$`, and returns its length.
func readLength(r *bufio.Reader, kind byte) (int, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return 0, err
	}

	line = strings.TrimSuffix(line, "\r\n")
	if line == "" || line[0] != kind {
		return 0, errBadCommand
	}

	n, err := strconv.Atoi(line[1:])
	if err != nil || n < 0 {
		return 0, errBadCommand
	}

	return n, nil
}
