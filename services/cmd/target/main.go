// target is a controllable HTTP "victim" for checks and chaos labs.
package main

import (
	"context"

	"pulse/internal/app"
	"pulse/internal/config"
	"pulse/internal/target"
)

func main() {
	env := config.FromOS()
	base := app.LoadBase(env, "target", ":8090")

	app.Run(env, base, func(ctx context.Context, a *app.App) error {
		target.New(a.Log).Register(a.HTTP)
		a.HTTP.SetStarted()
		<-ctx.Done()
		return nil // in-flight requests (slow mode) are drained by the HTTP shutdown
	})
}
