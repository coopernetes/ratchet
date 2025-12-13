package surgical

import (
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"go.yaml.in/yaml/v4"
)

func TestApplyReplacements(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		contents     string
		replacements []Replacement
		expected     string
	}{
		{
			name:         "no_replacements",
			contents:     "foo: bar\n",
			replacements: nil,
			expected:     "foo: bar\n",
		},
		{
			name:     "simple_replacement",
			contents: "uses: 'actions/checkout@v3'\n",
			replacements: []Replacement{
				{Line: 1, Column: 7, OldValue: "actions/checkout@v3", NewValue: "actions/checkout@abc123", NewComment: "ratchet:actions/checkout@v3"},
			},
			expected: "uses: 'actions/checkout@abc123' # ratchet:actions/checkout@v3\n",
		},
		{
			name:     "remove_existing_comment",
			contents: "uses: 'actions/checkout@abc123' # ratchet:actions/checkout@v3\n",
			replacements: []Replacement{
				{Line: 1, Column: 7, OldValue: "actions/checkout@abc123", NewValue: "actions/checkout@v3", NewComment: ""},
			},
			expected: "uses: 'actions/checkout@v3'\n",
		},
		{
			name: "multiple_replacements_same_file",
			contents: `steps:
  - uses: 'actions/checkout@v3'
  - uses: 'actions/setup-go@v4'
`,
			replacements: []Replacement{
				{Line: 2, Column: 11, OldValue: "actions/checkout@v3", NewValue: "actions/checkout@sha1", NewComment: "ratchet:actions/checkout@v3"},
				{Line: 3, Column: 11, OldValue: "actions/setup-go@v4", NewValue: "actions/setup-go@sha2", NewComment: "ratchet:actions/setup-go@v4"},
			},
			expected: `steps:
  - uses: 'actions/checkout@sha1' # ratchet:actions/checkout@v3
  - uses: 'actions/setup-go@sha2' # ratchet:actions/setup-go@v4
`,
		},
		{
			name: "preserves_indentation",
			contents: `jobs:
  init:
    steps:
    - uses: 'actions/checkout@v3'
`,
			replacements: []Replacement{
				{Line: 4, Column: 12, OldValue: "actions/checkout@v3", NewValue: "actions/checkout@sha1", NewComment: "ratchet:actions/checkout@v3"},
			},
			expected: `jobs:
  init:
    steps:
    - uses: 'actions/checkout@sha1' # ratchet:actions/checkout@v3
`,
		},
		{
			name: "preserves_multiline_content",
			contents: `steps:
  - uses: 'actions/checkout@v3'
  - run: |-
      echo "Hello"
      echo "World"
`,
			replacements: []Replacement{
				{Line: 2, Column: 11, OldValue: "actions/checkout@v3", NewValue: "actions/checkout@sha1", NewComment: "ratchet:actions/checkout@v3"},
			},
			expected: `steps:
  - uses: 'actions/checkout@sha1' # ratchet:actions/checkout@v3
  - run: |-
      echo "Hello"
      echo "World"
`,
		},
		{
			name: "handles_docker_refs",
			contents: `container:
  image: 'ubuntu:20.04'
`,
			replacements: []Replacement{
				{Line: 2, Column: 10, OldValue: "ubuntu:20.04", NewValue: "ubuntu@sha256:abc123", NewComment: "ratchet:ubuntu:20.04"},
			},
			expected: `container:
  image: 'ubuntu@sha256:abc123' # ratchet:ubuntu:20.04
`,
		},
		{
			name:     "invalid_line_number",
			contents: "foo: bar\n",
			replacements: []Replacement{
				{Line: 100, Column: 1, OldValue: "bar", NewValue: "baz", NewComment: ""},
			},
			expected: "foo: bar\n",
		},
		{
			name:     "invalid_column_number",
			contents: "foo: bar\n",
			replacements: []Replacement{
				{Line: 1, Column: 100, OldValue: "bar", NewValue: "baz", NewComment: ""},
			},
			expected: "foo: bar\n",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := ApplyReplacements(tc.contents, tc.replacements)
			if got != tc.expected {
				t.Errorf("ApplyReplacements() mismatch\ngot:\n%s\nwant:\n%s", got, tc.expected)
			}
		})
	}
}

