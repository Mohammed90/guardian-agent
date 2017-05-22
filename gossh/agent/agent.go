package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/user"
	"path/filepath"

	"path"

	"github.com/dimakogan/ssh/gossh/common"
	"github.com/hashicorp/yamux"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

type policyStore map[ssh.PolicyKey]ssh.PolicyScope

func proxySSH(toClient net.Conn, toServer net.Conn, control net.Conn, pc *ssh.Policy) error {
	var auths []ssh.AuthMethod
	aconn, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK"))
	if err != nil {
		return err
	}

	auths = append(auths, ssh.PublicKeysCallback(agent.NewClient(aconn).Signers))

	if err != nil {
		return err
	}

	curuser, err := user.Current()
	if err != nil {
		log.Fatalf("Failed to get current user: %s", err)
	}
	kh, err := knownhosts.New(path.Join(curuser.HomeDir, ".ssh", "known_hosts"))
	if err != nil {
		return err
	}
	clientConfig := &ssh.ClientConfig{
		User:            pc.User,
		HostKeyCallback: kh,
		Auth:            auths,
	}

	meteredConnToServer := common.CustomConn{Conn: toServer}
	proxy, err := ssh.NewProxyConn(pc.Server, toClient, &meteredConnToServer, clientConfig, pc.FilterClientPacket, pc.FilterServerPacket)
	if err != nil {
		return err
	}
	err = proxy.UpdateClientSessionParams()
	if err != nil {
		return err
	}

	done := proxy.Run()

	err = <-done
	if err != nil {
		return err
	}

	handshakeCompletedMsg := common.HandoffCompleteMessage{
		NextTransportByte: uint32(meteredConnToServer.BytesRead() - proxy.BufferedFromServer()),
	}
	packet := ssh.Marshal(handshakeCompletedMsg)
	return common.WriteControlPacket(control, common.MsgHandoffComplete, packet)
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	var cport int
	flag.IntVar(&cport, "l", 2345, "Proxy port to listen on.")

	curuser, err := user.Current()
	if err != nil {
		log.Fatalf("Failed to get current user: %s", err)
	}
	var knownHosts string
	flag.StringVar(&knownHosts, "known_hosts", filepath.Join(curuser.HomeDir, ".ssh/known_hosts"), "known hosts to verify against")

	flag.Parse()

	masterListener, err := net.Listen("tcp", fmt.Sprintf(":%d", cport))
	if err != nil {
		log.Fatalf("Failed to listen on control port %d: %s", cport, err)
	}
	defer masterListener.Close()

	// can and should be refactored if we do one agent in all rather than one per connection
	// Similarly, if we choose to enable a mode to remember per command approval (rather than all commands)
	// should make it map to an array of commands, with a wildcard to signify all.
	store := make(policyStore)

	for {
		master, err := masterListener.Accept()
		if err != nil {
			log.Printf("Failed to accept connection: %s", err)
			continue
		}
		handleConnection(master, store)
	}
}

func policyInStore(store policyStore, policy ssh.Policy) bool{
	scope, ok := store[policy.GetPolicyKey()]
	if ok {
		if scope.AllCommands {
			return true
		}
		for _, v := range scope.Commands {
			if policy.Command == v {
				return true
			}
		}
	}
	return false
}

func handleConnection(master net.Conn, store policyStore) {
	log.Printf("New incoming connection from %s", master.RemoteAddr())

	ymux, err := yamux.Server(master, nil)
	if err != nil {
		log.Printf("Failed to start ymux: %s", err)
		master.Close()
		return
	}
	defer ymux.Close()

	control, err := ymux.Accept()
	if err != nil {
		log.Printf("Failed to accept control stream: %s", err)
		return
	}
	defer control.Close()

	msgNum, payload, err := common.ReadControlPacket(control)
	if msgNum != common.MsgExecutionRequest {
		log.Printf("Unexpected control message: %d (expecting MsgExecutionRequest)", msgNum)
		return
	}
	execReq := new(common.ExecutionRequestMessage)
	if err = ssh.Unmarshal(payload, execReq); err != nil {
		log.Printf("Failed to unmarshal ExecutionRequestMessage: %s", err)
		return
	}

	policy := ssh.NewPolicy(execReq.User, execReq.Command, execReq.Server)

	if !policyInStore(store, *policy) {
		err = policy.AskForApproval(store)
		if err != nil {
			log.Printf("Request denied: %s", err)
			common.WriteControlPacket(control, common.MsgExecutionDenied, []byte{})
			return
		}
	}
	common.WriteControlPacket(control, common.MsgExecutionApproved, []byte{})

	sshData, err := ymux.Accept()
	if err != nil {
		log.Printf("Failed to accept data stream: %s", err)
		return
	}
	defer sshData.Close()

	transport, err := ymux.Accept()
	if err != nil {
		log.Printf("Failed to get transport stream: %s", err)
		return
	}
	defer transport.Close()

	err = proxySSH(sshData, transport, control, policy)
	transport.Close()
	sshData.Close()
	control.Close()
	// Wait for client to close master connection
	ioutil.ReadAll(master)

	if err == nil {
		log.Printf("Session complete OK")
	} else {
		log.Printf("Proxy session finished with error: %s", err)
	}
}
