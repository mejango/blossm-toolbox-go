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
 "github.com/mejango/blossm-toolbox-go/procedures/command"
 )
 func New(store command.Appender, main func(context.Context, command.Request) (command.Result,error)) command.Procedure {
 return command.Procedure{Store:store,Main:main}
 }
 `,
	"fact": `import (
 "context"
 "encoding/json"
 "github.com/mejango/blossm-toolbox-go/procedures/fact"
 )
 func New(read func(context.Context,fact.Request)(json.RawMessage,error),stream func(context.Context,fact.Request,fact.Yield)error) fact.Procedure {
 return fact.Procedure{ReadFn:read,StreamFn:stream}
 }
 `,
	"job": `import("context";"github.com/mejango/blossm-toolbox-go/procedures/job")
 func New(run func(context.Context,job.Input)error) job.Procedure{return job.Procedure{Run:run}}
 `,
	"projection": `import("github.com/mejango/blossm-toolbox-go/procedures/projection")
 func New(aggregates projection.Aggregator, views projection.Writer, handlers map[projection.Source]projection.Handler)*projection.Engine{
 return &projection.Engine{Aggregates:aggregates,Views:views,Handlers:handlers}
 }
 func ReplayScheduler(roots projection.RootStream,producer projection.Producer)projection.Scheduler{return projection.Scheduler{Roots:roots,Producer:producer}}
 `,
	"event-store": `
import (
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
)
import (
	"encoding/json"
	"errors"
)

type Reducer struct {
	NormalizePayload func(string, json.RawMessage) (json.RawMessage, error)
	Reduce           func(json.RawMessage, eventlib.Event) (json.RawMessage, error)
}

func (r Reducer) Normalize(action string, payload json.RawMessage) (json.RawMessage, error) {
	if r.NormalizePayload == nil {
		return nil, errors.New("payload validator is required")
	}
	return r.NormalizePayload(action, payload)
}
func (r Reducer) Apply(state json.RawMessage, event eventlib.Event) (json.RawMessage, error) {
	if r.Reduce == nil {
		return nil, errors.New("deterministic reducer is required")
	}
	return r.Reduce(state, event)
}
`,
	"view-store": `
import (
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
)
import (
	"github.com/mejango/blossm-toolbox-go/procedures/view_stores/generic"
)

func New(backend eventlib.Backend, scope view.Scope) *view.Store {
	return &view.Store{Backend: backend, Scope: scope}
}
`,
	"view-composite": `import("context";"github.com/mejango/blossm-toolbox-go/procedures/view_stores/generic")
 func Execute[Q,R any](ctx context.Context,scope view.Scope,query Q,readers map[string]view.ComponentReader,main view.CompositeMain[Q,R])(R,error){
 return view.ExecuteComposite(ctx,scope,query,readers,main)
 }
 `,
	"gateway": `import("context";"net/http";"github.com/mejango/blossm-toolbox-go/procedures/gateways")
 // New exposes only explicitly configured routes and their authenticated checks.
 // The caller owns backend, worker and HTTP server lifecycles.
 func New(ctx context.Context,config transport.Framework)(http.Handler,error){return config.Handler(ctx)}
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
