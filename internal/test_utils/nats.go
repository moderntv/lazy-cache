package test_utils

import (
	"sync"
	"testing"

	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
)

var (
	natsMu            sync.Mutex
	natsConnectionMap map[*testing.T]*nats.Conn
)

func newNatsServerConnection(t *testing.T) *nats.Conn {
	natsOptions := []nats.Option{
		nats.NoEcho(),
	}

	s := natstest.RunRandClientPortServer()
	nc, err := nats.Connect(s.ClientURL(), natsOptions...)
	assert.NoError(t, err)
	_ = s

	t.Cleanup(func() {
		nc.Close()
		s.Shutdown()
		natsMu.Lock()
		delete(natsConnectionMap, t)
		natsMu.Unlock()
	})
	return nc
}

func NatsConnection(t *testing.T) *nats.Conn {
	natsMu.Lock()
	defer natsMu.Unlock()

	if natsConnectionMap == nil {
		natsConnectionMap = make(map[*testing.T]*nats.Conn)
	}

	// natsConnectionMap is a map is handling the connection to the nats server for each test - this is to avoid creating a new connection for mulltiple repositories in one test
	connection, exists := natsConnectionMap[t]

	if !exists {
		connection = newNatsServerConnection(t)
		natsConnectionMap[t] = connection
	}

	return connection
}
