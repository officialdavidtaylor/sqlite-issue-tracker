// Package cli implements the sit command-line interface.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/officialdavidtaylor/sqlite-issue-tracker/internal/identity"
	"github.com/officialdavidtaylor/sqlite-issue-tracker/internal/issue"
	"github.com/officialdavidtaylor/sqlite-issue-tracker/internal/repository"
	storage "github.com/officialdavidtaylor/sqlite-issue-tracker/internal/storage/sqlite"
)

const usage = `sit - local SQLite issue tracking

Usage:
  sit [--db PATH] <command> [arguments]

Commands:
  init                         initialize the shared database
  create [flags]               create an issue
  get ID [--json]              show an issue
  list [flags]                 list issues
  update ID [flags]            update using an expected revision
  delete ID [flags]            soft-delete using an expected revision
  link SOURCE TARGET [flags]   add a directed relationship
  unlink SOURCE TARGET [flags] remove a directed relationship
  links ID [flags]             list relationships touching an issue
  history [ID] [--json]        show the audit log
  hash                         print logical state and history hashes
  snapshot [flags]             export a versionable snapshot and manifest
  verify                       run SQLite integrity_check

Global environment:
  SIT_DB       override the live database path
  SIT_ACTOR    default audit actor
`

// Run executes the CLI and returns any user-facing error.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	global := flag.NewFlagSet("sit", flag.ContinueOnError)
	global.SetOutput(stderr)
	databaseFlag := global.String("db", os.Getenv("SIT_DB"), "live SQLite database path")
	global.Usage = func() { _, _ = io.WriteString(stderr, usage) }
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	remaining := global.Args()
	if len(remaining) == 0 {
		_, _ = io.WriteString(stdout, usage)
		return nil
	}
	if remaining[0] == "help" || remaining[0] == "--help" || remaining[0] == "-h" {
		_, _ = io.WriteString(stdout, usage)
		return nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	paths, err := repository.Discover(ctx, cwd)
	if err != nil {
		return err
	}
	databasePath := *databaseFlag
	if databasePath == "" {
		databasePath = paths.Database
	}
	store, err := storage.Open(ctx, databasePath)
	if err != nil {
		return err
	}
	defer store.Close()

	command, commandArgs := remaining[0], remaining[1:]
	switch command {
	case "init":
		if len(commandArgs) != 0 {
			return errors.New("init accepts no arguments")
		}
		fmt.Fprintf(stdout, "initialized %s\n", databasePath)
		return nil
	case "create":
		return runCreate(ctx, store, commandArgs, stdout, stderr)
	case "get":
		return runGet(ctx, store, commandArgs, stdout, stderr)
	case "list":
		return runList(ctx, store, commandArgs, stdout, stderr)
	case "update":
		return runUpdate(ctx, store, commandArgs, stdout, stderr)
	case "delete":
		return runDelete(ctx, store, commandArgs, stdout, stderr)
	case "link":
		return runLink(ctx, store, commandArgs, stdout, stderr, false)
	case "unlink":
		return runLink(ctx, store, commandArgs, stdout, stderr, true)
	case "links":
		return runLinks(ctx, store, commandArgs, stdout, stderr)
	case "history":
		return runHistory(ctx, store, commandArgs, stdout, stderr)
	case "hash":
		if len(commandArgs) != 0 {
			return errors.New("hash accepts no arguments")
		}
		hashes, err := store.Hashes(ctx)
		if err != nil {
			return err
		}
		return writeJSON(stdout, hashes)
	case "snapshot":
		return runSnapshot(ctx, store, paths, commandArgs, stdout, stderr)
	case "verify":
		if len(commandArgs) != 0 {
			return errors.New("verify accepts no arguments")
		}
		if err := store.IntegrityCheck(ctx); err != nil {
			return err
		}
		_, err := fmt.Fprintln(stdout, "ok")
		return err
	default:
		return fmt.Errorf("unknown command %q\n\n%s", command, usage)
	}
}

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return flags
}

func defaultActor() string {
	if actor := strings.TrimSpace(os.Getenv("SIT_ACTOR")); actor != "" {
		return actor
	}
	return "unknown"
}

