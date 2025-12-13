feat: Add experimental YAML parser with format-preserving surgical replacements

Closes #117

This PR introduces an opt-in experimental YAML parser that preserves original
file formatting when pinning/unpinning references. The new implementation uses
go.yaml.in/yaml/v4 (the official yaml.org maintained fork) alongside the
existing github.com/braydonk/yaml parser.

## Motivation

The existing YAML parser (braydonk/yaml) is archived and no longer maintained.
This change introduces a migration path to the maintained go.yaml.in/yaml/v4
library while preserving backward compatibility.

## Key Changes

### New `-experimental-yaml` Flag

Available on `pin`, `unpin`, `update`, and `upgrade` commands:

```bash
ratchet pin -experimental-yaml workflow.yml
ratchet unpin -experimental-yaml workflow.yml
```

### Formatting Preservation

The experimental parser uses "surgical" text replacement based on YAML node
line/column positions instead of re-serializing the AST. This preserves:

- Original indentation styles (including non-standard indentation)
- Whitespace patterns
- Multiline literal block scalars (`|`, `|-`, `|+`)
- Leading/trailing blank lines
- Unicode characters (emojis, etc.)

### Behavior Comparison

| Aspect | Default Parser | Experimental Parser |
|--------|---------------|---------------------|
| Library | braydonk/yaml (archived) | go.yaml.in/yaml/v4 (maintained) |
| Indentation | Normalized | Preserved exactly |
| Whitespace | Normalized | Preserved exactly |
| Multiline blocks | Preserved | Preserved |

## New Package: `parser/surgical/`

- `surgical.go` - Core surgical replacement logic
- `parsers.go` - Parser implementations for all CI systems (Actions, CircleCI, CloudBuild, Drone, GitLabCI, Tekton)
- `surgical_test.go` - Comprehensive unit tests

## Migration Path

1. **Phase 1 (Current)**: Both parsers available; old is default, new is opt-in
2. **Phase 2**: Expand test coverage and gather feedback
3. **Phase 3**: Consider making experimental parser the default
4. **Phase 4**: Deprecate old parser, remove braydonk/yaml dependency

## Testing

- All existing tests pass
- New unit tests for surgical package
- Manual testing with testdata files confirms formatting preservation
- Round-trip testing (pin → unpin) produces identical files

## Files Changed

- `command/pin.go` - Added `-experimental-yaml` flag and dual code path
- `command/unpin.go` - Added `-experimental-yaml` flag and dual code path
- `command/update.go` - Added `-experimental-yaml` flag and dual code path
- `command/upgrade.go` - Added `-experimental-yaml` flag and dual code path
- `command/command.go` - Added `writeSurgicalReplacements()` helper
- `parser/surgical/surgical.go` - Core surgical replacement logic (new)
- `parser/surgical/parsers.go` - Parser implementations (new)
- `parser/surgical/surgical_test.go` - Unit tests (new)
- `go.mod` - Added go.yaml.in/yaml/v4 dependency
