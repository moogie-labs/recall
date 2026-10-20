// Package cli adapts Recall's application services to human and JSON commands.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"recall/internal/app"
	"recall/internal/bootstrap"
	"recall/internal/config"
	"recall/internal/embedding"
	"recall/internal/mcpserver"
	"recall/internal/model"
	"recall/internal/tui"
)

// Adapter owns its streaming interface and application lifecycle. Runner allows
// overriding the default MCP and TUI adapters independently.
type Adapter func(context.Context, config.Config, io.Reader, io.Writer, io.Writer) error

type Runner struct {
	TUI, MCP Adapter

	open       func(context.Context, config.Config) (*app.App, error)
	initialize func(context.Context, config.Config, string, func(int64, int64)) (bootstrap.Readiness, error)
	provider   func(config.Config) embedding.Provider
	worker     func(context.Context, config.Config) error
	executable func() (string, error)
}

// Run never exits the process. Streaming adapters may close supplied streams
// when their protocol terminates; the MCP adapter preserves stream closers.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return (Runner{}).Run(ctx, args, stdin, stdout, stderr)
}

func (r Runner) Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// This must precede Cobra, application construction, reconciliation, or asset
	// discovery. Guardian invocations retain their environment and FDs 3–6.
	if len(args) > 0 && args[0] == "_embedding-worker" {
		return r.runWorker(ctx, args[1:], stderr)
	}
	if r.open == nil {
		r.open = app.Open
	}
	if r.TUI == nil {
		r.TUI = func(ctx context.Context, cfg config.Config, input io.Reader, output, stderr io.Writer) error {
			if !tui.IsTerminal(input, output) {
				return &model.Error{Code: "runtime_unavailable", Message: "TUI requires terminal input and output; use a CLI subcommand or --help"}
			}
			application, err := r.open(ctx, cfg)
			if err != nil {
				return err
			}
			err = tui.RunIO(ctx, application, cfg, input, output, stderr)
			return errors.Join(err, application.Close())
		}
	}
	if r.MCP == nil {
		r.MCP = func(ctx context.Context, cfg config.Config, input io.Reader, output, _ io.Writer) error {
			application, err := r.open(ctx, cfg)
			if err != nil {
				return err
			}
			err = mcpserver.Run(ctx, application, input, output)
			return errors.Join(err, application.Close())
		}
	}
	if r.initialize == nil {
		r.initialize = bootstrap.Initialize
	}
	if r.provider == nil {
		r.provider = embedding.NewClient
	}
	if r.executable == nil {
		r.executable = os.Executable
	}
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	i := &invocation{ctx: ctx, runner: r, stdin: stdin, stdout: stdout, stderr: stderr}
	root := i.rootCommand()
	// Cobra interprets nil as "use os.Args"; this API's nil means no args.
	root.SetArgs(append([]string{}, args...))
	command, err := root.ExecuteContextC(ctx)
	if err != nil && !i.executed {
		// Do not seed Cobra's mutable flag destination with fallback state:
		// partial parsing can overwrite it before reaching a later --json.
		fallbackJSON := wantsJSON(args, command)
		i.json = fallbackJSON
	}
	if err != nil && !i.executed && model.ErrorCode(err) == "internal" {
		err = invalid("invalid command or arguments; use --help")
	}
	if i.service != nil {
		err = errors.Join(err, i.service.Close())
	}
	if err != nil {
		return i.failure(err)
	}
	if i.streaming {
		return 0
	}
	if err := i.success(); err != nil {
		// A partially written output cannot be repaired with a second JSON value.
		fmt.Fprintln(stderr, "recall: cannot write command output")
		return 1
	}
	return 0
}

type invocation struct {
	ctx                       context.Context
	runner                    Runner
	stdin                     io.Reader
	stdout, stderr            io.Writer
	root                      string
	json, executed, streaming bool
	cfg                       *config.Config
	service                   *app.App
	data                      any
}

func (i *invocation) rootCommand() *cobra.Command {
	c := i.command("recall", "Local notes, tasks, and offline search", noArgs, func(_ *cobra.Command, _ []string) (any, error) {
		return nil, i.adapter("TUI", i.runner.TUI)
	})
	c.SilenceErrors, c.SilenceUsage = true, true
	c.CompletionOptions.DisableDefaultCmd = true
	c.SetIn(i.stdin)
	c.SetOut(i.stdout)
	c.SetErr(i.stderr)
	c.PersistentFlags().StringVar(&i.root, "root", "", "Collection root (default: RECALL_HOME or ~/.local/share/recall)")
	c.PersistentFlags().BoolVar(&i.json, "json", false, "Emit one structured JSON result")
	c.SetFlagErrorFunc(func(*cobra.Command, error) error { return invalid("invalid flags; use --help") })
	defaultHelp := c.HelpFunc()
	c.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		var help bytes.Buffer
		cmd.SetOut(&help)
		defaultHelp(cmd, args)
		cmd.SetOut(i.stdout)
		i.data = help.String()
	})
	c.AddCommand(i.initCommand(), i.syncCommand(), i.statusCommand(), i.documentCommand(), i.folderCommand(), i.taskCommand(), i.searchCommand())
	c.AddCommand(i.command("mcp", "Serve Recall tools over local stdio MCP", noArgs, func(*cobra.Command, []string) (any, error) {
		return nil, i.adapter("MCP", i.runner.MCP)
	}))
	return c
}

func (i *invocation) command(use, short string, args cobra.PositionalArgs, run func(*cobra.Command, []string) (any, error)) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: args, RunE: func(c *cobra.Command, args []string) error {
		i.executed = true
		if err := i.ctx.Err(); err != nil {
			return err
		}
		data, err := run(c, args)
		i.data = data
		return err
	}}
}

