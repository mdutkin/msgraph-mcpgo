//go:build !devconsole

package main

import (
	"net/http"

	"github.com/fnfbraga/msgraph-mcpgo/internal/config"
	"github.com/rs/zerolog"
)

// registerDevConsole is a no-op in a release build.
//
// The development console and its Microsoft Entra device-code proxy are
// excluded by build tag rather than by a runtime check. A runtime check still
// ships the handlers inside the artifact, where a configuration mistake, an
// environment variable set wrongly in a task definition, or a later refactor
// can expose them. Excluding them at compile time means the code is not in the
// production binary at all, and no deployment error can bring it back.
func registerDevConsole(_ *http.ServeMux, _ *config.Config, logger *zerolog.Logger) {
	logger.Debug().Msg("Development console excluded from this build")
}
