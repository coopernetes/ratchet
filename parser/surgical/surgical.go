// Package surgical provides format-preserving YAML operations using go.yaml.in/yaml/v4.package surgical

// Unlike the default parser package which uses braydonk/yaml and AST-based modifications,
// this package uses surgical text replacements based on line/column positions to preserve
// the exact original formatting of YAML files.
package surgical

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"go.yaml.in/yaml/v4"

	"github.com/sethvargo/ratchet/resolver"
	"golang.org/x/sync/semaphore"
)

const (
	ratchetPrefix  = "ratchet:"
	ratchetExclude = "ratchet:exclude"
)

// Replacement represents a surgical text replacement to be made in the original file.
type Replacement struct {
	Filename   string // which file this replacement is for
	Line       int    // 1-indexed line number
	Column     int    // 1-indexed column number
	OldValue   string // original value to replace
	NewValue   string // new value
	NewComment string // new line comment (without leading #)
}

// Parser defines an interface which parses references out of the given yaml node.
type Parser interface {
	DenormalizeRef(ref string) string
	Parse(nodes map[string]*yaml.Node) (*RefsList, error)
}

// NodeWithFile wraps a yaml.Node with its source filename for tracking purposes.
type NodeWithFile struct {
	Node     *yaml.Node
	Filename string
}

// RefsList contains the list of references found in the yaml.
type RefsList struct {
	once          sync.Once
	refs          map[string][]*yaml.Node
	refsWithFiles map[string][]NodeWithFile
}

func (l *RefsList) init() {
	l.refs = make(map[string][]*yaml.Node, 16)
	l.refsWithFiles = make(map[string][]NodeWithFile, 16)
}

// AddWithFile adds a ref with its source filename for surgical replacement tracking.
func (l *RefsList) AddWithFile(ref string, m *yaml.Node, filename string) {
	l.once.Do(l.init)
	l.refs[ref] = append(l.refs[ref], m)
	l.refsWithFiles[ref] = append(l.refsWithFiles[ref], NodeWithFile{Node: m, Filename: filename})
}

// AllWithFiles returns all refs with their source filenames.
func (l *RefsList) AllWithFiles() map[string][]NodeWithFile {
	l.once.Do(l.init)
	result := make(map[string][]NodeWithFile, len(l.refsWithFiles))
	for k, v := range l.refsWithFiles {
		result[k] = append([]NodeWithFile(nil), v...)
	}
	return result
}

// LoadResult holds the parsed yaml node and original contents for a file.
type LoadResult struct {
	Node     *yaml.Node
	Contents string
	Filename string
}

// LoadYAMLFiles loads YAML files and returns their nodes and original contents.
func LoadYAMLFiles(fsys fs.FS, paths []string) (map[string]*LoadResult, error) {
	results := make(map[string]*LoadResult, len(paths))

	for _, pth := range paths {
		pth = filepath.ToSlash(filepath.Clean(pth))
		contents, err := fs.ReadFile(fsys, pth)
		if err != nil {
			return nil, fmt.Errorf("failed to read file %s: %w", pth, err)
		}

		var node yaml.Node
		dec := yaml.NewDecoder(bytes.NewReader(contents))
		if err := dec.Decode(&node); err != nil {
			return nil, fmt.Errorf("failed to parse yaml for %s: %w", pth, err)
		}

		results[pth] = &LoadResult{
			Node:     &node,
			Contents: string(contents),
			Filename: pth,
		}
	}

	return results, nil
}

// Nodes extracts just the yaml.Node pointers from LoadResults.
func Nodes(results map[string]*LoadResult) map[string]*yaml.Node {
	nodes := make(map[string]*yaml.Node, len(results))
	for k, v := range results {
		nodes[k] = v.Node
	}
	return nodes
}

