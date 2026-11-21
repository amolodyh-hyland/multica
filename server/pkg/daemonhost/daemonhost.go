// Package daemonhost re-exports the parts of internal/daemon that an
// embedding host needs to run the Multica agent daemon in-process.
// internal/daemon cannot be imported from another module; this package can.
package daemonhost

import (
	"github.com/multica-ai/multica/server/internal/daemon"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

type (
	Config    = daemon.Config
	Overrides = daemon.Overrides
	Daemon    = daemon.Daemon // methods, including Run and RestartBinary, come with the alias
)

const (
	DefaultServerURL  = daemon.DefaultServerURL
	DefaultHealthPort = daemon.DefaultHealthPort

	// PreparationHelperArg selects the execution-environment helper mode: the daemon
	// runs Prepare and Reuse in a subprocess of its own executable started with this
	// single argument, so the host binary must call RunPreparationHelper for it.
	PreparationHelperArg = execenv.PreparationHelperArg
)

var (
	LoadConfig             = daemon.LoadConfig
	New                    = daemon.New
	NormalizeServerBaseURL = daemon.NormalizeServerBaseURL
	RunPreparationHelper   = execenv.RunPreparationHelper
)
