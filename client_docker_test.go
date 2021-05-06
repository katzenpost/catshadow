// SPDX-FileCopyrightText: 2019, David Stainton <dawuud@riseup.net>
// SPDX-License-Identifier: AGPL-3.0-or-later
//
// client.go - client
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

// +build docker_test

package catshadow

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/katzenpost/catshadow/config"
	"github.com/katzenpost/client"
	cConfig "github.com/katzenpost/client/config"
	"github.com/katzenpost/core/crypto/rand"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func getClientState(c *Client) *State {
	contacts := []*Contact{}
	for _, contact := range c.contacts {
		contacts = append(contacts, contact)
	}
	return &State{
		SpoolReadDescriptor: c.spoolReadDescriptor,
		Contacts:            contacts,
		LinkKey:             c.linkKey,
		User:                c.user,
		Provider:            c.client.Provider(),
		Conversations:       c.GetAllConversations(),
	}
}

func createRandomStateFile(t *testing.T) string {
	require := require.New(t)

	tmpDir, err := ioutil.TempDir("", "catshadow_test")
	require.NoError(err)
	id := [6]byte{}
	_, err = rand.Reader.Read(id[:])
	require.NoError(err)
	stateFile := filepath.Join(tmpDir, fmt.Sprintf("%x.catshadow.state", id))
	_, err = os.Stat(stateFile)
	require.True(os.IsNotExist(err))
	return stateFile
}

func createCatshadowClientWithState(t *testing.T, stateFile string, username string, useReunion bool) *Client {
	require := require.New(t)

	catshadowCfg, err := config.LoadFile("testdata/catshadow.toml")
	require.NoError(err)
	if useReunion {
		catshadowCfg.Panda = nil
		catshadowCfg.Reunion.Enable = true
	} else {
		catshadowCfg.Reunion.Enable = false
	}
	var stateWorker *StateWriter
	var catShadowClient *Client
	cfg, err := catshadowCfg.ClientConfig()
	require.NoError(err)

	cfg, linkKey, err := client.AutoRegisterRandomClient(cfg)
	require.NoError(err)
	//cfg.Logging.Level = "INFO" // client verbosity reductionism
	c, err := client.New(cfg)
	require.NoError(err)
	passphrase := []byte("")
	stateWorker, err = NewStateWriter(c.GetLogger(username+" catshadow_state"), stateFile, passphrase)
	require.NoError(err)
	// must start stateWorker BEFORE calling NewClientAndRemoteSpool
	stateWorker.Start()
	backendLog, err := catshadowCfg.InitLogBackend()
	require.NoError(err)

	// XXX: this can time out because it is UNRELIABLE
	tries := 3
	err = nil
	for ; tries > 0; tries-- {
		catShadowClient, err = NewClientAndRemoteSpool(backendLog, c, stateWorker, username, linkKey)
		if err != client.ErrReplyTimeout || err == nil {
			break
		}
		t.Log("NewClientAndRemoteSpool timed out")
	}
	require.NoError(err)
	t.Log("NewClientAndRemoteSpool created a contact and remote spool")

	// Start catshadow client.
	catShadowClient.Start()

	return catShadowClient
}

func reloadCatshadowState(t *testing.T, stateFile string) *Client {
	require := require.New(t)

	// Load catshadow config file.
	catshadowCfg, err := config.LoadFile("testdata/catshadow.toml")
	require.NoError(err)
	var stateWorker *StateWriter
	var catShadowClient *Client

	cfg, err := catshadowCfg.ClientConfig()
	require.NoError(err)

	passphrase := []byte("")
	key := stretchKey(passphrase)
	state, err := decryptStateFile(stateFile, key)
	require.NoError(err)
	cfg.Account = &cConfig.Account{
		User:     state.User,
		Provider: state.Provider,
	}

	logBackend, err := catshadowCfg.InitLogBackend()
	require.NoError(err)
	c, err := client.New(cfg)
	require.NoError(err)
	stateWorker, state, err = LoadStateWriter(c.GetLogger(cfg.Account.User+" "+"catshadow_state"), stateFile, passphrase)
	require.NoError(err)

	catShadowClient, err = New(logBackend, c, stateWorker, state)
	require.NoError(err)

	// Start catshadow client.
	stateWorker.Start()
	catShadowClient.Start()

	return catShadowClient
}

