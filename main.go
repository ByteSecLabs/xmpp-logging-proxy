package main

import (
	"bytes"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/orivej/e"
	"github.com/pkg/errors"
)

type ReadResult struct {
	n   int
	err error
	buf []byte
}

const (
	bufSize         = 4000
	clientTLSMarker = "<starttls"
	serverTLSMarker = "<proceed"
)

var (
	flListenPort = flag.Int("port", 5222, "listen port")
	flServer     = flag.String("server", "<server:port>", "target server:port")

	flKeyPath  = flag.String("key", "", "path to TLS certificate key")
	flCertPath = flag.String("cert", "", "path to TLS certificate")

	flReplaceLocal  = flag.String("replace-local", "", "")
	flReplaceRemote = flag.String("replace-remote", "", "")
)

func main() {
	flag.Parse()

	certificate, err := tls.X509KeyPair([]byte(localhostCert), []byte(localhostKey))
	e.Exit(err)
	if len(*flCertPath) > 0 {
		certificate, err = tls.LoadX509KeyPair(*flCertPath, *flKeyPath)
		e.Exit(errors.Wrap(err, "can not load TLS key pair"))
	}

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", *flListenPort))
	e.Exit(err)

	for idx := 1; true; idx++ {
		client, err := listener.Accept()
		e.Exit(err)
		go func(idx int) {
			defer e.CloseOrPrint(client)
			err := serve(client, idx, certificate)
			fmt.Printf("X%d: %s\n", idx, err)
		}(idx)
	}
}

func serve(client net.Conn, idx int, certificate tls.Certificate) error {
	server, err := net.Dial("tcp", *flServer)
	if err != nil {
		return errors.Wrap(err, "failed to connect to server")
	}
	defer e.CloseOrPrint(server)

	err = proxy(client, server, idx, true)
	if err != nil {
		return errors.Wrap(err, "session terminated")
	}

	tlsClient := tls.Server(client, &tls.Config{
		Certificates: []tls.Certificate{certificate},
	})
	tlsServer := tls.Client(server, &tls.Config{
		InsecureSkipVerify: true,
	})
	fmt.Printf("X%d: eavesdropping TLS\n", idx)
	err = proxy(tlsClient, tlsServer, idx, false)
	return errors.Wrap(err, "TLS session terminated")
}

func proxy(client, server io.ReadWriter, idx int, untilTLS bool) error {
	clientNext, clientResults := startReader(client)
	serverNext, serverResults := startReader(server)

	rLocal := []byte(*flReplaceLocal)
	rRemote := []byte(*flReplaceRemote)
	replace := len(rLocal) > 0 && len(rRemote) > 0

	for {
		select {
		case result := <-clientResults:
			err := result.err
			if err != nil {
				return errors.Wrap(err, "failed to read from client")
			}

			buf := result.buf[:result.n]
			if replace {
				buf = bytes.Replace(buf, rLocal, rRemote, -1)
			}
			fmt.Printf("C%d|%s|\n", idx, escape(string(buf)))

			_, err = server.Write(buf)
			if err != nil {
				return errors.Wrap(err, "failed to write to server")
			}

			if untilTLS && bytes.Contains(buf, []byte(clientTLSMarker)) {
				fmt.Printf("X%d: client goes TLS\n", idx)
				clientNext <- false
				continue
			}

			clientNext <- true

		case result := <-serverResults:
			err := result.err
			if err != nil {
				return errors.Wrap(err, "failed to read from server")
			}

			buf := result.buf[:result.n]
			fmt.Printf("S%d|%s|\n", idx, escape(string(buf)))
			if replace {
				buf = bytes.Replace(buf, rRemote, rLocal, -1)
			}

			_, err = client.Write(buf)
			if err != nil {
				return errors.Wrap(err, "failed to write to client")
			}

			if untilTLS && bytes.Contains(buf, []byte(serverTLSMarker)) {
				fmt.Printf("X%d: server goes TLS\n", idx)
				serverNext <- false
				return nil
			}

			serverNext <- true
		}
	}
}

func startReader(reader io.Reader) (chan<- bool, chan ReadResult) {
	readNext := make(chan bool)
	buf := make([]byte, bufSize)
	results := make(chan ReadResult)
	go func() {
		for {
			n, err := reader.Read(buf)
			results <- ReadResult{n, err, buf}
			if !<-readNext {
				return
			}
		}
	}()
	return readNext, results
}

func escape(s string) string {
	s = strings.Replace(s, "\\", "\\\\", -1)
	s = strings.Replace(s, "\n", "\\n", -1)
	return s
}