func TestLoadYAMLFiles(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		files   map[string]string
		paths   []string
		wantErr bool
	}{
		{
			name: "single_file",
			files: map[string]string{
				"test.yml": "foo: bar\n",
			},
			paths:   []string{"test.yml"},
			wantErr: false,
		},
		{
			name: "multiple_files",
			files: map[string]string{
				"a.yml": "a: 1\n",
				"b.yml": "b: 2\n",
			},
			paths:   []string{"a.yml", "b.yml"},
			wantErr: false,
		},
		{
			name:    "file_not_found",
			files:   map[string]string{},
			paths:   []string{"nonexistent.yml"},
			wantErr: true,
		},
		{
			name: "invalid_yaml",
			files: map[string]string{
				"bad.yml": "foo: [invalid\n",
			},
			paths:   []string{"bad.yml"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fsys := fstest.MapFS{}
			for name, content := range tc.files {
				fsys[name] = &fstest.MapFile{Data: []byte(content)}
			}

			results, err := LoadYAMLFiles(fsys, tc.paths)
			if tc.wantErr {
				if err == nil {
					t.Error("LoadYAMLFiles() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("LoadYAMLFiles() unexpected error: %v", err)
				return
			}

			if len(results) != len(tc.paths) {
				t.Errorf("LoadYAMLFiles() returned %d results, want %d", len(results), len(tc.paths))
			}

			for _, path := range tc.paths {
				if _, ok := results[path]; !ok {
					t.Errorf("LoadYAMLFiles() missing result for %s", path)
				}
			}
		})
	}
}

func TestLoadYAMLFiles_PreservesContent(t *testing.T) {
	t.Parallel()

	// Test that original content is preserved exactly
	content := `# Comment
jobs:
  init:
    steps:
    - uses: 'actions/checkout@v3'  # inline comment
`
	fsys := fstest.MapFS{
		"test.yml": &fstest.MapFile{Data: []byte(content)},
	}

	results, err := LoadYAMLFiles(fsys, []string{"test.yml"})
	if err != nil {
		t.Fatalf("LoadYAMLFiles() error: %v", err)
	}

	if results["test.yml"].Contents != content {
		t.Errorf("LoadYAMLFiles() contents not preserved\ngot:\n%s\nwant:\n%s",
			results["test.yml"].Contents, content)
	}
}

func TestNodes(t *testing.T) {
	t.Parallel()

	node1 := &yaml.Node{}
	node2 := &yaml.Node{}

	results := map[string]*LoadResult{
		"a.yml": {Node: node1, Filename: "a.yml"},
		"b.yml": {Node: node2, Filename: "b.yml"},
	}

	nodes := Nodes(results)

	if len(nodes) != 2 {
		t.Errorf("Nodes() returned %d nodes, want 2", len(nodes))
	}
	if nodes["a.yml"] != node1 {
		t.Error("Nodes() wrong node for a.yml")
	}
	if nodes["b.yml"] != node2 {
		t.Error("Nodes() wrong node for b.yml")
	}
}

func TestActions_Parse(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		exp  []string
	}{
		{
			name: "empty_file",
			in:   "jobs:\n",
			exp:  nil,
		},
		{
			name: "uses_in_steps",
			in: `
jobs:
  my_job:
    steps:
      - uses: 'actions/checkout@v3'
      - uses: 'docker://ubuntu:20.04'
`,
			exp: []string{
				"actions://actions/checkout@v3",
				"container://ubuntu:20.04",
			},
		},
		{
			name: "workflow_uses",
			in: `
jobs:
  other_job:
    uses: 'org/repo/.github/workflows/other.yml@v0'
`,
			exp: []string{
				"actions://org/repo/.github/workflows/other.yml@v0",
			},
		},
		{
			name: "container_image",
			in: `
jobs:
  my_job:
    container:
      image: 'ubuntu:20.04'
`,
			exp: []string{
				"container://ubuntu:20.04",
			},
		},
		{
			name: "services",
			in: `
jobs:
  my_job:
    services:
      nginx:
        image: 'nginx:1.21'
`,
			exp: []string{
				"container://nginx:1.21",
			},
		},
		{
			name: "composite_action",
			in: `
runs:
  using: 'composite'
  steps:
    - uses: 'actions/checkout@v3'
`,
			exp: []string{
				"actions://actions/checkout@v3",
			},
		},
		{
			name: "skips_local_paths",
			in: `
jobs:
  my_job:
    steps:
      - uses: './local/action'
      - uses: 'actions/checkout@v3'
`,
			exp: []string{
				"actions://actions/checkout@v3",
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var node yaml.Node
			if err := yaml.Unmarshal([]byte(tc.in), &node); err != nil {
				t.Fatalf("yaml.Unmarshal() error: %v", err)
			}

			parser := &Actions{}
			refs, err := parser.Parse(map[string]*yaml.Node{"test.yml": &node})
			if err != nil {
				t.Fatalf("Parse() error: %v", err)
			}

			got := make([]string, 0)
			for ref := range refs.AllWithFiles() {
				got = append(got, ref)
			}

			// Sort for comparison
			sortStrings(got)
			sortStrings(tc.exp)

			// Handle nil vs empty slice comparison
			if len(got) == 0 && len(tc.exp) == 0 {
				return // Both empty, pass
			}

			if !reflect.DeepEqual(got, tc.exp) {
				t.Errorf("Parse() refs mismatch\ngot:  %v\nwant: %v", got, tc.exp)
			}
		})
	}
}

