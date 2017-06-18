package sshfwd

import (
	"bufio"
	"io"
	"log"
	"os"
	"os/exec"
	"path"

	"golang.org/x/crypto/ssh"

	"net"

	"fmt"

	"io/ioutil"

	"github.com/dimakogan/ssh/gossh/common"
)

const debugSSHFwd = true

type SSHFwd struct {
	SSHCmd         string
	SSHArgs        []string
	Host           string
	Port           int
	Username       string
	RemoteStubName string

	localSocket  string
	remoteSocket string
	listener     net.Listener
}

func (fwd *SSHFwd) SetupForwarding() error {
	fwd.SSHArgs = append(fwd.SSHArgs,
		fmt.Sprintf("-p %d", fwd.Port),
		"-S", path.Join(common.UserTempDir(), "%C.master"),
		fmt.Sprintf("%s@%s", fwd.Username, fwd.Host))
	remoteStub := exec.Command(fwd.SSHCmd, append(fwd.SSHArgs, "-M", fwd.RemoteStubName)...)
	remoteStdErr, err := remoteStub.StderrPipe()
	if err != nil {
		return fmt.Errorf("Failed to get ssh stderr: %s", err)
	}
	remoteStdOut, err := remoteStub.StdoutPipe()
	if err != nil {
		return fmt.Errorf("Failed to get ssh stdout: %s", err)
	}
	remoteStdIn, err := remoteStub.StdinPipe()
	if err != nil {
		return fmt.Errorf("Failed to get ssh stdin: %s", err)
	}

	err = remoteStub.Start()
	if err != nil {
		var stdErr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stdErr = ee.Stderr
		}
		os.Stderr.Write(stdErr)
		fullStdErr, _ := ioutil.ReadAll(remoteStdErr)
		return fmt.Errorf("Failed to run %s %s: %s\n%s", remoteStub.Path, remoteStub.Args, err, fullStdErr)
	}

	go io.Copy(os.Stderr, remoteStdErr)
	stubReader := bufio.NewReader(remoteStdOut)
	remoteSocket, _, err := stubReader.ReadLine()
	if err != nil {
		allErr, _ := ioutil.ReadAll(remoteStdErr)
		return fmt.Errorf("Failed to read remote socket path from stub: %s\n%s", err, allErr)
	}

	listener, bindAddr, err := common.CreateSocket("")
	if err != nil {
		return fmt.Errorf("Failed to listen on socket %s: %s", bindAddr, err)
	}
	log.Printf("Listening on: %s", bindAddr)

	fwd.localSocket = bindAddr
	fwd.remoteSocket = string(remoteSocket)
	fwd.listener = listener

	child := exec.Command(fwd.SSHCmd,
		append(fwd.SSHArgs, "-o ExitOnForwardFailure yes", "-T", "-O", "forward", fmt.Sprintf("-R %s:%s", string(remoteSocket), bindAddr))...)
	_, err = child.Output()
	if err != nil {
		var stdErr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stdErr = ee.Stderr
		}
		return fmt.Errorf("Failed to run SSH forwarding: %s\n%s", err, stdErr)
	}

	_, err = fmt.Fprintln(remoteStdIn, "start")
	if err != nil {
		return fmt.Errorf("Failed to ack forwarding: %s", err)
	}
	return nil
}

func (fwd *SSHFwd) Run(cmd string) error {
	if cmd == "" {
		fwd.SSHArgs = append(fwd.SSHArgs, "-t")
	} else {
		fwd.SSHArgs = append(fwd.SSHArgs, cmd)
	}
	for _, s := range fwd.SSHArgs {
		log.Printf(s)
	}
	child := exec.Command(fwd.SSHCmd, fwd.SSHArgs...)

	child.Stderr = os.Stderr
	child.Stdout = os.Stdout
	child.Stdin = os.Stdin

	return child.Run()
}

func (fwd *SSHFwd) Accept() (net.Conn, error) {
	client, err := fwd.listener.Accept()
	if err != nil {
		return nil, err
	}
	clientPipe, agentPipe := net.Pipe()
	go func() {
		io.Copy(client, clientPipe)
		client.Close()
	}()
	go func() {
		msg := common.AgentForwardingNoticeMsg{Hostname: fwd.Host, Port: uint32(fwd.Port), Username: fwd.Username}
		if err = common.WriteControlPacket(clientPipe, common.MsgAgentForwardingNotice, ssh.Marshal(msg)); err != nil {
			log.Printf("Failed to send message to agent: %s", err)
			return
		}
		io.Copy(clientPipe, client)
		if debugSSHFwd {
			log.Printf("Finished copying from client to real agent.")
		}
		clientPipe.Close()
	}()

	return agentPipe, nil
}

func (fwd *SSHFwd) Close() {
	child := exec.Command(fwd.SSHCmd, append(fwd.SSHArgs, "-O exit")...)
	child.Run()
	os.Remove(fwd.localSocket)
	fwd.listener.Close()
}
