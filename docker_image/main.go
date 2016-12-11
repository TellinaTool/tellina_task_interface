package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/kr/pty"
)

// Stdout will be sent in outgoing WebSocket messages every StdoutMessageLimit.
const StdoutMessageLimit time.Duration = 100 * time.Millisecond

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}
var dockerHostIP string

func main() {
	out, err := exec.Command("/bin/bash", "-c", `route -n | awk '/UG[ \t]/{print $2}'`).Output()
	if err != nil {
		log.Fatalln("Failed to get Docker host IP:", err)
	}
	dockerHostIP = strings.TrimSpace(string(out))
	StartServer(10411)
}

func StartServer(port int) {
	r := mux.NewRouter()
	r.HandleFunc("/{container_id}", XtermHandler)
	log.Printf("Starting server on port %d...\n", port)
	log.Fatalln(http.ListenAndServe(fmt.Sprintf(":%d", port), r))
}

func XtermHandler(response http.ResponseWriter, request *http.Request) {
	container_id := mux.Vars(request)["container_id"]

	file, err := pty.Start(exec.Command("/bin/bash", "--login"))
	if err != nil {
		log.Fatalln("Failed to start pseudo-terminal:", err)
	}

	// wait for terminal session to start
	// TODO(kvu787): find a better way
	time.Sleep(250 * time.Millisecond)

	// Open connection to task interface server
	url := fmt.Sprintf("ws://%s:10411/container/%s", dockerHostIP, container_id)
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		log.Fatalf("Could not open websocket to %v: %v\n", url, err)
	}

	// user -> container
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				log.Println("user -> container read msg error:", err)
				break
			}
			_, err = io.Copy(file, bytes.NewReader(data))
			if err != nil {
				log.Println("user -> container write msg error:", err)
				break
			}
		}
	}()

	// container -> user
	go func() {
		reader := bufio.NewReader(file)
		lock := &sync.Mutex{}
		buffer := &bytes.Buffer{}

		go func() {
			tickC := time.Tick(StdoutMessageLimit)
			for {
				<-tickC
				var err error
				lock.Lock()
				if buffer.Len() > 0 {
					err = conn.WriteMessage(websocket.TextMessage, buffer.Bytes())
					buffer.Reset()
					if err != nil {
						lock.Unlock()
						log.Println("user -> container write msg error:", err)
						return
					}
				}
				lock.Unlock()
			}
		}()

		for {
			b, err := reader.ReadByte()
			if err != nil {
				if err == io.EOF {
					break
				} else {
					log.Fatalln("pty stdout read error:", err)
				}
			}
			lock.Lock()
			buffer.WriteByte(b)
			lock.Unlock()
		}
	}()
}