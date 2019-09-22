// catshadow_test.go - Katzenpost catshadow library tests.
// Copyright (C) 2019  Masala, David Stainton.
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

// Package client provides a Katzenpost client library.
package catshadow

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	csConfig "github.com/katzenpost/catshadow/config"
	"github.com/katzenpost/client"
	"github.com/katzenpost/client/config"
	"github.com/katzenpost/core/crypto/rand"
	"github.com/katzenpost/kimchi"
	"github.com/stretchr/testify/require"
)

const basePort = 30000

func TestCreateCatshadowClient(t *testing.T) {
	require := require.New(t)

	k := kimchi.NewKimchi(basePort, "", nil, false, 0, 2, 6)
	k.Run()
	go func() {
		defer k.Shutdown()
		<-time.After(90 * time.Second) // must wait for provider to fetch pki document

		client, err := getCatshadowClient(k)
		require.NoError(err)
		// Start catshadow client.
		client.Shutdown()
		client.Wait()
	}()
	k.Wait()
}

func TestCatshadowMessaging(t *testing.T) {
	require := require.New(t)

	k := kimchi.NewKimchi(basePort, "", nil, false, 0, 2, 6)
	k.Run()
	go func() {
		defer k.Shutdown()
		<-time.After(90 * time.Second) // must wait for provider to fetch pki document

		clients := make([]*Client, 0)
		wg := new(sync.WaitGroup)
		for i:=0; i<2;i++ {
			client, err := getCatshadowClient(k)
			require.NoError(err)
			clients = append(clients, client)
		}
		// panda exchange between pairs of clients
		for i, client := range clients {
			cl := client
			pair := fmt.Sprintf("secret_pair %v", i % (len(clients) / 2))
			contact := fmt.Sprintf("client-%v", i)
			wg.Add(1)
			go func() {
				t.Logf("secret_pair: %v with %v", pair, contact)
				cl.NewContact(contact, []byte(pair))
				ch := cl.EventsChan()
				go func(){
					select {
					case ev := <-ch:
						_, ok := ev.(KeyExchangeCompleted)
						require.NotNil(ok)
					case <-time.After(60*time.Second):
						// timed out..
						t.Errorf("PANDA Exchange timed out")
					}
					wg.Done()
				}()
			}()
		}
		wg.Wait()

		// Send messages between clients
		for i, client := range clients {
			contact := fmt.Sprintf("client-%v", i)
			cl := client
			go func() {
				t.Logf("Saying hello to %v", contact)
				cl.SendMessage(contact, []byte("hello, catshadow"))
				ch := cl.EventsChan()
				wg.Add(1)
				go func(){
					select {
					case ev := <-ch:
						_, ok := ev.(MessageDelivered)
						require.NotNil(ok)
					case <-time.After(60*time.Second):
						t.Errorf("SendMessage timed out")
					}
					wg.Done()
				}()
			}()
		}

		wg.Wait()

		// turn it off!
		for _, client := range clients {
			cl := client
			wg.Add(1)
			go func() {
				cl.Shutdown()
				cl.Wait()
				wg.Done()
			}()
		}
		wg.Wait()
	}()
	k.Wait()
}


// returns a running CatshadowClient configured from kimchi
func getCatshadowClient(k *kimchi.Kimchi) (*Client, error) {
	cfg, username, linkKey, err := k.GetClientConfig()
	if err != nil {
		return nil, err
	}
	cfg.Panda = &config.Panda{Receiver: "+panda", Provider: "provider-0", BlobSize: 1000,}
	// instantiate a client instance
	c, err := client.New(cfg)
	if err != nil {
		return nil, err
	}

	// add client log output
	go k.LogTailer(username, cfg.Logging.File)

	// create a tmpdir
	tmpDir, err := ioutil.TempDir("", "catshadow_test")
	if err != nil {
		return nil, err
	}

	// get a random id for statefile
	id := [6]byte{}
	_, err = rand.Reader.Read(id[:])
	if err != nil {
		return nil, err
	}
	stateFile := filepath.Join(tmpDir, fmt.Sprintf("%x.catshadow.state", id))
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		panic(err)
	}
	passphrase := []byte("")

	// initialize statewriter
	stateWorker, err := NewStateWriter(c.GetLogger("catshadow_state"), stateFile, passphrase)
	if err != nil {
		return nil, err
	}
	csCfg := &csConfig.Config{Logging: cfg.Logging}
	backendLog, err := csCfg.InitLogBackend()
	if err != nil {
		return nil, err
	}

	user := fmt.Sprintf("%x", linkKey.PublicKey().Bytes())
	cs, err := NewClientAndRemoteSpool(backendLog, c, stateWorker, user, linkKey)
	if err != nil {
		return nil, err
	}
	stateWorker.Start()
	cs.Start()
	return cs, nil
}