func TestCircleCI_Parse(t *testing.T) {
	t.Parallel()

	// Note: CircleCI orbs are not supported because there's no documented API
	// for resolving orbs to an absolute version
	in := `
jobs:
  build:
    docker:
      - image: 'cimg/go:1.20'
`

	var node yaml.Node
	if err := yaml.Unmarshal([]byte(in), &node); err != nil {
		t.Fatalf("yaml.Unmarshal() error: %v", err)
	}

	parser := &CircleCI{}
	refs, err := parser.Parse(map[string]*yaml.Node{"test.yml": &node})
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}

	got := make([]string, 0)
	for ref := range refs.AllWithFiles() {
		got = append(got, ref)
	}

	sortStrings(got)
	expected := []string{
		"container://cimg/go:1.20",
	}

	if !reflect.DeepEqual(got, expected) {
		t.Errorf("Parse() refs mismatch\ngot:  %v\nwant: %v", got, expected)
	}
}

func TestCloudBuild_Parse(t *testing.T) {
	t.Parallel()

	in := `
steps:
  - name: 'gcr.io/cloud-builders/docker'
    args: ['build', '.']
`

	var node yaml.Node
	if err := yaml.Unmarshal([]byte(in), &node); err != nil {
		t.Fatalf("yaml.Unmarshal() error: %v", err)
	}

	parser := &CloudBuild{}
	refs, err := parser.Parse(map[string]*yaml.Node{"test.yml": &node})
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}

	got := make([]string, 0)
	for ref := range refs.AllWithFiles() {
		got = append(got, ref)
	}

	expected := []string{
		"container://gcr.io/cloud-builders/docker",
	}

	if !reflect.DeepEqual(got, expected) {
		t.Errorf("Parse() refs mismatch\ngot:  %v\nwant: %v", got, expected)
	}
}

func TestDrone_Parse(t *testing.T) {
	t.Parallel()

	in := `
kind: pipeline
steps:
  - name: build
    image: 'golang:1.20'
`

	var node yaml.Node
	if err := yaml.Unmarshal([]byte(in), &node); err != nil {
		t.Fatalf("yaml.Unmarshal() error: %v", err)
	}

	parser := &Drone{}
	refs, err := parser.Parse(map[string]*yaml.Node{"test.yml": &node})
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}

	got := make([]string, 0)
	for ref := range refs.AllWithFiles() {
		got = append(got, ref)
	}

	expected := []string{
		"container://golang:1.20",
	}

	if !reflect.DeepEqual(got, expected) {
		t.Errorf("Parse() refs mismatch\ngot:  %v\nwant: %v", got, expected)
	}
}

func TestGitLabCI_Parse(t *testing.T) {
	t.Parallel()

	in := `
image: 'ruby:3.0'
test:
  image: 'golang:1.20'
  script:
    - go test
`

	var node yaml.Node
	if err := yaml.Unmarshal([]byte(in), &node); err != nil {
		t.Fatalf("yaml.Unmarshal() error: %v", err)
	}

	parser := &GitLabCI{}
	refs, err := parser.Parse(map[string]*yaml.Node{"test.yml": &node})
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}

	got := make([]string, 0)
	for ref := range refs.AllWithFiles() {
		got = append(got, ref)
	}

	sortStrings(got)
	expected := []string{
		"container://golang:1.20",
		"container://ruby:3.0",
	}

	if !reflect.DeepEqual(got, expected) {
		t.Errorf("Parse() refs mismatch\ngot:  %v\nwant: %v", got, expected)
	}
}