func TestDockerPandaSuccess(t *testing.T) {
	require := require.New(t)

	aliceState := createRandomStateFile(t)
	alice := createCatshadowClientWithState(t, aliceState, "alice", false)
	bobState := createRandomStateFile(t)
	bob := createCatshadowClientWithState(t, bobState, "bob", false)

	sharedSecret := []byte("There is a certain kind of small town that grows like a boil on the ass of every Army base in the world.")
	randBytes := [8]byte{}
	_, err := rand.Reader.Read(randBytes[:])
	require.NoError(err)
	sharedSecret = append(sharedSecret, randBytes[:]...)

	alice.NewContact("bob", sharedSecret)
	bob.NewContact("alice", sharedSecret)

loop1:
	for {
		ev := <-alice.EventSink
		switch event := ev.(type) {
		case *KeyExchangeCompletedEvent:
			require.Nil(event.Err)
			break loop1
		default:
		}
	}

loop2:
	for {
		ev := <-bob.EventSink
		switch event := ev.(type) {
		case *KeyExchangeCompletedEvent:
			require.Nil(event.Err)
			break loop2
		default:
		}
	}

	alice.Shutdown()
	bob.Shutdown()
}

func TestDockerPandaTagContendedError(t *testing.T) {
	require := require.New(t)

	aliceStateFilePath := createRandomStateFile(t)
	alice := createCatshadowClientWithState(t, aliceStateFilePath, "alice", false)
	bobStateFilePath := createRandomStateFile(t)
	bob := createCatshadowClientWithState(t, bobStateFilePath, "bob", false)

	sharedSecret := []byte("twas brillig and the slithy toves")
	randBytes := [8]byte{}
	_, err := rand.Reader.Read(randBytes[:])
	require.NoError(err)
	sharedSecret = append(sharedSecret, randBytes[:]...)

	alice.NewContact("bob", sharedSecret)
	bob.NewContact("alice", sharedSecret)

loop1:
	for {
		ev := <-alice.EventSink
		switch event := ev.(type) {
		case *KeyExchangeCompletedEvent:
			require.Nil(event.Err)
			break loop1
		default:
		}
	}

loop2:
	for {
		ev := <-bob.EventSink
		switch event := ev.(type) {
		case *KeyExchangeCompletedEvent:
			require.Nil(event.Err)
			break loop2
		default:
		}
	}

	alice.Shutdown()
	bob.Shutdown()

	// second phase of test, use same panda shared secret
	// in order to test that it invokes a tag contended error
	adaState := createRandomStateFile(t)
	ada := createCatshadowClientWithState(t, adaState, "ada", false)
	jeffState := createRandomStateFile(t)
	jeff := createCatshadowClientWithState(t, jeffState, "jeff", false)

	ada.NewContact("jeff", sharedSecret)
	jeff.NewContact("ada", sharedSecret)

loop3:
	for {
		ev := <-ada.EventSink
		switch event := ev.(type) {
		case *KeyExchangeCompletedEvent:
			require.NotNil(event.Err)
			break loop3
		default:
		}
	}

loop4:
	for {
		ev := <-jeff.EventSink
		switch event := ev.(type) {
		case *KeyExchangeCompletedEvent:
			require.NotNil(event.Err)
			break loop4
		default:
		}
	}

	ada.Shutdown()
	jeff.Shutdown()
}

