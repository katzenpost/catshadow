// server.go - CBOR based catshadow controller server.
// Copyright (C) 2019  David Stainton.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, either version 3 of the
// License, or (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

// Package server provides a catshadow controller protocol.
package server

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io/ioutil"
	"net"
	"sync"
	"syscall"

	"github.com/ugorji/go/codec"
	"gopkg.in/op/go-logging.v1"
)

const (
	serverNetwork = "UNIX"
)

// Server is a Unix domain socket server
// which handles only one connection at a time.
type Server struct {
	sync.WaitGroup

	socketFile string
	l          net.Listener
	conn       net.Conn
	log        *logging.Logger
	closeAllCh chan interface{}
}

func New(log *logging.Logger) (*Server, error) {
	syscall.Umask(0077)

	var err error
	s := &Server{
		log:        log,
		closeAllCh: make(chan interface{}),
	}
	socketFile, err := ioutil.TempFile("", "catshadow")
	if err != nil {
		return nil, err
	}
	s.socketFile = socketFile.Name()

	return s, nil
}

func (s *Server) Start() error {
	var err error
	s.l, err = net.Listen(serverNetwork, s.socketFile)
	if err != nil {
		return err
	}
	s.log.Debugf("Listening on: %v", s.socketFile)

	return nil
}

func (s *Server) Halt() {
	s.log.Debug("gracefully halting")
	if s.l != nil {
		s.l.Close()
		close(s.closeAllCh)
	}
	s.Wait()
	s.l = nil
}

func (s *Server) readCommand() ([]byte, error) {
	lenBytes := [4]byte{}
	count, err := s.conn.Read(lenBytes[:])
	if err != nil {
		return nil, err
	}
	if count != 4 {
		return nil, errors.New("read count is not 4")
	}
	mesgLen := binary.BigEndian.Uint32(lenBytes[:])
	buffer := make([]byte, mesgLen)
	count, err = s.conn.Read(buffer)
	if err != nil {
		return nil, err
	}
	if uint32(count) != mesgLen {
		return nil, errors.New("read count is not message size")
	}
	return buffer, nil
}

func (s *Server) onCommand(rawCmd []byte) error {
	command := make(Command)
	err := codec.NewDecoder(bytes.NewBuffer(rawCmd), new(codec.CborHandle)).Decode(&command)
	if err != nil {
		return err
	}
	return nil
}

func (s *Server) worker() {
	closedCh := make(chan interface{})
	defer func() {
		s.log.Debugf("Closing")
		s.conn.Close()
		s.Done()
	}()

	var err error
	go func() {
		defer close(closedCh)
		for {
			s.conn, err = s.l.Accept()
			if err != nil {
				if e, ok := err.(net.Error); ok && !e.Temporary() {
					s.log.Errorf("Critical accept failure: %v", err)
					return
				}
				s.log.Debugf("Transient accept failure: %v", err)
				continue
			}
			s.log.Debugf("Accepted new connection: %v", s.conn.RemoteAddr())

			cmd, err := s.readCommand()
			if err != nil {
				return
			}
			if err = s.onCommand(cmd); err != nil {
				s.log.Debugf("Failed to process command: %v", err)
				return
			}
		}
	}()

	// Wait till Server teardown, or the command processing go routine
	// returns for whatever reason.
	select {
	case <-s.closeAllCh:
		// Server teardown, close the connection and wait for the go
		// routine to return.
		s.conn.Close()
		<-closedCh
	case <-closedCh:
	}
}
