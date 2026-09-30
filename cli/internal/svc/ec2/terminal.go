package ec2

import (
	"encoding/json"
	"io"
	"net/http"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"golang.org/x/net/websocket"
)

// terminal attaches an interactive shell to a running instance over a
// WebSocket (the equivalent of EC2 Instance Connect). Client frames are JSON:
// {"t":"i","d":"<input>"} for keystrokes, {"t":"r","c":cols,"r":rows} to resize.
// Server frames carry raw terminal output.
func (s *Service) terminal(c *httpx.Ctx) (any, error) {
	i, err := s.get(c.Param("id"))
	if err != nil {
		return nil, err
	}
	if i.State != "running" {
		return nil, core.Errf(http.StatusConflict, "IncorrectInstanceState", "instance %s is %s", i.ID, i.State)
	}
	if i.IsVM() {
		return nil, errVMUnsupported("the browser terminal for")
	}
	c.MarkWritten()
	ws := websocket.Server{Handler: func(conn *websocket.Conn) {
		defer conn.Close()
		ex, err := s.env.Docker.C.CreateExec(docker.CreateExecOptions{
			Container: i.ContainerID, Tty: true, AttachStdin: true, AttachStdout: true, AttachStderr: true,
			Env: []string{"TERM=xterm-256color"},
			Cmd: []string{"/bin/sh", "-c", "cd ~ 2>/dev/null; if command -v bash >/dev/null; then exec bash -l; else exec sh -l; fi"},
		})
		if err != nil {
			_ = websocket.Message.Send(conn, "\r\n[homecloud] could not open a shell: "+err.Error()+"\r\n")
			return
		}
		pr, pw := io.Pipe()
		go func() {
			defer pw.Close()
			for {
				var raw string
				if err := websocket.Message.Receive(conn, &raw); err != nil {
					return
				}
				var m struct {
					T string `json:"t"`
					D string `json:"d"`
					C int    `json:"c"`
					R int    `json:"r"`
				}
				if json.Unmarshal([]byte(raw), &m) != nil {
					continue
				}
				switch m.T {
				case "i":
					if _, err := pw.Write([]byte(m.D)); err != nil {
						return
					}
				case "r":
					if m.C > 0 && m.R > 0 {
						_ = s.env.Docker.C.ResizeExecTTY(ex.ID, m.R, m.C)
					}
				}
			}
		}()
		out := wsWriter{conn}
		_ = s.env.Docker.C.StartExec(ex.ID, docker.StartExecOptions{
			InputStream: pr, OutputStream: out, ErrorStream: out, Tty: true, RawTerminal: true,
		})
		_ = websocket.Message.Send(conn, "\r\n[homecloud] session closed\r\n")
	}}
	ws.ServeHTTP(c.W, c.R)
	return nil, nil
}

type wsWriter struct{ c *websocket.Conn }

func (w wsWriter) Write(p []byte) (int, error) {
	if err := websocket.Message.Send(w.c, string(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}