func TestDockerSendReceive(t *testing.T) {
	require := require.New(t)

	aliceStateFilePath := createRandomStateFile(t)
	alice := createCatshadowClientWithState(t, aliceStateFilePath, "alice", false)
	bobStateFilePath := createRandomStateFile(t)
	bob := createCatshadowClientWithState(t, bobStateFilePath, "bob", false)
	malStateFilePath := createRandomStateFile(t)
	mal := createCatshadowClientWithState(t, malStateFilePath, "mal", false)

	sharedSecret := []byte(`oxcart pillage village bicycle gravity socks`)
	sharedSecret2 := make([]byte, len(sharedSecret))
	randBytes := [8]byte{}
	_, err := rand.Reader.Read(randBytes[:])
	require.NoError(err)
	_, err = rand.Reader.Read(sharedSecret2[:])
	require.NoError(err)
	sharedSecret = append(sharedSecret, randBytes[:]...)

	alice.NewContact("bob", sharedSecret)
	// bob has 2 contacts
	bob.NewContact("alice", sharedSecret)
	bob.NewContact("mal", sharedSecret2)
	mal.NewContact("bob", sharedSecret2)

	bobKXFinishedChan := make(chan bool)
	bobReceivedMessageChan := make(chan bool)
	bobSentChan := make(chan bool)
	bobDeliveredChan := make(chan bool)
	go func() {
		for {
			ev := <-bob.EventSink
			switch event := ev.(type) {
			case *KeyExchangeCompletedEvent:
				require.Nil(event.Err)
				bobKXFinishedChan <- true
			case *MessageReceivedEvent:
				// fields: Nickname, Message, Timestamp
				bob.log.Debugf("BOB RECEIVED MESSAGE from %s:\n%s", event.Nickname, string(event.Message))
				bobReceivedMessageChan <- true
			case *MessageDeliveredEvent:
				require.Equal(event.Nickname, "mal")
				bobDeliveredChan <- true
			case *MessageSentEvent:
				bob.log.Debugf("BOB SENT MESSAGE to %s", event.Nickname)
				require.Equal(event.Nickname, "mal")
				bobSentChan <- true
			default:
			}
		}
	}()

	aliceKXFinishedChan := make(chan bool)
	aliceSentChan := make(chan bool)
	aliceDeliveredChan := make(chan bool)
	go func() {
		for {
			ev := <-alice.EventSink
			switch event := ev.(type) {
			case *KeyExchangeCompletedEvent:
				require.Nil(event.Err)
				aliceKXFinishedChan <- true
				break
			case *MessageSentEvent:
				alice.log.Debugf("ALICE SENT MESSAGE to %s", event.Nickname)
				require.Equal(event.Nickname, "bob")
				aliceSentChan <- true
			case *MessageDeliveredEvent:
				require.Equal(event.Nickname, "bob")
				aliceDeliveredChan <- true
			default:
			}
		}
	}()

	malKXFinishedChan := make(chan bool)
	malSentChan := make(chan bool)
	malReceivedMessageChan := make(chan bool)
	malDeliveredChan := make(chan bool)
	go func() {
		for {
			ev := <-mal.EventSink
			switch event := ev.(type) {
			case *KeyExchangeCompletedEvent:
				require.Nil(event.Err)
				malKXFinishedChan <- true
			case *MessageReceivedEvent:
				// fields: Nickname, Message, Timestamp
				require.Equal(event.Nickname, "bob")
				mal.log.Debugf("MAL RECEIVED MESSAGE:\n%s", string(event.Message))
				malReceivedMessageChan <- true
			case *MessageDeliveredEvent:
				require.Equal(event.Nickname, "bob")
				malDeliveredChan <- true
			case *MessageSentEvent:
				mal.log.Debugf("MAL SENT MESSAGE to %s", event.Nickname)
				require.Equal(event.Nickname, "bob")
				malSentChan <- true

			default:
			}
		}
	}()

	<-bobKXFinishedChan
	<-aliceKXFinishedChan
	<-malKXFinishedChan
	<-bobKXFinishedChan
	alice.SendMessage("bob", []byte(`Data encryption is used widely to protect the content of Internet
communications and enables the myriad of activities that are popular today,
from online banking to chatting with loved ones. However, encryption is not
sufficient to protect the meta-data associated with the communications.
`))
	<-aliceSentChan
	<-aliceDeliveredChan
	<-bobReceivedMessageChan

	alice.SendMessage("bob", []byte(`Since 1979, there has been active academic research into communication
meta-data protection, also called anonymous communication networking, that has
produced various designs. Of these, mix networks are among the most practical
and can readily scale to millions of users.
`))
	<-aliceSentChan
	<-aliceDeliveredChan
	<-bobReceivedMessageChan

	mal.SendMessage("bob", []byte(`Hello bob`))
	<-malSentChan
	<-malDeliveredChan
	<-bobReceivedMessageChan

	// bob replies to mal
	bob.SendMessage("mal", []byte(`Hello mal`))
	<-bobSentChan
	<-bobDeliveredChan
	<-malReceivedMessageChan

	// Test statefile persistence of conversation.

	alice.log.Debug("LOADING ALICE'S CONVERSATION")
	aliceConvesation := alice.GetConversation("bob")
	for i, mesg := range aliceConvesation {
		alice.log.Debugf("%d outbound %v message:\n%s\n", i, mesg.Outbound, mesg.Plaintext)
	}

	// Test sorted conversation and message delivery status
	aliceSortedConvesation := alice.GetSortedConversation("bob")
	for _, msg := range aliceSortedConvesation {
		require.True(msg.Sent)
		require.True(msg.Delivered)
	}

	bob.log.Debug("LOADING BOB'S CONVERSATION")
	bobConvesation := bob.GetConversation("alice")
	for i, mesg := range bobConvesation {
		bob.log.Debugf("%d outbound %v message:\n%s\n", i, mesg.Outbound, mesg.Plaintext)
	}

	mal.log.Debug("LOADING MAL'S CONVERSATION")
	malConvesation := mal.GetConversation("bob")
	for i, mesg := range malConvesation {
		bob.log.Debugf("%d outbound %v message:\n%s\n", i, mesg.Outbound, mesg.Plaintext)
	}

	alice.Shutdown()
	bob.Shutdown()
	mal.Shutdown()

	newAlice := reloadCatshadowState(t, aliceStateFilePath)
	newAlice.log.Debug("LOADING ALICE'S CONVERSATION WITH BOB")
	aliceConvesation = newAlice.GetConversation("bob")
	for i, mesg := range aliceConvesation {
		newAlice.log.Debugf("%d outbound %v message:\n%s\n", i, mesg.Outbound, mesg.Plaintext)
	}

	newBob := reloadCatshadowState(t, bobStateFilePath)
	newBob.log.Debug("LOADING BOB'S CONVERSATION WITH ALICE")
	bobConvesation = newBob.GetConversation("alice")
	for i, mesg := range bobConvesation {
		newBob.log.Debugf("%d outbound %v message:\n%s\n", i, mesg.Outbound, mesg.Plaintext)
	}

	newMal := reloadCatshadowState(t, malStateFilePath)
	newMal.log.Debug("LOADING MAL'S CONVERSATION WITH BOB")
	malBobConversation := newMal.GetConversation("bob")
	for i, mesg := range malBobConversation {
		newMal.log.Debugf("%d outbound %v message:\n%s\n", i, mesg.Outbound, mesg.Plaintext)
		if !mesg.Outbound {
			require.True(bytes.Equal(mesg.Plaintext, []byte(`Hello mal`)))
		} else {
			require.True(bytes.Equal(mesg.Plaintext, []byte(`Hello bob`)))
		}
	}

	newBob.log.Debug("LOADING BOB'S CONVERSATION WITH MAL")
	bobMalConversation := newBob.GetConversation("mal")
	for i, mesg := range bobMalConversation {
		newBob.log.Debugf("%d outbound %v message:\n%s\n", i, mesg.Outbound, mesg.Plaintext)
		if !mesg.Outbound {
			require.True(bytes.Equal(mesg.Plaintext, []byte(`Hello bob`)))
		} else {
			require.True(bytes.Equal(mesg.Plaintext, []byte(`Hello mal`)))
		}
	}

	newAliceState := getClientState(newAlice)
	aliceState := getClientState(alice)
	aliceBobConvo1 := aliceState.Conversations["bob"]
	aliceBobConvo2 := newAliceState.Conversations["bob"]
	newAlice.log.Debug("convo1\n")
	for i, message := range aliceBobConvo1 {
		require.True(bytes.Equal(message.Plaintext, aliceBobConvo2[i].Plaintext))
		// XXX require.True(message.Timestamp.Equal(aliceBobConvo2[i].Timestamp))
	}
	newAlice.Shutdown()
	newBob.Shutdown()
}

