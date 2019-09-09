//Package whatsapp provides a developer API to interact with the WhatsAppWeb-Servers.
package whatsapp

import (
	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"time"
)

type metric byte

const (
	debugLog metric = iota + 1
	queryResume
	queryReceipt
	queryMedia
	queryChat
	queryContacts
	queryMessages
	presence
	presenceSubscribe
	group
	read
	chat
	received
	pic
	status
	message
	queryActions
	block
	queryGroup
	queryPreview
	queryEmoji
	queryMessageInfo
	spam
	querySearch
	queryIdentity
	queryUrl
	profile
	contact
	queryVcard
	queryStatus
	queryStatusUpdate
	privacyStatus
	queryLiveLocations
	liveLocation
	queryVname
	queryLabels
	call
	queryCall
	queryQuickReplies
)

type flag byte

const (
	ignore flag = 1 << (7 - iota)
	ackRequest
	available
	notAvailable
	expires
	skipOffline
)

/*
Conn is created by NewConn. Interacting with the initialized Conn is the main way of interacting with our package.
It holds all necessary information to make the package work internally.
*/
type Conn struct {
	ws       *websocketWrapper
	listener *listenerWrapper

	connected bool
	loggedIn  bool
	wg        *sync.WaitGroup

	session        *Session
	sessionLock    uint32
	handler        []Handler
	msgCount       int64
	msgTimeout     time.Duration
	Info           *Info
	Store          *Store
	ServerLastSeen time.Time

	longClientName  string
	shortClientName string
	id              int
}

type websocketWrapper struct {
	sync.Mutex
	conn  *websocket.Conn
	close chan struct{}
}

type listenerWrapper struct {
	sync.RWMutex
	m map[string]chan string
}

var connCount = 0

/*
Creates a new connection with a given timeout. The websocket connection to the WhatsAppWeb servers get´s established.
The goroutine for handling incoming messages is started
*/
func NewConn(timeout time.Duration) (*Conn, error) {
	connCount += 1

	wac := &Conn{
		id:         connCount,
		handler:    make([]Handler, 0),
		msgCount:   int64(connCount) * 10000000000,
		msgTimeout: timeout,
		Store:      newStore(),

		longClientName:  "github.com/ximxim8963/go-whatsapp",
		shortClientName: "go-whatsapp",
	}
	return wac, wac.connect()
}

func (wac *Conn) GetId() int {
	return wac.id
}

// connect should be guarded with wsWriteMutex
func (wac *Conn) connect() (err error) {
	if wac.connected {
		return ErrAlreadyConnected
	}
	wac.connected = true
	defer func() { // set connected to false on error
		if err != nil {
			wac.connected = false
		}
	}()

	dialer := &websocket.Dialer{
		ReadBufferSize:   25 * 1024 * 1024,
		WriteBufferSize:  10 * 1024 * 1024,
		HandshakeTimeout: wac.msgTimeout,
	}

	headers := http.Header{"Origin": []string{"https://web.whatsapp.com"}}
	wsConn, _, err := dialer.Dial("wss://web.whatsapp.com/ws", headers)
	if err != nil {
		return errors.Wrap(err, "couldn't dial whatsapp web websocket")
	}

	wsConn.SetCloseHandler(func(code int, text string) error {
		// from default CloseHandler
		message := websocket.FormatCloseMessage(code, "")
		err := wsConn.WriteControl(websocket.CloseMessage, message, time.Now().Add(time.Second))

		// our close handling
		_, _ = wac.Disconnect()
		wac.handle(&ErrConnectionClosed{Code: code, Text: text})
		return err
	})

	wac.ws = &websocketWrapper{
		conn:  wsConn,
		close: make(chan struct{}),
	}

	wac.listener = &listenerWrapper{
		m: make(map[string]chan string),
	}

	wac.wg = &sync.WaitGroup{}
	wac.wg.Add(2)
	go wac.readPump()
	go wac.keepAlive(20000, 60000)

	wac.loggedIn = false
	return nil
}

func (wac *Conn) Disconnect() (Session, error) {
	if !wac.connected {
		return Session{}, ErrNotConnected
	}
	wac.connected = false
	wac.loggedIn = false

	close(wac.ws.close) //signal close
	wac.wg.Wait()       //wait for close

	err := wac.ws.conn.Close()
	wac.ws = nil

	if wac.session == nil {
		return Session{}, err
	}
	return *wac.session, err
}

func (wac *Conn) keepAlive(minIntervalMs int, maxIntervalMs int) {
	defer wac.wg.Done()

	for {
		err := wac.sendKeepAlive()
		if err != nil {
			wac.handle(errors.Wrap(err, "keepAlive failed"))
			//TODO: Consequences?
		}
		interval := rand.Intn(maxIntervalMs-minIntervalMs) + minIntervalMs
		select {
		case <-time.After(time.Duration(interval) * time.Millisecond):
		case <-wac.ws.close:
			log.Println("Websocket close", wac.ws.close)
			os.Exit(88)
			return
		}
	}
}
