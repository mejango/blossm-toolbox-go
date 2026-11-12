# Dia runtime

`Run(context.Context)` is the single owning implementation for the Dia HTTP
application lifecycle, database/signing clients, publication workers and optional
wallet RPC clients. Configuration is documented in ../deploy/README.md. Context
cancellation shuts down and joins workers before clients close. `LogStopped`
reports only controlled lifecycle metadata and never serializes provider causes.

Both this module's reference cmd/dia and github.com/mejango/dia-go call this
package. The export is a behavior-preserving extraction of the verified cmd/dia;
there is no second application runtime implementation.