func mutationID(value string) (string, error) {
	if strings.TrimSpace(value) != "" {
		return value, nil
	}
	return identity.New("mut_")
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printIssue(output io.Writer, value issue.Issue, asJSON bool) error {
	if asJSON {
		return writeJSON(output, value)
	}
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintf(writer, "ID:\t%s\nTitle:\t%s\nStatus:\t%s\nRevision:\t%d\nCreated:\t%s\nUpdated:\t%s\n", value.ID, value.Title, value.Status, value.Revision, value.CreatedAt.Format("2006-01-02T15:04:05Z07:00"), value.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"))
	if value.DeletedAt != nil {
		fmt.Fprintf(writer, "Deleted:\t%s\n", value.DeletedAt.Format("2006-01-02T15:04:05Z07:00"))
	}
	if value.Body != "" {
		fmt.Fprintf(writer, "Body:\t%s\n", value.Body)
	}
	return writer.Flush()
}

func runCreate(ctx context.Context, store *storage.Store, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("create", stderr)
	id := flags.String("id", "", "issue ID (generated when omitted)")
	title := flags.String("title", "", "issue title")
	body := flags.String("body", "", "issue body")
	status := flags.String("status", "open", "open, in_progress, blocked, or closed")
	actor := flags.String("actor", defaultActor(), "audit actor")
	mutation := flags.String("mutation-id", "", "idempotency key")
	asJSON := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("create accepts flags only")
	}
	if *id == "" {
		generated, err := identity.NewIssueID()
		if err != nil {
			return err
		}
		*id = generated
	}
	mutationValue, err := mutationID(*mutation)
	if err != nil {
		return err
	}
	created, err := store.CreateIssue(ctx, issue.CreateParams{MutationID: mutationValue, ID: *id, Title: *title, Body: *body, Status: *status, Actor: *actor})
	if err != nil {
		return err
	}
	return printIssue(stdout, created, *asJSON)
}

func splitLeading(args []string, count int, command string) ([]string, []string, error) {
	if len(args) < count {
		return nil, nil, fmt.Errorf("%s requires %d positional argument(s)", command, count)
	}
	positionals := args[:count]
	for _, value := range positionals {
		if strings.HasPrefix(value, "-") {
			return nil, nil, fmt.Errorf("%s positional arguments must precede flags", command)
		}
	}
	return positionals, args[count:], nil
}

func runGet(ctx context.Context, store *storage.Store, args []string, stdout, stderr io.Writer) error {
	positionals, rest, err := splitLeading(args, 1, "get")
	if err != nil {
		return err
	}
	flags := newFlags("get", stderr)
	asJSON := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(rest); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected get arguments")
	}
	value, err := store.GetIssue(ctx, positionals[0])
	if err != nil {
		return err
	}
	return printIssue(stdout, value, *asJSON)
}

func runList(ctx context.Context, store *storage.Store, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("list", stderr)
	status := flags.String("status", "", "filter by status")
	all := flags.Bool("all", false, "include deleted issues")
	asJSON := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("list accepts flags only")
	}
	items, err := store.ListIssues(ctx, issue.ListOptions{Status: *status, IncludeDeleted: *all})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(stdout, items)
	}
	writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tSTATUS\tREV\tTITLE")
	for _, item := range items {
		deleted := ""
		if item.DeletedAt != nil {
			deleted = " (deleted)"
		}
		fmt.Fprintf(writer, "%s\t%s%s\t%d\t%s\n", item.ID, item.Status, deleted, item.Revision, item.Title)
	}
	return writer.Flush()
}

type optionalString struct {
	value string
	set   bool
}

func (s *optionalString) String() string { return s.value }
func (s *optionalString) Set(value string) error {
	s.value, s.set = value, true
	return nil
}

func stringPointer(value optionalString) *string {
	if !value.set {
		return nil
	}
	return &value.value
}

func runUpdate(ctx context.Context, store *storage.Store, args []string, stdout, stderr io.Writer) error {
	positionals, rest, err := splitLeading(args, 1, "update")
	if err != nil {
		return err
	}
	flags := newFlags("update", stderr)
	var title, body, status optionalString
	flags.Var(&title, "title", "new title")
	flags.Var(&body, "body", "new body")
	flags.Var(&status, "status", "new status")
	expected := flags.Int64("expected-revision", 0, "required current revision")
	actor := flags.String("actor", defaultActor(), "audit actor")
	mutation := flags.String("mutation-id", "", "idempotency key")
	asJSON := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(rest); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected update arguments")
	}
	mutationValue, err := mutationID(*mutation)
	if err != nil {
		return err
	}
	updated, err := store.UpdateIssue(ctx, issue.UpdateParams{MutationID: mutationValue, ID: positionals[0], Title: stringPointer(title), Body: stringPointer(body), Status: stringPointer(status), ExpectedRevision: *expected, Actor: *actor})
	if err != nil {
		return err
	}
	return printIssue(stdout, updated, *asJSON)
}

