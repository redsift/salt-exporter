package listener

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/kpetremann/salt-exporter/pkg/event"
	"github.com/rs/zerolog/log"
	"github.com/vmihailenco/msgpack/v5"
)

type eventParser interface {
	Parse(message map[string]interface{}) (event.SaltEvent, error)
}

const DefaultIPCFilepath = "unix:///var/run/salt/master/master_event_pub.ipc"

// EventListener listens to the salt-master event bus and sends events to the event channel.
type EventListener struct {
	// ctx specificies the context used mainly for cancellation
	ctx context.Context

	// eventChan is the channel to send events to
	eventChan chan event.SaltEvent

	// iPC is the endpoint to the salt-master event bus
	iPC string

	// saltEventBus keeps the connection to the salt-master event bus
	saltEventBus net.Conn

	// decoder is msgpack decoder for parsing the event bus messages
	decoder *msgpack.Decoder

	eventParser eventParser

	mt sync.Mutex
}

// Open opens the salt-master event bus.
func (e *EventListener) Open() {
	log.Info().Str("endpoint", e.iPC).Msg("connecting to salt-master event bus")
	var err error
	var scheme string

	if strings.HasPrefix(e.iPC, "tcp://") {
		scheme = "tcp"
	} else {
		scheme = "unix"
	}
	address := strings.TrimPrefix(e.iPC, scheme+"://")

	for {
		select {
		case <-e.ctx.Done():
			return
		default:
		}

		e.saltEventBus, err = net.Dial(scheme, address)
		if err != nil {
			log.Error().Msg("failed to connect to event bus, retrying in 5 seconds")
			time.Sleep(time.Second * 5)
			continue
		}
		e.mt.Lock()
		defer e.mt.Unlock()
		log.Info().Msg("successfully connected to event bus")
		e.decoder = msgpack.NewDecoder(e.saltEventBus)
		return
	}
}

// Close closes the salt-master event bus.
func (e *EventListener) Close() error {
	log.Info().Msg("disconnecting from salt-master event bus")
	if e.saltEventBus != nil {
		return e.saltEventBus.Close()
	} else {
		return errors.New("trying to close already closed bus")
	}
}

// Reconnect reconnects to the salt-master event bus.
func (e *EventListener) Reconnect() {
	select {
	case <-e.ctx.Done():
		return
	default:
		e.Close()
		e.Open()
	}
}

// NewEventListener creates a new EventListener
//
// The events will be sent to eventChan.
func NewEventListener(ctx context.Context, eventParser eventParser, eventChan chan event.SaltEvent) *EventListener {
	e := EventListener{
		ctx:         ctx,
		eventChan:   eventChan,
		eventParser: eventParser,
		iPC:         DefaultIPCFilepath,
		mt:          sync.Mutex{},
	}
	return &e
}

// SetIPC sets the endpoint to the salt-master event bus
//
// The IPC file must be readable by the user running the exporter.
//
// Default: /var/run/salt/master/master_event_pub.ipc.
func (e *EventListener) SetIPC(endpoint string) {
	e.iPC = endpoint
}

// ListenEvents listens to the salt-master event bus and sends events to the event channel.
func (e *EventListener) ListenEvents() {
	for {
		select {
		case <-e.ctx.Done():
			log.Info().Msg("stop listening events")
			e.mt.Lock()
			e.Close()
			e.mt.Unlock()
			return
		default:
			e.mt.Lock()
			message, err := e.decoder.DecodeMap()
			e.mt.Unlock()
			if err != nil {
				log.Error().Str("error", err.Error()).Msg("unable to read event")
				log.Error().Msg("event bus may be closed, trying to reconnect")

				e.Reconnect()

				continue
			}
			if event, err := e.eventParser.Parse(message); err == nil {
				e.eventChan <- event
			}
		}
	}
}