func (i *invocation) group(name, short string) *cobra.Command {
	return i.command(name, short, noArgs, func(c *cobra.Command, _ []string) (any, error) {
		if err := c.Help(); err != nil {
			return nil, err
		}
		return i.data, nil
	})
}

func (i *invocation) adapter(name string, adapter Adapter) error {
	if adapter == nil {
		return &model.Error{Code: "runtime_unavailable", Message: name + " is unavailable in this build"}
	}
	if i.json {
		return invalid("JSON mode requires a non-streaming CLI subcommand")
	}
	cfg, err := i.config()
	if err != nil {
		return err
	}
	i.streaming = true
	return adapter(i.ctx, cfg, i.stdin, i.stdout, i.stderr)
}

func (i *invocation) config() (config.Config, error) {
	if i.cfg != nil {
		return *i.cfg, nil
	}
	cfg, err := config.Resolve(i.root)
	if err == nil {
		i.cfg = &cfg
	}
	return cfg, err
}

func (i *invocation) withApp(run func(*app.App) (any, error)) (any, error) {
	if i.service == nil {
		cfg, err := i.config()
		if err != nil {
			return nil, err
		}
		i.service, err = i.runner.open(i.ctx, cfg)
		if err != nil {
			return nil, err
		}
	}
	return run(i.service)
}

func noArgs(_ *cobra.Command, args []string) error {
	if len(args) != 0 {
		return invalid("unexpected arguments; use --help")
	}
	return nil
}

func exactArgs(count int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != count {
			return invalid(fmt.Sprintf("expected %d argument(s); use --help", count))
		}
		return nil
	}
}

func invalid(message string) error { return &model.Error{Code: "invalid_argument", Message: message} }

type envelope struct {
	Data  any          `json:"data"`
	Error *model.Error `json:"error"`
}

func (i *invocation) failure(err error) int {
	problem := &model.Error{Code: model.ErrorCode(err), Message: "operation failed"}
	var typed *model.Error
	if errors.As(err, &typed) {
		problem.Message, problem.Path = typed.Message, typed.Path
	}
	if errors.Is(err, context.Canceled) {
		problem.Message = "operation canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		problem.Message = "operation timed out"
	}
	if i.json {
		if json.NewEncoder(i.stdout).Encode(envelope{Error: problem}) != nil {
			fmt.Fprintln(i.stderr, "recall: cannot write error result")
		}
	} else {
		fmt.Fprintln(i.stderr, "recall:", problem.Error())
		if problem.Code == "not_initialized" {
			fmt.Fprintln(i.stderr, "Run recall init with the same --root to initialize this collection.")
		}
	}
	switch problem.Code {
	case "invalid_argument", "invalid_document", "type_mismatch", "outside_root":
		return 2
	case "not_initialized", "not_found":
		return 3
	case "conflict", "already_exists", "duplicate_id", "folder_not_empty":
		return 4
	default:
		return 1
	}
}

func (i *invocation) success() error {
	if i.json {
		return json.NewEncoder(i.stdout).Encode(envelope{Data: i.data})
	}
	switch data := i.data.(type) {
	case string:
		_, err := io.WriteString(i.stdout, data)
		return err
	case model.Document:
		_, err := io.WriteString(i.stdout, data.Raw)
		return err
	case model.Page[model.DocumentSummary]:
		return writeDocuments(i.stdout, data.Items)
	case documentList:
		for _, d := range data.Diagnostics {
			fmt.Fprintf(i.stderr, "%s: %s: %s\n", d.Path, d.Code, d.Message)
		}
		return writeDocuments(i.stdout, data.Documents)
	case []string:
		for _, value := range data {
			if _, err := fmt.Fprintln(i.stdout, value); err != nil {
				return err
			}
		}
		return nil
	case []model.Event:
		loc := time.Local
		if i.cfg != nil {
			loc, _ = time.LoadLocation(i.cfg.Timezone)
		}
		for _, e := range data {
			if _, err := fmt.Fprintf(i.stdout, "%s\t%s\n", e.At.In(loc).Format(time.RFC3339), e.State); err != nil {
				return err
			}
		}
		return nil
	case []model.Hit:
		w := tabwriter.NewWriter(i.stdout, 0, 4, 2, ' ', 0)
		for _, hit := range data {
			location := hit.Path
			if hit.StartLine > 0 {
				location = fmt.Sprintf("%s:%d-%d", hit.Path, hit.StartLine, hit.EndLine)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n%s\n", location, hit.Title, hit.IndexState, hit.Snippet)
		}
		return w.Flush()
	default:
		encoder := json.NewEncoder(i.stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(data)
	}
}

func writeDocuments(out io.Writer, documents []model.DocumentSummary) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tPATH\tSTATE\tTITLE")
	for _, d := range documents {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d.ID, d.Path, d.State, d.Title)
	}
	return w.Flush()
}

// Recover only output-mode intent when Cobra stops before a trailing flag.
// Use the selected command's flag definitions for arity, not a second parser:
// a string flag can consume even --json or -- as its literal value. Unknown
// flags have no known arity; flags belonging only to sibling commands stay unknown.
func wantsJSON(args []string, command *cobra.Command) bool {
	want := false
	for at := 0; at < len(args); at++ {
		arg := args[at]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		name, value, equals := strings.Cut(arg[2:], "=")
		if name == "json" {
			if !equals {
				want = true
			} else if mode, err := strconv.ParseBool(value); err == nil {
				want = mode
			}
			continue
		}
		if flag := command.Flags().Lookup(name); flag != nil && flag.NoOptDefVal == "" && !equals {
			at++ // Required value, regardless of whether it starts with --.
		}
	}
	return want
}
