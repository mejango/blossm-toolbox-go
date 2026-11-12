package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// Run defaults to offline preparation. Provider/HTTP execution requires --execute.
func Run(ctx context.Context, args []string, out, stderr io.Writer) error {
	return RunWith(ctx, args, out, stderr, Execution{})
}

// RunWith permits isolated provider/HTTP fixtures without ambient command fallback.
func RunWith(ctx context.Context, args []string, out, stderr io.Writer, execution Execution) error {
	if len(args) == 0 {
		return errors.New("usage: blossm init|scaffold|validate|generate|plan|deploy|token|internal-token|job|replay|roles|secret|queue|test|build|version (see cli/README.md)")
	}
	switch args[0] {
	case "internal-token":
		return internalToken(ctx, args[1:], out, stderr, execution)
	case "token":
		return developerToken(ctx, args[1:], out, stderr, execution)
	case "deploy", "roles", "secret", "queue", "job", "replay":
		return runWorkflow(ctx, args, out, stderr, execution)
	case "scaffold":
		return scaffold(args[1:], out, stderr)
	}
	command := args[0]
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(stderr)
	config := f.String("config", "blossm.json", "strict JSON configuration")
	switch command {
	case "version":
		if len(args) != 1 {
			return errors.New("version accepts no arguments")
		}
		_, err := fmt.Fprintln(out, "blossm config-v2")
		return err
	case "init":
		provider := f.String("provider", "gcp", "gcp or aws")
		driver := f.String("database", "postgres", "postgres or mongo")
		if err := parse(f, args[1:]); err != nil {
			return err
		}
		c := Example(*provider, *driver)
		if err := c.Validate(); err != nil {
			return err
		}
		data, err := encode(c)
		if err != nil {
			return err
		}
		file, err := os.OpenFile(*config, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		if _, err = file.Write(data); err != nil {
			file.Close()
			os.Remove(*config)
			return err
		}
		if err = file.Close(); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "Created %s with illustrative resource identifiers; review before generation.\n", *config)
		return err
	case "validate", "generate", "plan":
		dir := f.String("out", "deploy/generated", "new output directory (generate only)")
		dry := f.Bool("dry-run", false, "prepare offline only")
		if err := parse(f, args[1:]); err != nil {
			return err
		}

		if *dry {
			return errors.New("use plan or deploy --dry-run")
		}
		c, err := ReadConfig(*config)
		if err != nil {
			return err
		}
		if command == "validate" {
			_, err = fmt.Fprintln(out, "Configuration valid; resource existence is unverified.")
			return err
		}
		p, err := Prepare(c)
		if err != nil {
			return err
		}
		if command == "generate" {
			if err = WritePlan(*dir, p); err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "Prepared offline artifacts in %s; nothing deployed.\n", *dir)
			return err
		}
		b, err := encode(p)
		if err != nil {
			return err
		}
		_, err = out.Write(b)
		return err
	case "test", "build":
		dir := f.String("dir", ".", "Go module directory")
		race := f.Bool("race", false, "test with race detector")
		integration := f.Bool("integration", false, "test tagged native database suites; requires both URLs")
		dest := f.String("out", "bin", "build output directory")
		if err := parse(f, args[1:]); err != nil {
			return err
		}
		if command == "build" && (*race || *integration) {
			return errors.New("race and integration are test options")
		}
		if command == "test" {
			if *integration && (os.Getenv("POSTGRES_URL") == "" || os.Getenv("MONGODB_URI") == "") {
				return errors.New("native integration tests require POSTGRES_URL and MONGODB_URI")
			}
			a := []string{"test", "-mod=readonly", "-count=1"}
			if *race {
				a = append(a, "-race")
			}
			if *integration {
				a = append(a, "-tags=integration")
			}
			a = append(a, "./...")
			return runGo(ctx, *dir, a, out, stderr)
		}
		configPath := *config
		if !filepath.IsAbs(configPath) {
			configPath = filepath.Join(*dir, configPath)
		}
		c, err := ReadConfig(configPath)
		if err != nil {
			return err
		}
		output := *dest
		if !filepath.IsAbs(output) {
			output = filepath.Join(*dir, output)
		}
		abs, err := filepath.Abs(output)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(abs, 0755); err != nil {
			return err
		}
		return runGo(ctx, *dir, []string{"build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-o", filepath.Join(abs, c.Application.Binary), c.Application.Package}, out, stderr)

	default:
		return fmt.Errorf("unknown command %q", command)
	}
}
func parse(f *flag.FlagSet, args []string) error {
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", f.Args())
	}
	return nil
}
func runGo(ctx context.Context, dir string, args []string, out, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.Stderr = stderr
	// No toolchain or dependency download is permitted by the offline CLI.
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOWORK=off")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go %s failed: %w", args[0], err)
	}
	return nil
}
