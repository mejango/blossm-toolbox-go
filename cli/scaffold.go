package cli

import (
	"errors"
	"flag"
	"fmt"
	"go/format"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Scaffolds are executable adapters with required injected behavior, never successful business stubs.
var procedureTemplates = map[string]string{
	"command": `import (
 "context"
 "github.com/mejango/blossm-toolbox-go/command"
 )
 func New(store command.Appender, main func(context.Context, command.Request) (command.Result,error)) command.Procedure {
 return command.Procedure{Store:store,Main:main}
 }
 `,
	"fact": `import (
 "context"
 "encoding/json"
 "github.com/mejango/blossm-toolbox-go/fact"
 )
 func New(read func(context.Context,fact.Request)(json.RawMessage,error),stream func(context.Context,fact.Request,fact.Yield)error) fact.Procedure {
 return fact.Procedure{ReadFn:read,StreamFn:stream}
 }
 `,
	"job": `import("context";"github.com/mejango/blossm-toolbox-go/job")
 func New(run func(context.Context,job.Input)error) job.Procedure{return job.Procedure{Run:run}}
 `,
	"projection": `import("github.com/mejango/blossm-toolbox-go/projection")
 func New(aggregates projection.Aggregator, views projection.Writer, handlers map[projection.Source]projection.Handler)*projection.Engine{
 return &projection.Engine{Aggregates:aggregates,Views:views,Handlers:handlers}
 }
 func ReplayScheduler(roots projection.RootStream,producer projection.Producer)projection.Scheduler{return projection.Scheduler{Roots:roots,Producer:producer}}
 `,
	"event-store": `import("encoding/json";"errors";"github.com/mejango/blossm-toolbox-go/eventstore")
 type Reducer struct{NormalizePayload func(string,json.RawMessage)(json.RawMessage,error);Reduce func(json.RawMessage,eventstore.Event)(json.RawMessage,error)}
 func(r Reducer) Normalize(action string,payload json.RawMessage)(json.RawMessage,error){if r.NormalizePayload==nil{return nil,errors.New("payload validator is required")};return r.NormalizePayload(action,payload)}
 func(r Reducer) Apply(state json.RawMessage,event eventstore.Event)(json.RawMessage,error){if r.Reduce==nil{return nil,errors.New("deterministic reducer is required")};return r.Reduce(state,event)}
 `,
	"view-store": `import("github.com/mejango/blossm-toolbox-go/view";"github.com/mejango/blossm-toolbox-go/eventstore")
 func New(backend eventstore.Backend,scope view.Scope)*view.Store{return &view.Store{Backend:backend,Scope:scope}}
 `,
	"view-composite": `import("context";"github.com/mejango/blossm-toolbox-go/view")
 func Execute[Q,R any](ctx context.Context,scope view.Scope,query Q,readers map[string]view.ComponentReader,main view.CompositeMain[Q,R])(R,error){
 return view.ExecuteComposite(ctx,scope,query,readers,main)
 }
 `,
	"gateway": `import("context";"github.com/mejango/blossm-toolbox-go/serviceapp")
 // New composes the document command, facts, jobs, projections, views and identity HTTP API.
 // Supply separate identity/document backend namespaces and an exact internal verifier.
 // The caller owns backend closure, publication workers and HTTP server lifecycle.
 func New(ctx context.Context,config serviceapp.Config)(*serviceapp.App,error){return serviceapp.New(ctx,config)}
 `,
}

func scaffold(args []string, out, stderr io.Writer) error {
	f := flag.NewFlagSet("scaffold", flag.ContinueOnError)
	f.SetOutput(stderr)
	kind := f.String("kind", "command", "command|fact|job|projection|event-store|view-store|view-composite|gateway")
	name := f.String("name", "", "Go package name")
	root := f.String("dir", ".", "existing Go module directory")
	if err := parse(f, args); err != nil {
		return err
	}
	if !token.IsIdentifier(*name) || token.Lookup(*name).IsKeyword() || *name == "_" || strings.ContainsAny(*name, "/\\") {
		return errors.New("name must be a Go package identifier")
	}
	body, ok := procedureTemplates[*kind]
	if !ok {
		return errors.New("unknown procedure kind")
	}
	// Use the existing module; do not generate nested go.mod files.
	if _, err := os.Stat(filepath.Join(*root, "go.mod")); err != nil {
		return errors.New("dir must be the existing Go module root")
	}
	data, err := format.Source([]byte("package " + *name + "\n\n" + body))
	if err != nil {
		return err
	}
	dir := filepath.Join(*root, *name)
	if err = os.Mkdir(dir, 0755); err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			os.RemoveAll(dir)
		}
	}()
	if err = os.WriteFile(filepath.Join(dir, "procedure.go"), data, 0644); err != nil {
		return err
	}
	readme := fmt.Sprintf("# %s %s\n\nGenerated typed adapter in the existing module. Inject your application behavior and trusted\nauthorization context; nil behavior fails rather than reporting success. Register its handler\nin explicitly authorized application/Framework composition and run go test ./... before preparing deployment. No cloud resources were created.\n", *name, *kind)
	if err = os.WriteFile(filepath.Join(dir, "README.md"), []byte(readme), 0644); err != nil {
		return err
	}
	complete = true
	_, err = fmt.Fprintf(out, "Created %s (%s); wire the required application behavior before deployment.\n", dir, *kind)
	return err
}