func TestDockerReunionSuccess(t *testing.T) {
	require := require.New(t)

	aliceState := createRandomStateFile(t)
	alice := createCatshadowClientWithState(t, aliceState, "alice", true)

	bobState := createRandomStateFile(t)
	bob := createCatshadowClientWithState(t, bobState, "bob", true)

	sharedSecret := []byte("There is a certain kind of small town that grows like a boil on the ass of every Army base in the world.")
	randBytes := [8]byte{}
	_, err := rand.Reader.Read(randBytes[:])
	require.NoError(err)
	sharedSecret = append(sharedSecret, randBytes[:]...)

	alice.NewContact("bob", sharedSecret)
	bob.NewContact("alice", sharedSecret)

	//for i:=0; i<10; i++ {
	//	go func() {
	//		malState := createRandomStateFile(t)
	//		mal := createCatshadowClientWithState(t, malState)
	//		antifaState := createRandomStateFile(t)
	//		antifa := createCatshadowClientWithState(t, antifaState)
	//		randBytes := [8]byte{}
	//		rand.Reader.Read(randBytes[:])

	//		go func() {mal.NewContact("antifa", randBytes[:])}()
	//		go func() {antifa.NewContact("mal", randBytes[:])}()
	//	}()
	//}
	afails := 0
	bfails := 0

loop1:
	for {
		ev := <-alice.EventSink
		switch event := ev.(type) {
		case *KeyExchangeCompletedEvent:
			// XXX: we do multiple exchanges, some will fail
			alice.log.Debugf("reunion ALICE RECEIVED event: %v\n", event)
			if event.Err != nil {
				afails++
				require.True(afails < 6)
				continue
			} else {
				break loop1
			}
		default:
		}
	}

loop2:
	for {
		ev := <-bob.EventSink
		switch event := ev.(type) {
		case *KeyExchangeCompletedEvent:
			// XXX: we do multiple exchanges, some will fail
			bob.log.Debugf("reunion BOB RECEIVED event: %v\n", event)
			if event.Err != nil {
				bfails++
				require.True(bfails < 6)
				continue
			} else {
				break loop2
			}
		default:
		}
	}

	alice.Shutdown()
	bob.Shutdown()
}

