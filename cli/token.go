package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// developerToken acquires provider identity, not application privileges. Preparation
// never discovers credentials. Only explicit execution can invoke gcloud.
func developerToken(ctx context.Context, args []string, out, stderr io.Writer, e Execution) error {
	f := flag.NewFlagSet("token", flag.ContinueOnError)
	f.SetOutput(stderr)
	config := f.String("config", "blossm.json", "strict provider configuration")
	audience := f.String("audience", "", "single intended GCP token recipient")
	account := f.String("impersonate-service-account", "", "explicit optional service account; requires existing impersonation authority")
	execute := f.Bool("execute", false, "acquire token and write sensitive token to stdout")
	dry := f.Bool("dry-run", false, "prepare offline only (default)")
	if err := parse(f, args); err != nil {
		return err
	}
	if *execute && *dry {
		return errors.New("execute and dry-run are mutually exclusive")
	}
	c, err := ReadConfig(*config)
	if err != nil {
		return err
	}
	if c.Provider == "aws" {
		return errors.New("AWS service authentication uses cloud/aws.ServiceAuth.Sign with explicit credentials, region and service (SigV4); no bearer token is minted")
	}
	if *audience == "" || strings.ContainsAny(*audience, " ,\t\r\n\x00") {
		return errors.New("one nonempty audience without whitespace or commas is required")
	}
	argv := []string{"gcloud", "auth", "print-identity-token", "--audiences=" + *audience, "--project=" + c.GCP.Project, "--quiet"}
	if *account != "" {
		if !strings.HasSuffix(*account, ".iam.gserviceaccount.com") || strings.ContainsAny(*account, " \t\r\n\x00") || !strings.Contains(*account, "@") {
			return errors.New("invalid impersonated service account")
		}
		argv = append(argv, "--impersonate-service-account="+*account)
	}
	if !*execute {
		return printPlan(out, WorkflowPlan{Status: "prepared-only", Operation: "token", Actions: []Action{{Args: argv}}, Note: "Explicit execution prints a sensitive GCP identity token. Existing gcloud credentials and audience/impersonation authority are required; no IAM grants are created. This does not mint an application JWT."})
	}
	raw, err := e.run(ctx, Invocation{Args: argv})
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || len(token) > 65536 || strings.ContainsAny(token, " \t\r\n\x00") {
		return errors.New("provider returned empty or invalid token output")
	}
	_, err = fmt.Fprintln(out, token)
	return err
}
