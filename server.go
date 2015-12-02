// Copyright 2015 someonegg. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bdmsg

import (
	"github.com/someonegg/goutility/chanutil"
	"golang.org/x/net/context"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// SClient represents a message client in the server side.
// You can send(recive) message to(from) client use SClient.Pumper().
//
// Multiple goroutines can invoke methods on a SClient simultaneously.
type SClient struct {
	Pumper

	c  net.Conn
	wg *sync.WaitGroup

	handshaked int32
}

// NewSClient allocates and returns a new SClient.
//
// The ownership of c will be transferred to SClient, dont
// control it in other places.
func NewSClient(parent context.Context, c net.Conn, ioc Converter,
	h PumperHandler, pumperInN, pumperOutN int, wg *sync.WaitGroup) *SClient {

	rw := ioc.Convert(c)

	t := &SClient{}
	t.c = c
	t.wg = wg
	t.Pumper.init(rw, h, t, pumperInN, pumperOutN)
	t.Pumper.Start(parent, t)

	return t
}

func (c *SClient) OnStop() {
	c.c.Close()
	if sn, ok := c.rw.(StopNotifier); ok {
		sn.OnStop()
	}
	if c.wg != nil {
		c.wg.Done()
	}
}

// The pumper's initial userdata is *SClient.
func (c *SClient) InnerPumper() *Pumper {
	return &c.Pumper
}

func (c *SClient) Conn() net.Conn {
	return c.c
}

func (c *SClient) Handshake() {
	atomic.StoreInt32(&c.handshaked, 1)
}

func (c *SClient) Handshaked() bool {
	return atomic.LoadInt32(&c.handshaked) == 1
}

// Server represents a message server.
// You can accept message clients use it.
//
// Multiple goroutines can invoke methods on a Server simultaneously.
type Server struct {
	err     error
	quitCtx context.Context
	quitF   context.CancelFunc
	stopD   chanutil.DoneChan

	l    net.Listener
	ioc  Converter
	hsto time.Duration

	h          PumperHandler
	pumperInN  int
	pumperOutN int

	cliWG sync.WaitGroup
}

// NewServer allocates and returns a new Server.
//
// Note: hsto is "handshake timeout".
func NewServerF(l net.Listener, ioc Converter, hsto time.Duration,
	h PumperHandler, pumperInN, pumperOutN int) *Server {

	s := &Server{}

	s.quitCtx, s.quitF = context.WithCancel(context.Background())
	s.stopD = chanutil.NewDoneChan()
	s.l = l
	s.ioc = ioc
	s.hsto = hsto
	s.h = h
	s.pumperInN = pumperInN
	s.pumperOutN = pumperOutN

	return s
}

func (s *Server) Start() {
	go s.work(s.quitCtx)
}

func (s *Server) work(ctx context.Context) {
	defer s.ending()

	for q := false; !q; {
		c, err := s.l.Accept()
		if err != nil {
			panic(err)
		}
		if c != nil {
			s.newClient(c)
		}

		select {
		case <-ctx.Done():
			q = true
		default:
		}
	}
}

func (s *Server) ending() {
	if e := recover(); e != nil {
		switch v := e.(type) {
		case error:
			s.err = v
		default:
			s.err = errUnknownPanic
		}
	}

	s.stopD.SetDone()
}

func (s *Server) newClient(c net.Conn) {
	s.cliWG.Add(1)
	cli := NewSClient(s.quitCtx, c, s.ioc,
		s.h, s.pumperInN, s.pumperOutN, &s.cliWG)

	if s.hsto > 0 {
		go monitorHSTO(cli, s.hsto)
	}
}

func monitorHSTO(c *SClient, hsto time.Duration) {
	defer func() { recover() }()
	select {
	case <-time.After(hsto):
		if !c.Handshaked() {
			c.Stop()
		}
	}
}

// Return non-nil if an error has happened.
// When errored, the server will stop.
func (s *Server) Err() error {
	return s.err
}

// Request to stop the server.
func (s *Server) Stop() {
	s.quitF()
	s.l.Close()
}

// Returns a done channel, it will be
// signaled when the server is stopped.
func (s *Server) StopD() chanutil.DoneChanR {
	return s.stopD.R()
}

func (s *Server) Stopped() bool {
	return s.stopD.R().Done()
}

func (s *Server) WaitClients() {
	s.cliWG.Wait()
}
