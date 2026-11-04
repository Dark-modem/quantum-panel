// Command quantum-panel is the Quantum controller binary (the panel
// formerly known as gre-panel). It is management-only: it never sits in
// the tunnel data path, and tunnels keep running if this process dies.
// The implementation lives in internal/panel.
package main

import (
	"os"

	"github.com/Dark-modem/quantum-panel/internal/panel"
)

func main() {
	os.Exit(panel.Main(os.Args[1:]))
}
