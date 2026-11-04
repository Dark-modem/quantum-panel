module github.com/Dark-modem/quantum-panel

go 1.23

require (
	github.com/creack/pty v1.1.24
	github.com/gorilla/websocket v1.5.3
	golang.org/x/crypto v0.31.0
	golang.org/x/sys v0.28.0 // indirect
)

require github.com/Dark-modem/quantum-protocol v0.0.0

replace github.com/Dark-modem/quantum-protocol => ../quantum-protocol
