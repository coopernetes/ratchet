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

const upgradeCommandDesc = `Upgrade all pinned versions to the latest version`

const upgradeCommandHelp = `
Usage: ratchet f [FILE...]

The "upgrade" command unpins any pinned versions, upgrades the unpinned version
constraint to the latest available value, and then re-pins the versions with the
new version constraint.

This command will upgrade pinned versions to versions beyond the constraint
(e.g. v2 -> v3).

EXAMPLES

    ratchet upgrade ./path/to/file.yaml

FLAGS

`

type UpgradeCommand struct {
	flagConcurrency      int64
	flagParser           string
	flagOut              string
	flagPin              bool
	flagExperimentalYAML bool
}

func (c *UpgradeCommand) Desc() string {
	return upgradeCommandDesc
}

func (c *UpgradeCommand) Flags() *flag.FlagSet {
	f := flag.NewFlagSet("", flag.ExitOnError)
	f.Usage = func() {
		fmt.Fprintf(os.Stderr, "%s\n\n", strings.TrimSpace(upgradeCommandHelp))
		f.PrintDefaults()
	}

	f.Int64Var(&c.flagConcurrency, "concurrency", concurrency.DefaultConcurrency(1),
		"maximum number of concurrent resolutions")
	f.StringVar(&c.flagParser, "parser", "actions", "parser to use")
	f.StringVar(&c.flagOut, "out", "", "output path (defaults to input file)")
	f.BoolVar(&c.flagPin, "pin", true, "pin resolved upgraded versions")
	f.BoolVar(&c.flagExperimentalYAML, "experimental-yaml", false,
		"use experimental YAML parser (go.yaml.in/yaml/v4) with surgical text replacement to preserve original file formatting")

	return f
}

func (c *UpgradeCommand) Run(ctx context.Context, originalArgs []string) error {
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

func (c *UpgradeCommand) runAST(ctx context.Context, args []string, res resolver.Resolver) error {
	par, err := parser.For(ctx, c.flagParser)
	if err != nil {
		return err
	}

	loadResult, err := loadYAMLFiles(os.DirFS("."), args)
	if err != nil {
		return err
	}

	if len(loadResult) > 1 && c.flagOut != "" && !strings.HasSuffix(c.flagOut, "/") {
		return fmt.Errorf("-out must be a directory when upgrading multiple files")
	}

	if err := parser.Unpin(ctx, loadResult.nodes()); err != nil {
		return fmt.Errorf("failed to unpin refs: %w", err)
	}

	if err := parser.Upgrade(ctx, res, par, loadResult.nodes(), c.flagConcurrency); err != nil {
		return fmt.Errorf("failed to upgrade refs: %w", err)
	}

	if c.flagPin {
		if err := parser.Pin(ctx, res, par, loadResult.nodes(), c.flagConcurrency); err != nil {
			return fmt.Errorf("failed to pin upgraded refs: %w", err)
		}
	}

	if err := loadResult.writeYAMLFiles(c.flagOut); err != nil {
		return fmt.Errorf("failed to save files: %w", err)
	}

	return nil
}

func (c *UpgradeCommand) runSurgical(ctx context.Context, args []string, res resolver.Resolver) error {
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
		return fmt.Errorf("-out must be a directory when upgrading multiple files")
	}

	replacements, err := surgical.UpgradeSurgical(ctx, res, par, surgical.Nodes(loadResults), c.flagConcurrency)
	if err != nil {
		return fmt.Errorf("failed to upgrade refs: %w", err)
	}

	// If not pinning, we need to clear the NewComment to remove the ratchet comment
	if !c.flagPin {
		for i := range replacements {
			replacements[i].NewComment = ""
		}
	}

	if err := writeSurgicalReplacements(loadResults, replacements, c.flagOut); err != nil {
		return fmt.Errorf("failed to save files: %w", err)
	}

	return nil
}