// This test must fail or else everything works
func TestTillDistress(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	if deadline, ok := t.Deadline(); ok {
		timeLeft := deadline.Sub(time.Now())
		if timeLeft < 4*time.Minute {
			t.Skipf("Forget it, this test needs longer than %s to run to be useful", timeLeft)
		}
	}

	aliceStateFilePath := createRandomStateFile(t)
	alice := createCatshadowClientWithState(t, aliceStateFilePath, "alice", false)
	bobStateFilePath := createRandomStateFile(t)
	bob := createCatshadowClientWithState(t, bobStateFilePath, "bob", false)

	sharedSecret := []byte("blah")
	randBytes := [8]byte{}
	_, err := rand.Reader.Read(randBytes[:])
	require.NoError(err)
	sharedSecret = append(sharedSecret, randBytes[:]...)

	t.Log("Alice adds contact bob")
	alice.NewContact("bob", sharedSecret)
	t.Log("Bob adds contact alice")
	bob.NewContact("alice", sharedSecret)

	bobKXFinishedChan := make(chan bool)
	bobDeliveredChan := make(chan bool, 1)
	bobReceivedChan := make(chan bool, 1)

	haltCh := make(chan interface{})

	var wait sync.WaitGroup
	wait.Add(4)

	// keep track of the last time a message was received by each client
	var aliceLast, bobLast time.Time
	go func() {
		for {
			select {
			case <-haltCh:
				t.Log("haltCh closed!")
				close(bobKXFinishedChan)
				close(bobDeliveredChan)
				close(bobReceivedChan)
				wait.Done()
				return
			case ev := <-bob.EventSink:
				switch event := ev.(type) {
				case *KeyExchangeCompletedEvent:
					require.Nil(event.Err)
					bobKXFinishedChan <- true
					bob.log.Debug("BOB completed key exchange")
				case *MessageReceivedEvent:
					// fields: Nickname, Message, Timestamp
					bob.log.Debugf("BOB RECEIVED MESSAGE from %s:\n%s", event.Nickname, string(event.Message))
					bobLast = time.Now()
					bobReceivedChan <- true
				case *MessageDeliveredEvent:
					require.Equal(event.Nickname, "alice")
					bobDeliveredChan <- true
				case *MessageSentEvent:
					bob.log.Debugf("BOB SENT MESSAGE to %s", event.Nickname)
					require.Equal(event.Nickname, "alice")
				default:
					bob.log.Debugf("BOB event %v", event)
				}
			}
		}
	}()

	aliceKXFinishedChan := make(chan bool)
	aliceDeliveredChan := make(chan bool, 1)
	aliceReceivedChan := make(chan bool, 1)
	go func() {
		for {
			select {
			case <-haltCh:
				t.Log("haltCh closed!")
				close(aliceKXFinishedChan)
				close(aliceDeliveredChan)
				close(aliceReceivedChan)
				wait.Done()
				return
			case ev := <-alice.EventSink:
				switch event := ev.(type) {
				case *KeyExchangeCompletedEvent:
					require.Nil(event.Err)
					aliceKXFinishedChan <- true
					alice.log.Debug("ALICE completed key exchange")
				case *MessageReceivedEvent:
					// fields: Nickname, Message, Timestamp
					alice.log.Debugf("ALICE RECEIVED MESSAGE from %s:\n%s", event.Nickname, string(event.Message))
					aliceLast = time.Now()
					aliceReceivedChan <- true
				case *MessageDeliveredEvent:
					require.Equal(event.Nickname, "bob")
					aliceDeliveredChan <- true
				case *MessageSentEvent:
					alice.log.Debugf("ALICE SENT MESSAGE to %s", event.Nickname)
					require.Equal(event.Nickname, "bob")
				default:
					alice.log.Debugf("ALICE event %v", event)
				}
			}
		}
	}()

	<-bobKXFinishedChan
	<-aliceKXFinishedChan
	aliceLast = time.Now()
	bobLast = time.Now()

	i := 0
	go func() {
		t.Logf("Alice has joined the chat")
		for {
			select {
			case <-haltCh:
				t.Log("haltCh closed!")
				wait.Done()
				return
			default:
			}

			msg := fmt.Sprintf("hi bob, it's the %d time i call you...", i)
			alice.SendMessage("bob", []byte(msg))
			alice.log.Debugf("ALICE SENT MESSAGE to bob: %s", msg)
			if _, ok := <-aliceDeliveredChan; !ok {
				continue
			}
			if _, ok := <-bobReceivedChan; !ok {
				continue
			}
			i++
		}
	}()
	j := 0
	go func() {
		t.Logf("Bob has joined the chat")
		for {
			select {
			case <-haltCh:
				t.Log("haltCh closed!")
				wait.Done()
				return
			default:
			}

			msg := fmt.Sprintf("hi alice, it's the %d time i call you...", j)
			bob.SendMessage("alice", []byte(msg))
			bob.log.Debugf("BOB SENT MESSAGE to alice: %s", msg)
			if _, ok := <-bobDeliveredChan; !ok {
				continue
			}
			if _, ok := <-aliceReceivedChan; !ok {
				continue
			}
			j++
		}
	}()

	// start a timer that fires shortly before the testing framework will
	// terminate the test suite. It is recommended to pass a larger -timeout
	// value to thoroughly test message delivery
	go func() {
		deadline, ok := t.Deadline()
		if !ok { // called with -timeout 0, but do not run forever
			deadline = time.Now().Add(24 * time.Hour)
		}
		// when we will halt the test
		testTimeout := deadline.Sub(time.Now().Add(5 * time.Second))
		// the maximum delay between receiving a message or failing this test
		threshold := 5 * time.Minute
		timer := time.After(testTimeout)
		t.Logf("This test will run for %s", testTimeout)
		for {
			select {
			case <-timer:
				t.Log("Time is up!")
				close(haltCh)
				return
			case <-alice.HaltCh():
				t.Errorf("Client exited prematurely!")
				close(haltCh)
				return
			case <-bob.HaltCh():
				t.Errorf("Client exited prematurely!")
				close(haltCh)
				return
			case <-time.After(threshold):
				// require that both clients received a message within the last minute or fail
				now := time.Now()
				t.Logf("Alice last received %s ago", now.Sub(aliceLast))
				t.Logf("Bob last received %s ago", now.Sub(bobLast))
				if !assert.WithinDuration(now, bobLast, threshold) || !assert.WithinDuration(now, aliceLast, threshold) {
					t.Errorf("Failed to receive a message within %s", threshold)
					close(haltCh)
					return
				}
			}
		}
	}()

	wait.Wait()
	// assert at least some messages were received on both sides
	require.GreaterOrEqual(i, 1)
	require.GreaterOrEqual(j, 0)
	t.Log("Shutdown clients!")
	alice.Shutdown()
	bob.Shutdown()
	t.Log("Clients Shutdown!")
}