// ApplyReplacements applies surgical text replacements to the original content
// without re-serializing the YAML. This preserves all original formatting.
func ApplyReplacements(contents string, replacements []Replacement) string {
	if len(replacements) == 0 {
		return contents
	}

	lines := strings.Split(contents, "\n")

	// Sort replacements by line (descending) so we can apply them without
	// affecting the positions of subsequent replacements
	slices.SortFunc(replacements, func(a, b Replacement) int {
		if a.Line != b.Line {
			return b.Line - a.Line // descending
		}
		return b.Column - a.Column // descending
	})

	for _, rep := range replacements {
		if rep.Line < 1 || rep.Line > len(lines) {
			continue
		}

		lineIdx := rep.Line - 1
		line := lines[lineIdx]

		// Find the old value in the line starting from the column position
		colIdx := rep.Column - 1
		if colIdx < 0 || colIdx >= len(line) {
			continue
		}

		// Find the old value in the line
		valueStart := strings.Index(line[colIdx:], rep.OldValue)
		if valueStart == -1 {
			continue
		}
		valueStart += colIdx
		valueEnd := valueStart + len(rep.OldValue)

		// Build the new line
		newLine := line[:valueStart] + rep.NewValue + line[valueEnd:]

		// Handle comment - find existing ratchet comment and update/remove it
		ratchetIdx := strings.Index(newLine, " # ratchet:")
		if ratchetIdx == -1 {
			ratchetIdx = strings.Index(newLine, "\t# ratchet:")
		}

		if ratchetIdx != -1 {
			// Remove existing ratchet comment
			newLine = strings.TrimRight(newLine[:ratchetIdx], " \t")
		}

		if rep.NewComment != "" {
			newLine = newLine + " # " + rep.NewComment
		}

		lines[lineIdx] = newLine
	}

	return strings.Join(lines, "\n")
}

// PinSurgical extracts all references from the given YAML documents and resolves them
// using the given resolver, returning a list of surgical text replacements to apply.
// This does NOT modify the YAML nodes - it only returns replacements to be applied
// to the original text, preserving all original formatting.
func PinSurgical(ctx context.Context, res resolver.Resolver, parser Parser, nodes map[string]*yaml.Node, concurrency int64) ([]Replacement, error) {
	refsList, err := parser.Parse(nodes)
	if err != nil {
		return nil, err
	}
	refs := refsList.AllWithFiles()

	sem := semaphore.NewWeighted(concurrency)

	var merrLock sync.Mutex
	var merr error
	var replacements []Replacement
	var replacementsLock sync.Mutex

	for ref, nodesWithFiles := range refs {
		ref := ref
		nodesWithFiles := nodesWithFiles

		if isAbsolute(ref) {
			continue
		}

		if err := sem.Acquire(ctx, 1); err != nil {
			return nil, fmt.Errorf("failed to acquire semaphore: %w", err)
		}

		go func() {
			defer sem.Release(1)

			// Pre-filter excluded nodes
			tmp := nodesWithFiles[:0]
			for _, nwf := range nodesWithFiles {
				if !shouldExclude(nwf.Node.LineComment) {
					tmp = append(tmp, nwf)
				}
			}
			nodesWithFiles = tmp

			if len(nodesWithFiles) == 0 {
				return
			}

			resolved, err := res.Resolve(ctx, ref)
			if err != nil {
				merrLock.Lock()
				merr = errors.Join(merr, fmt.Errorf("failed to resolve %q: %w", ref, err))
				merrLock.Unlock()
				return
			}

			denormRef := resolver.DenormalizeRef(ref)

			for _, nwf := range nodesWithFiles {
				node := nwf.Node
				newValue := strings.Replace(node.Value, denormRef, resolved, 1)
				newComment := appendOriginalToComment(node.LineComment, node.Value)

				rep := Replacement{
					Filename:   nwf.Filename,
					Line:       node.Line,
					Column:     node.Column,
					OldValue:   node.Value,
					NewValue:   newValue,
					NewComment: newComment,
				}

				replacementsLock.Lock()
				replacements = append(replacements, rep)
				replacementsLock.Unlock()
			}
		}()
	}

	if err := sem.Acquire(ctx, concurrency); err != nil {
		return nil, fmt.Errorf("failed to wait for semaphore: %w", err)
	}

	return replacements, merr
}