func runDelete(ctx context.Context, store *storage.Store, args []string, stdout, stderr io.Writer) error {
	positionals, rest, err := splitLeading(args, 1, "delete")
	if err != nil {
		return err
	}
	flags := newFlags("delete", stderr)
	expected := flags.Int64("expected-revision", 0, "required current revision")
	actor := flags.String("actor", defaultActor(), "audit actor")
	mutation := flags.String("mutation-id", "", "idempotency key")
	asJSON := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(rest); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected delete arguments")
	}
	mutationValue, err := mutationID(*mutation)
	if err != nil {
		return err
	}
	deleted, err := store.DeleteIssue(ctx, issue.DeleteParams{MutationID: mutationValue, ID: positionals[0], ExpectedRevision: *expected, Actor: *actor})
	if err != nil {
		return err
	}
	return printIssue(stdout, deleted, *asJSON)
}

func runLink(ctx context.Context, store *storage.Store, args []string, stdout, stderr io.Writer, remove bool) error {
	command := "link"
	if remove {
		command = "unlink"
	}
	positionals, rest, err := splitLeading(args, 2, command)
	if err != nil {
		return err
	}
	flags := newFlags(command, stderr)
	relationship := flags.String("type", "blocks", "relationship name")
	actor := flags.String("actor", defaultActor(), "audit actor")
	mutation := flags.String("mutation-id", "", "idempotency key")
	if err := flags.Parse(rest); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected %s arguments", command)
	}
	mutationValue, err := mutationID(*mutation)
	if err != nil {
		return err
	}
	params := issue.LinkParams{MutationID: mutationValue, SourceID: positionals[0], TargetID: positionals[1], Relationship: *relationship, Actor: *actor}
	if remove {
		err = store.RemoveLink(ctx, params)
	} else {
		err = store.AddLink(ctx, params)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s %s -[%s]-> %s\n", command, params.SourceID, params.Relationship, params.TargetID)
	return nil
}

func runLinks(ctx context.Context, store *storage.Store, args []string, stdout, stderr io.Writer) error {
	positionals, rest, err := splitLeading(args, 1, "links")
	if err != nil {
		return err
	}
	flags := newFlags("links", stderr)
	direction := flags.String("direction", "both", "incoming, outgoing, or both")
	asJSON := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(rest); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected links arguments")
	}
	links, err := store.ListLinks(ctx, positionals[0], *direction)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(stdout, links)
	}
	for _, link := range links {
		fmt.Fprintf(stdout, "%s -[%s]-> %s\n", link.SourceID, link.Relationship, link.TargetID)
	}
	return nil
}

func runHistory(ctx context.Context, store *storage.Store, args []string, stdout, stderr io.Writer) error {
	issueID := ""
	rest := args
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		issueID, rest = rest[0], rest[1:]
	}
	flags := newFlags("history", stderr)
	asJSON := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(rest); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected history arguments")
	}
	events, err := store.History(ctx, issueID)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(stdout, events)
	}
	writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "EVENT\tISSUE\tOPERATION\tACTOR\tREVISION\tMUTATION")
	for _, event := range events {
		revision := "-"
		if event.ResultingRevision != nil {
			revision = strconv.FormatInt(*event.ResultingRevision, 10)
		}
		fmt.Fprintf(writer, "%d\t%s\t%s\t%s\t%s\t%s\n", event.EventID, event.IssueID, event.Operation, event.Actor, revision, event.MutationID)
	}
	return writer.Flush()
}

func runSnapshot(ctx context.Context, store *storage.Store, paths repository.Paths, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("snapshot", stderr)
	output := flags.String("output", paths.Snapshot, "snapshot database path")
	manifest := flags.String("manifest", paths.Manifest, "manifest JSON path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("snapshot accepts flags only")
	}
	if *output != paths.Snapshot && *manifest == paths.Manifest {
		*manifest = filepath.Join(filepath.Dir(*output), "manifest.json")
	}
	result, err := store.ExportSnapshot(ctx, *output, *manifest)
	if err != nil {
		return err
	}
	return writeJSON(stdout, struct {
		Snapshot string           `json:"snapshot"`
		Manifest string           `json:"manifest"`
		Details  storage.Manifest `json:"details"`
	}{*output, *manifest, result})
}