func TestDockerAddRemoveContact(t *testing.T) {
	require := require.New(t)

	a := createCatshadowClientWithState(t, createRandomStateFile(t), false)
	b := createCatshadowClientWithState(t, createRandomStateFile(t), false)

	s := [8]byte{}
	_, err := rand.Reader.Read(s[:])
	require.NoError(err)

	a.NewContact("b", s[:])
	b.NewContact("a", s[:])

loop1:
	for {
		ev := <-a.EventSink
		switch event := ev.(type) {
		case *KeyExchangeCompletedEvent:
			require.Nil(event.Err)
			break loop1
		default:
		}
	}

loop2:
	for {
		ev := <-b.EventSink
		switch event := ev.(type) {
		case *KeyExchangeCompletedEvent:
			require.Nil(event.Err)
			break loop2
		default:
		}
	}

	t.Log("Sending message to b")
	a.SendMessage("b", []byte{0})
loop3:
	for {
		ev := <-a.EventSink
		switch event := ev.(type) {
		case *MessageDeliveredEvent:
			t.Log("Message delivered to b")
			if event.Nickname == "b" {
				break loop3
			} else {
				t.Log(event)
			}
		default:
		}
	}

	t.Log("Sending message to a")
	b.SendMessage("a", []byte{0})

loop4:
	for {
		ev := <-b.EventSink
		switch event := ev.(type) {
		case *MessageDeliveredEvent:
			t.Log("Message delivered to a")
			if event.Nickname == "a" {
				break loop4
			}
		default:
		}
	}

	t.Log("Removing contact b")
	err = a.RemoveContact("b")
	require.NoError(err)
	require.Equal(len(a.GetContacts()), 0)

	t.Log("Removing contact b again, checking for err")
	err = a.RemoveContact("b")
	require.Error(err, errContactNotFound)

	c := a.GetConversation("b")
	require.Equal(len(c), 0)
	// verify that contact data is gone
	t.Log("Sending message to b, must fail")
	a.SendMessage("b", []byte("must fail"))
loop5:
	for {
		ev := <-a.EventSink
		switch event := ev.(type) {
		case *MessageNotSentEvent:
			if event.Nickname == "b" {
				break loop5
			}
		default:
		}
	}

	a.Shutdown()
	b.Shutdown()
}

