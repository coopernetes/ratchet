package command

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sethvargo/ratchet/internal/concurrency"
	"github.com/sethvargo/ratchet/parser"
	"github.com/sethvargo/ratchet/parser/surgical"
	"github.com/sethvargo/ratchet/resolver"
)

const pinCommandDesc = `Resolve and pin all versions`

const pinCommandHelp = `
Usage: ratchet pin [FILE...]

The "pin" command resolves and pins any unpinned versions to their absolute or
hashed version for the given input file:

    actions/checkout@v3 -> actions/checkout@2541b1294d2704b0964813337f...

The original unpinned version is preserved in a comment, next to the pinned
version. If a version is already pinned, it does nothing.

To update versions that are already pinned, use the "update" command instead.

EXAMPLES

  ratchet pin ./path/to/file.yaml

FLAGS

`

type PinCommand struct {
	flagConcurrency     int64
	flagParser          string
	flagOut             string
	flagExperimentalYAML bool
}

func (c *PinCommand) Desc() string {
	return pinCommandDesc
}

func (c *PinCommand) Flags() *flag.FlagSet {
	f := flag.NewFlagSet("", flag.ExitOnError)
	f.Usage = func() {
		fmt.Fprintf(os.Stderr, "%s\n\n", strings.TrimSpace(pinCommandHelp))
		f.PrintDefaults()
	}

	f.Int64Var(&c.flagConcurrency, "concurrency", concurrency.DefaultConcurrency(1),
		"maximum number of concurrent resolutions")
	f.StringVar(&c.flagParser, "parser", "actions", "parser to use")
	f.StringVar(&c.flagOut, "out", "", "output path (defaults to input file)")
	f.BoolVar(&c.flagExperimentalYAML, "experimental-yaml", false,
		"use experimental YAML parser (go.yaml.in/yaml/v4) with surgical text replacement to preserve original file formatting")

	return f
}

func (c *PinCommand) Run(ctx context.Context, originalArgs []string) error {
	args, err := parseFlags(c.Flags(), originalArgs)
	if err != nil {
		return fmt.Errorf("failed to parse flags: %w", err)
	}

	res, err := resolver.NewDefaultResolver(ctx)
	if err != nil {
		return fmt.Errorf("failed to create resolver: %w", err)
	}

	// Use surgical approach if experimental-yaml flag is set
	if c.flagExperimentalYAML {
		return c.runSurgical(ctx, args, res)
	}

	// Default: use AST-based approach
	return c.runAST(ctx, args, res)
}

func (c *PinCommand) runAST(ctx context.Context, args []string, res resolver.Resolver) error {
	par, err := parser.For(ctx, c.flagParser)
	if err != nil {
		return err
	}

	loadResult, err := loadYAMLFiles(os.DirFS("."), args)
	if err != nil {
		return err
	}

	if len(loadResult) > 1 && c.flagOut != "" && !strings.HasSuffix(c.flagOut, "/") {
		return fmt.Errorf("-out must be a directory when pinning multiple files")
	}

	if err := parser.Pin(ctx, res, par, loadResult.nodes(), c.flagConcurrency); err != nil {
		return fmt.Errorf("failed to pin refs: %w", err)
	}

	if err := loadResult.writeYAMLFiles(c.flagOut); err != nil {
		return fmt.Errorf("failed to save files: %w", err)
	}

	return nil
}

func (c *PinCommand) runSurgical(ctx context.Context, args []string, res resolver.Resolver) error {
	par, err := surgical.For(c.flagParser)
	if err != nil {
		return err
	}
	if par == nil {
		return fmt.Errorf("parser %q not supported for preserve-formatting mode", c.flagParser)
	}

	loadResults, err := surgical.LoadYAMLFiles(os.DirFS("."), args)
	if err != nil {
		return err
	}

	if len(loadResults) > 1 && c.flagOut != "" && !strings.HasSuffix(c.flagOut, "/") {
		return fmt.Errorf("-out must be a directory when pinning multiple files")
	}

	replacements, err := surgical.PinSurgical(ctx, res, par, surgical.Nodes(loadResults), c.flagConcurrency)
	if err != nil {
		return fmt.Errorf("failed to pin refs: %w", err)
	}

	if err := writeSurgicalReplacements(loadResults, replacements, c.flagOut); err != nil {
		return fmt.Errorf("failed to save files: %w", err)
	}

	return nil
}