func TestTekton_Parse(t *testing.T) {
	t.Parallel()

	in := `
apiVersion: tekton.dev/v1beta1
kind: Task
spec:
  steps:
    - name: build
      image: 'golang:1.20'
`

	var node yaml.Node
	if err := yaml.Unmarshal([]byte(in), &node); err != nil {
		t.Fatalf("yaml.Unmarshal() error: %v", err)
	}

	parser := &Tekton{}
	refs, err := parser.Parse(map[string]*yaml.Node{"test.yml": &node})
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}

	got := make([]string, 0)
	for ref := range refs.AllWithFiles() {
		got = append(got, ref)
	}

	expected := []string{
		"container://golang:1.20",
	}

	if !reflect.DeepEqual(got, expected) {
		t.Errorf("Parse() refs mismatch\ngot:  %v\nwant: %v", got, expected)
	}
}

func TestFor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		parser  string
		wantNil bool
	}{
		{"actions", "actions", false},
		{"circleci", "circleci", false},
		{"cloudbuild", "cloudbuild", false},
		{"drone", "drone", false},
		{"gitlabci", "gitlabci", false},
		{"tekton", "tekton", false},
		{"unknown", "unknown", true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parser, err := For(tc.parser)
			if err != nil {
				t.Fatalf("For() error: %v", err)
			}

			if tc.wantNil && parser != nil {
				t.Errorf("For(%q) = %v, want nil", tc.parser, parser)
			}
			if !tc.wantNil && parser == nil {
				t.Errorf("For(%q) = nil, want non-nil", tc.parser)
			}
		})
	}
}

// sortStrings sorts a slice of strings in place.
func sortStrings(s []string) {
	for i := 0; i < len(s); i++ {
		for j := i + 1; j < len(s); j++ {
			if s[i] > s[j] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

func TestRoundTrip_PreservesFormatting(t *testing.T) {
	t.Parallel()

	// Test that unpinning a pinned file preserves all formatting
	original := `jobs:
  init:
    runs-on: 'ubuntu-latest'

    steps:
    - uses: 'actions/checkout@abc123def456' # ratchet:actions/checkout@v3

    - name: 'Test'
      uses: 'actions/setup-go@xyz789' # ratchet:actions/setup-go@v4
      with:
        go-version: '1.20'
`

	expected := `jobs:
  init:
    runs-on: 'ubuntu-latest'

    steps:
    - uses: 'actions/checkout@v3'

    - name: 'Test'
      uses: 'actions/setup-go@v4'
      with:
        go-version: '1.20'
`

	// Parse the YAML to get node positions
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(original), &node); err != nil {
		t.Fatalf("yaml.Unmarshal() error: %v", err)
	}

	// Simulate unpin replacements (these would come from the actual unpin logic)
	replacements := []Replacement{
		{Line: 6, Column: 12, OldValue: "actions/checkout@abc123def456", NewValue: "actions/checkout@v3", NewComment: ""},
		{Line: 9, Column: 14, OldValue: "actions/setup-go@xyz789", NewValue: "actions/setup-go@v4", NewComment: ""},
	}

	got := ApplyReplacements(original, replacements)
	if got != expected {
		t.Errorf("Round-trip formatting not preserved\ngot:\n%s\nwant:\n%s", got, expected)
	}
}

func TestApplyReplacements_Unicode(t *testing.T) {
	t.Parallel()

	// Test that unicode content is preserved
	contents := `steps:
  - uses: 'actions/checkout@v3'
  - run: echo "Hello 😀 World"
`

	replacements := []Replacement{
		{Line: 2, Column: 11, OldValue: "actions/checkout@v3", NewValue: "actions/checkout@sha1", NewComment: "ratchet:actions/checkout@v3"},
	}

	got := ApplyReplacements(contents, replacements)

	if !strings.Contains(got, "😀") {
		t.Error("Unicode emoji not preserved")
	}
	if !strings.Contains(got, "actions/checkout@sha1") {
		t.Error("Replacement not applied")
	}
}