func TestDockerRenameContact(t *testing.T) {
	require := require.New(t)

	a := createCatshadowClientWithState(t, createRandomStateFile(t), false)
	b := createCatshadowClientWithState(t, createRandomStateFile(t), false)

	s := [8]byte{}
	_, err := rand.Reader.Read(s[:])
	require.NoError(err)

	a.NewContact("b", s[:])
	b.NewContact("a", s[:])

loop1:
	for {
		ev := <-a.EventSink
		switch event := ev.(type) {
		case *KeyExchangeCompletedEvent:
			require.Nil(event.Err)
			break loop1
		default:
		}
	}

loop2:
	for {
		ev := <-b.EventSink
		switch event := ev.(type) {
		case *KeyExchangeCompletedEvent:
			require.Nil(event.Err)
			break loop2
		default:
		}
	}

	t.Log("Sending message to b")
	a.SendMessage("b", []byte{0})
loop3:
	for {
		ev := <-a.EventSink
		switch event := ev.(type) {
		case *MessageDeliveredEvent:
			t.Log("Message delivered to b")
			if event.Nickname == "b" {
				break loop3
			} else {
				t.Log(event)
			}
		default:
		}
	}

	t.Log("Sending message to a")
	b.SendMessage("a", []byte{0})

loop4:
	for {
		ev := <-b.EventSink
		switch event := ev.(type) {
		case *MessageDeliveredEvent:
			t.Log("Message delivered to a")
			if event.Nickname == "a" {
				break loop4
			}
		default:
		}
	}

	t.Log("Renaming contact b")
	err = a.RenameContact("b", "b2")
	require.NoError(err)

	c := a.GetConversation("b")
	require.Equal(len(c), 0)

	// verify that contact data is gone
	t.Log("Sending message to b, must fail")
	a.SendMessage("b", []byte("must fail"))

loop5:
	for {
		ev := <-a.EventSink
		switch event := ev.(type) {
		case *MessageNotSentEvent:
			if event.Nickname == "b" {
				break loop5
			}
		default:
		}
	}

	// send message to the renamed contact
	a.SendMessage("b2", []byte{0})
loop6:
	for {
		ev := <-a.EventSink
		switch event := ev.(type) {
		case *MessageDeliveredEvent:
			t.Log("Message delivered to b2")
			if event.Nickname == "b2" {
				break loop6
			} else {
				t.Log(event)
			}
		default:
		}
	}
loop7:
	for {
		ev := <-b.EventSink
		switch event := ev.(type) {
		case *MessageReceivedEvent:
			t.Log("Message received by b2")
			if event.Nickname == "a" {
				break loop7
			} else {
				t.Log(event)
			}
		default:
		}
	}

	// verify that b2 has received 2 messages
	c = b.GetConversation("a")
	require.Equal(len(c), 2)

	// clear conversation history
	b.WipeConversation("a")
	c = b.GetConversation("a")
	require.Equal(len(c), 0)

	a.Shutdown()
	b.Shutdown()
}