// UnpinSurgical reverts pinned versions back to their original unpinned values,
// returning surgical text replacements to apply.
func UnpinSurgical(ctx context.Context, nodes map[string]*yaml.Node) ([]Replacement, error) {
	var replacements []Replacement

	for filename, document := range nodes {
		if err := unpinSurgicalNode(document, filename, &replacements); err != nil {
			return nil, err
		}
	}

	return replacements, nil
}

func unpinSurgicalNode(m *yaml.Node, filename string, replacements *[]Replacement) error {
	if m == nil {
		return nil
	}

	switch m.Kind {
	case yaml.DocumentNode, yaml.SequenceNode, yaml.MappingNode:
		for _, child := range m.Content {
			if err := unpinSurgicalNode(child, filename, replacements); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		if original := extractOriginalFromComment(m.LineComment); original != "" {
			*replacements = append(*replacements, Replacement{
				Filename:   filename,
				Line:       m.Line,
				Column:     m.Column,
				OldValue:   m.Value,
				NewValue:   original,
				NewComment: "", // Remove the ratchet comment
			})
		}
	}

	return nil
}

// UpdateSurgical updates already-pinned refs to their latest versions,
// returning surgical text replacements to apply.
func UpdateSurgical(ctx context.Context, res resolver.Resolver, parser Parser, nodes map[string]*yaml.Node, concurrency int64) ([]Replacement, error) {
	refsList, err := parser.Parse(nodes)
	if err != nil {
		return nil, err
	}
	refs := refsList.AllWithFiles()

	sem := semaphore.NewWeighted(concurrency)

	var merrLock sync.Mutex
	var merr error
	var replacementsLock sync.Mutex
	var replacements []Replacement

	for _, nodesWithFiles := range refs {
		nodesWithFiles := nodesWithFiles

		if err := sem.Acquire(ctx, 1); err != nil {
			return nil, fmt.Errorf("failed to acquire semaphore: %w", err)
		}

		go func() {
			defer sem.Release(1)

			// Pre-filter excluded nodes
			tmp := nodesWithFiles[:0]
			for _, nwf := range nodesWithFiles {
				if !shouldExclude(nwf.Node.LineComment) {
					tmp = append(tmp, nwf)
				}
			}
			nodesWithFiles = tmp

			if len(nodesWithFiles) == 0 {
				return
			}

			// For update, we need to get the original ref from the comment
			for _, nwf := range nodesWithFiles {
				node := nwf.Node
				originalRef := extractOriginalFromComment(node.LineComment)
				if originalRef == "" {
					// Not pinned, skip
					continue
				}

				resolved, err := res.Resolve(ctx, originalRef)
				if err != nil {
					merrLock.Lock()
					merr = errors.Join(merr, fmt.Errorf("failed to resolve %q: %w", originalRef, err))
					merrLock.Unlock()
					continue
				}

				denormRef := resolver.DenormalizeRef(originalRef)
				newValue := strings.Replace(originalRef, denormRef, resolved, 1)
				newComment := ratchetPrefix + originalRef

				rep := Replacement{
					Filename:   nwf.Filename,
					Line:       node.Line,
					Column:     node.Column,
					OldValue:   node.Value,
					NewValue:   newValue,
					NewComment: newComment,
				}

				replacementsLock.Lock()
				replacements = append(replacements, rep)
				replacementsLock.Unlock()
			}
		}()
	}

	if err := sem.Acquire(ctx, concurrency); err != nil {
		return nil, fmt.Errorf("failed to wait for semaphore: %w", err)
	}

	return replacements, merr
}

// UpgradeSurgical upgrades pinned versions to the latest available version,
// returning surgical text replacements to apply.
func UpgradeSurgical(ctx context.Context, res resolver.Resolver, parser Parser, nodes map[string]*yaml.Node, concurrency int64) ([]Replacement, error) {
	refsList, err := parser.Parse(nodes)
	if err != nil {
		return nil, err
	}
	refs := refsList.AllWithFiles()

	sem := semaphore.NewWeighted(concurrency)

	var merrLock sync.Mutex
	var merr error
	var replacementsLock sync.Mutex
	var replacements []Replacement

	for ref, nodesWithFiles := range refs {
		ref := ref
		nodesWithFiles := nodesWithFiles

		if err := sem.Acquire(ctx, 1); err != nil {
			return nil, fmt.Errorf("failed to acquire semaphore: %w", err)
		}

		go func() {
			defer sem.Release(1)

			// Pre-filter excluded nodes
			tmp := nodesWithFiles[:0]
			for _, nwf := range nodesWithFiles {
				if !shouldExclude(nwf.Node.LineComment) {
					tmp = append(tmp, nwf)
				}
			}
			nodesWithFiles = tmp

			if len(nodesWithFiles) == 0 {
				return
			}

			for _, nwf := range nodesWithFiles {
				node := nwf.Node
				
				// Get the base ref (either from comment or the current value)
				baseRef := ref
				if original := extractOriginalFromComment(node.LineComment); original != "" {
					baseRef = original
				}

				// Get the latest version
				latestRef, err := res.LatestVersion(ctx, baseRef)
				if err != nil {
					merrLock.Lock()
					merr = errors.Join(merr, fmt.Errorf("failed to upgrade %q: %w", baseRef, err))
					merrLock.Unlock()
					continue
				}

				resolved, err := res.Resolve(ctx, latestRef)
				if err != nil {
					merrLock.Lock()
					merr = errors.Join(merr, fmt.Errorf("failed to resolve %q: %w", latestRef, err))
					merrLock.Unlock()
					continue
				}

				denormRef := resolver.DenormalizeRef(latestRef)
				newValue := strings.Replace(latestRef, denormRef, resolved, 1)
				newComment := ratchetPrefix + latestRef

				rep := Replacement{
					Filename:   nwf.Filename,
					Line:       node.Line,
					Column:     node.Column,
					OldValue:   node.Value,
					NewValue:   newValue,
					NewComment: newComment,
				}

				replacementsLock.Lock()
				replacements = append(replacements, rep)
				replacementsLock.Unlock()
			}
		}()
	}

	if err := sem.Acquire(ctx, concurrency); err != nil {
		return nil, fmt.Errorf("failed to wait for semaphore: %w", err)
	}

	return replacements, merr
}

// Helper functions

func shouldExclude(comment string) bool {
	return strings.Contains(comment, ratchetExclude)
}

func isAbsolute(ref string) bool {
	// Check for SHA-like patterns (40 or 64 hex chars after @)
	if idx := strings.LastIndex(ref, "@"); idx >= 0 {
		after := ref[idx+1:]
		if len(after) >= 40 && isHexString(after[:40]) {
			return true
		}
		if len(after) >= 64 && isHexString(after[:64]) {
			return true
		}
	}
	// Check for sha256: prefix (container digests)
	if strings.Contains(ref, "@sha256:") {
		return true
	}
	return false
}

func isHexString(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func appendOriginalToComment(existing, original string) string {
	// If there's already a ratchet comment, keep it
	if strings.Contains(existing, ratchetPrefix) {
		return strings.TrimPrefix(existing, "# ")
	}
	return ratchetPrefix + original
}

func extractOriginalFromComment(comment string) string {
	if idx := strings.Index(comment, ratchetPrefix); idx >= 0 {
		return strings.TrimSpace(comment[idx+len(ratchetPrefix):])
	}
	return ""
}
