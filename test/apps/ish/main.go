package main

import (
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"

	"github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/shutdown"
)

/*
ish: the Inexusable/Insecure/Internet SHell.
*/
func main() {
	defer shutdown.Exit()

	name := os.Getenv("NAME")
	port := os.Getenv("PORT")
	addr := ":" + port
	if name == "" {
		name = "ish-service"
	}

	l, err := net.Listen("tcp", addr)
	if err != nil {
		shutdown.Fatal(err)
	}
	defer l.Close()

	// User jobs do not get the cluster discoverd key. flynn-host registers
	// the port service. System jobs still register from the process.
	if os.Getenv("DISCOVERD_AUTH_KEY") != "" {
		hb, err := discoverd.AddServiceAndRegister(name, addr)
		if err != nil {
			// The listener is already up. A failed register must not exit
			// the process; flynn-host also publishes this port.
			log.Printf("discoverd register: %v", err)
		} else {
			shutdown.BeforeExit(func() { hb.Close() })
		}
	}

	http.HandleFunc("/ish", ish)
	if err := http.Serve(l, nil); err != nil {
		shutdown.Fatal(err)
	}
}

func ish(resp http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(resp, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	body, _ := ioutil.ReadAll(req.Body)

	cmd := exec.Command("/bin/sh", "-c", string(body)) // no bash in busybox
	cmd.Stdout = io.MultiWriter(resp, os.Stdout)
	cmd.Stderr = io.MultiWriter(resp, os.Stderr)
	cmd.Run()
}
