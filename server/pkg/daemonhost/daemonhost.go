// Package daemonhost re-exports the parts of internal/daemon that an
// embedding host needs to run the Multica agent daemon in-process.
// internal/daemon cannot be imported from another module; this package can.
package daemonhost

import "github.com/multica-ai/multica/server/internal/daemon"

type (
	Config    = daemon.Config
	Overrides = daemon.Overrides
	Daemon    = daemon.Daemon // methods, including Run and RestartBinary, come with the alias
)

const (
	DefaultServerURL  = daemon.DefaultServerURL
	DefaultHealthPort = daemon.DefaultHealthPort
)

var (
	LoadConfig             = daemon.LoadConfig
	New                    = daemon.New
	NormalizeServerBaseURL = daemon.NormalizeServerBaseURL
)
