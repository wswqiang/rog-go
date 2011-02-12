// The ncrpc package layers client-server and server-client
// RPC interfaces on top of netchan.
package ncrpc

import (
	"os"
	"fmt"
	"io"
	"log"
	"net"
	"rog-go.googlecode.com/hg/ncnet"
	"netchan"
	"rpc"
	"sync"
)

type Server struct {
	Exporter      *netchan.Exporter
	RPCServer *rpc.Server
	mu       sync.Mutex
	clients  map[string]*rpc.Client
	clientid int
}

// Client gets the RPC connection for a given client.
func (srv *Server) Client(name string) (c *rpc.Client) {
	srv.mu.Lock()
	c = srv.clients[name]
	srv.mu.Unlock()
	return
}

// ClientNames returns the list of all clients that have
// published RPC connections to the server.
func (srv *Server) ClientNames() (a []string) {
	srv.mu.Lock()
	for name := range srv.clients {
		a = append(a, name)
	}
	srv.mu.Unlock()
	return
}

// NewServer creates a new RPC-over-netchan
// server. It returns a new Server instance containing
// a netchan.Exporter and an rpc.Server which
// is listening on a channel within it.
// It reserves the use of netchan channels with the
// prefix "ncnet".
//
// If acceptClientRPC is true, the server will accept
// incoming client RPC registrations made by Client.Serve.
//
// Conventionally Register is called on the rpc.Server
// to export some server RPC methods, and ListenAndServe is
// then called on the netchan.Exporter to listen on the network.
func NewServer(acceptClientRPC bool) (*Server, os.Error) {
	rpcsrv := rpc.NewServer()
	exp := netchan.NewExporter()
	nclis, err := ncnet.Listen(exp, "ncnet.ctl")
	if err != nil {
		return nil, err
	}
	srv := &Server{
		clients: make(map[string]*rpc.Client),
		Exporter: exp,
		RPCServer: rpcsrv,
	}
	rpcsrv.RegisterName("Ncnet-publisher", publisher{acceptClientRPC, srv})
	go func() {
		rpcsrv.Accept(nclis)
		nclis.Close()
	}()
	return srv, nil
}

// Client represents an ncrpc client. Importer holds
// the underlying netchan connection, and Server
// can be used to make calls to the server RPC interface.
type Client struct {
	Importer *netchan.Importer
	Server *rpc.Client
}

// Import makes a connection to an ncrpc server
// and calls NewClient on it.
func Import(network, addr string) (*Client, os.Error) {
	conn, err := net.Dial(network, "", addr)
	if err != nil {
		return nil, err
	}
	return NewClient(conn)
}

// NewClient makes a the netchan connection from
// the given connection, imports the rpc service
// from that, and returns both in a new Client instance.
// It assumes that the server has been started
// with Server.
func NewClient(conn io.ReadWriter) (*Client, os.Error) {
	imp := netchan.NewImporter(conn)
	srvconn, err := ncnet.Dial(imp, "ncnet.ctl")
	if err != nil {
		return nil, err
	}
	return &Client{imp, rpc.NewClient(srvconn)}, nil
}

// Serve announces an RPC service on the client using the
// given name (which must currently be unique amongst all
// clients).
func (c *Client) Serve(clientName string, rpcServer *rpc.Server) os.Error {
	var clientId string
	rpcServer.RegisterName("ClientRPC", clientRPC{})		// TODO better name
	if err := c.Server.Call("Ncnet-publisher.Publish", &clientName, &clientId); err != nil {
		return err
	} 
	clientconn, err := ncnet.Dial(c.Importer, clientId)
	if err != nil {
		return err
	}

	go rpcServer.ServeConn(clientconn)
	return nil
}

// clientRPC implements the methods that Server.Publish expects of a client.
type clientRPC struct {}

func (clientRPC) Ping(*struct{}, *struct{}) os.Error {
	return nil
}

// Wait blocks until the client is ready to leave.
// Currently that's forever.
func (clientRPC) Wait(*struct{}, *struct{}) os.Error {
	select {}
	return nil
}

type publisher struct {
	acceptClientRPC bool
	srv *Server
}

// Publish is the RPC method that allows a client to publish
// its own RPC interface. It is called (remotely) by Client.Serve.
func (p publisher) Publish(name *string, clientId *string) os.Error {
	if !p.acceptClientRPC {
		return os.ErrorString("client RPC connections not accepted")
	}
	srv := p.srv
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.clients[*name] != nil {
		return os.ErrorString("client name already exists")
	}
	*clientId = fmt.Sprintf("ncnet.client%d", srv.clientid)
	srv.clientid++
	listener, err := ncnet.Listen(srv.Exporter, *clientId)
	if err != nil {
		return fmt.Errorf("cannot listen on netchan %q: %v", *clientId, err)
	}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("error on ncnet.Accept(%q): %v", *clientId, err)
			return
		}
		listener.Close()
		client := rpc.NewClient(conn)
		err = client.Call("ClientRPC.Ping", &struct{}{}, &struct{}{})
		if err != nil {
			log.Printf("error on init: %v", err)
			return
		}
		srv.mu.Lock()
		srv.clients[*name] = client
		srv.mu.Unlock()
		// when call completes, client has left.
		client.Call("ClientRPC.Wait", &struct{}{}, &struct{}{})
		srv.mu.Lock()
		srv.clients[*name] = nil, false
		srv.mu.Unlock()
	}()
	return nil
}
