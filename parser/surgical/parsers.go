package surgical

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/sethvargo/ratchet/resolver"
)

// Actions implements the Parser interface for GitHub Actions workflows.
type Actions struct{}

func (a *Actions) DenormalizeRef(ref string) string {
	isContainer := strings.HasPrefix(ref, resolver.ContainerProtocol)
	ref = resolver.DenormalizeRef(ref)
	if isContainer {
		return "docker://" + ref
	}
	return ref
}

func (a *Actions) Parse(nodes map[string]*yaml.Node) (*RefsList, error) {
	var refs RefsList

	for pth, node := range nodes {
		if err := a.parseOne(&refs, pth, node); err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", pth, err)
		}
	}

	return &refs, nil
}

func (a *Actions) parseOne(refs *RefsList, filename string, node *yaml.Node) error {
	if node == nil {
		return nil
	}

	if node.Kind != yaml.DocumentNode {
		return fmt.Errorf("expected document node, got %v", node.Kind)
	}

	// Top-level object map
	for _, docMap := range node.Content {
		if docMap.Kind != yaml.MappingNode {
			continue
		}

		for i, topLevelMap := range docMap.Content {
			// runs: keyword (for composite actions)
			if topLevelMap.Value == "runs" {
				runs := docMap.Content[i+1]
				if runs.Kind != yaml.MappingNode {
					continue
				}

				// Only look at composite actions.
				foundComposite := false
				for j, runMap := range runs.Content {
					if runMap.Value == "using" && len(runs.Content) > j+1 && runs.Content[j+1].Value == "composite" {
						foundComposite = true
						break
					}
				}
				if !foundComposite {
					continue
				}

				// List of steps, iterate over each step and find the "uses" clause.
				for j, runMap := range runs.Content {
					if runMap.Value == "steps" {
						steps := runs.Content[j+1]
						for _, step := range steps.Content {
							if step.Kind != yaml.MappingNode {
								continue
							}

							for k, property := range step.Content {
								if property.Value == "uses" {
									uses := step.Content[k+1]
									if strings.Contains(uses.Value, "${{") {
										continue
									}

									switch {
									case strings.HasPrefix(uses.Value, "docker://"):
										ref := resolver.NormalizeContainerRef(uses.Value)
										refs.AddWithFile(ref, uses, filename)
									case strings.Contains(uses.Value, "@"):
										ref := resolver.NormalizeActionsRef(uses.Value)
										refs.AddWithFile(ref, uses, filename)
									}
								}
							}
						}
					}
				}
			}

			// jobs: keyword
			if topLevelMap.Value == "jobs" {
				jobs := docMap.Content[i+1]
				if jobs.Kind != yaml.MappingNode {
					continue
				}

				for _, jobMap := range jobs.Content {
					if jobMap.Kind != yaml.MappingNode {
						continue
					}

					for j, sub := range jobMap.Content {
						// Container reference for running the job
						if sub.Value == "container" {
							containerMap := jobMap.Content[j+1]
							for k, property := range containerMap.Content {
								if property.Value == "image" {
									image := containerMap.Content[k+1]
									if strings.Contains(image.Value, "${{") {
										continue
									}
									ref := resolver.NormalizeContainerRef(image.Value)
									refs.AddWithFile(ref, image, filename)
									break
								}
							}
						}

						// CI service container
						if sub.Value == "services" {
							servicesMap := jobMap.Content[j+1]
							for _, subMap := range servicesMap.Content {
								if subMap.Kind != yaml.MappingNode {
									continue
								}
								for k, property := range subMap.Content {
									if property.Value == "image" {
										image := subMap.Content[k+1]
										if strings.Contains(image.Value, "${{") {
											continue
										}
										ref := resolver.NormalizeContainerRef(image.Value)
										refs.AddWithFile(ref, image, filename)
										break
									}
								}
							}
						}

						// List of steps
						if sub.Value == "steps" {
							steps := jobMap.Content[j+1]
							for _, step := range steps.Content {
								if step.Kind != yaml.MappingNode {
									continue
								}

								for k, property := range step.Content {
									if property.Value == "uses" {
										uses := step.Content[k+1]
										if strings.Contains(uses.Value, "${{") {
											continue
										}

										switch {
										case strings.HasPrefix(uses.Value, "docker://"):
											ref := resolver.NormalizeContainerRef(uses.Value)
											refs.AddWithFile(ref, uses, filename)
										case strings.Contains(uses.Value, "@"):
											ref := resolver.NormalizeActionsRef(uses.Value)
											refs.AddWithFile(ref, uses, filename)
										}
									}
								}
							}
						}

						// Top-level uses, likely for a reusable workflow.
						if sub.Value == "uses" {
							uses := jobMap.Content[j+1]
							if strings.Contains(uses.Value, "${{") {
								continue
							}

							switch {
							case strings.HasPrefix(uses.Value, "docker://"):
								ref := resolver.NormalizeContainerRef(uses.Value)
								refs.AddWithFile(ref, uses, filename)
							case strings.Contains(uses.Value, "@"):
								ref := resolver.NormalizeActionsRef(uses.Value)
								refs.AddWithFile(ref, uses, filename)
							}
						}
					}
				}
			}
		}
	}

	return nil
}

// CircleCI implements the Parser interface for CircleCI workflows.
type CircleCI struct{}

func (p *CircleCI) DenormalizeRef(ref string) string {
	return resolver.DenormalizeRef(ref)
}

func (p *CircleCI) Parse(nodes map[string]*yaml.Node) (*RefsList, error) {
	m := new(RefsList)

	for filename, document := range nodes {
		if err := p.parseNode(document, filename, m); err != nil {
			return nil, err
		}
	}

	return m, nil
}

func (p *CircleCI) parseNode(node *yaml.Node, filename string, m *RefsList) error {
	if node == nil {
		return nil
	}

	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			if err := p.parseNode(child, filename, m); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i < len(node.Content)-1; i += 2 {
			keyNode := node.Content[i]
			valueNode := node.Content[i+1]

			if keyNode.Kind == yaml.ScalarNode && keyNode.Value == "image" {
				if valueNode.Kind == yaml.ScalarNode && valueNode.Value != "" {
					ref := resolver.NormalizeContainerRef(valueNode.Value)
					m.AddWithFile(ref, valueNode, filename)
				}
			}

			if err := p.parseNode(valueNode, filename, m); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if err := p.parseNode(child, filename, m); err != nil {
				return err
			}
		}
	}

	return nil
}

// CloudBuild implements the Parser interface for Google Cloud Build.
type CloudBuild struct{}

func (p *CloudBuild) DenormalizeRef(ref string) string {
	return resolver.DenormalizeRef(ref)
}

func (p *CloudBuild) Parse(nodes map[string]*yaml.Node) (*RefsList, error) {
	m := new(RefsList)

	for filename, document := range nodes {
		if err := p.parseNode(document, filename, m); err != nil {
			return nil, err
		}
	}

	return m, nil
}

func (p *CloudBuild) parseNode(node *yaml.Node, filename string, m *RefsList) error {
	if node == nil {
		return nil
	}

	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			if err := p.parseNode(child, filename, m); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i < len(node.Content)-1; i += 2 {
			keyNode := node.Content[i]
			valueNode := node.Content[i+1]

			if keyNode.Kind == yaml.ScalarNode && keyNode.Value == "name" {
				if valueNode.Kind == yaml.ScalarNode && valueNode.Value != "" {
					ref := resolver.NormalizeContainerRef(valueNode.Value)
					m.AddWithFile(ref, valueNode, filename)
				}
			}

			if err := p.parseNode(valueNode, filename, m); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if err := p.parseNode(child, filename, m); err != nil {
				return err
			}
		}
	}

	return nil
}

// Drone implements the Parser interface for Drone CI.
type Drone struct{}

func (p *Drone) DenormalizeRef(ref string) string {
	return resolver.DenormalizeRef(ref)
}

func (p *Drone) Parse(nodes map[string]*yaml.Node) (*RefsList, error) {
	m := new(RefsList)

	for filename, document := range nodes {
		if err := p.parseNode(document, filename, m); err != nil {
			return nil, err
		}
	}

	return m, nil
}

func (p *Drone) parseNode(node *yaml.Node, filename string, m *RefsList) error {
	if node == nil {
		return nil
	}

	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			if err := p.parseNode(child, filename, m); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i < len(node.Content)-1; i += 2 {
			keyNode := node.Content[i]
			valueNode := node.Content[i+1]

			if keyNode.Kind == yaml.ScalarNode && keyNode.Value == "image" {
				if valueNode.Kind == yaml.ScalarNode && valueNode.Value != "" {
					ref := resolver.NormalizeContainerRef(valueNode.Value)
					m.AddWithFile(ref, valueNode, filename)
				}
			}

			if err := p.parseNode(valueNode, filename, m); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if err := p.parseNode(child, filename, m); err != nil {
				return err
			}
		}
	}

	return nil
}

// GitLabCI implements the Parser interface for GitLab CI.
type GitLabCI struct{}

func (p *GitLabCI) DenormalizeRef(ref string) string {
	return resolver.DenormalizeRef(ref)
}

func (p *GitLabCI) Parse(nodes map[string]*yaml.Node) (*RefsList, error) {
	m := new(RefsList)

	for filename, document := range nodes {
		if err := p.parseNode(document, filename, m); err != nil {
			return nil, err
		}
	}

	return m, nil
}

func (p *GitLabCI) parseNode(node *yaml.Node, filename string, m *RefsList) error {
	if node == nil {
		return nil
	}

	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			if err := p.parseNode(child, filename, m); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i < len(node.Content)-1; i += 2 {
			keyNode := node.Content[i]
			valueNode := node.Content[i+1]

			if keyNode.Kind == yaml.ScalarNode && keyNode.Value == "image" {
				if valueNode.Kind == yaml.ScalarNode && valueNode.Value != "" {
					ref := resolver.NormalizeContainerRef(valueNode.Value)
					m.AddWithFile(ref, valueNode, filename)
				}
			}

			if err := p.parseNode(valueNode, filename, m); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if err := p.parseNode(child, filename, m); err != nil {
				return err
			}
		}
	}

	return nil
}

// Tekton implements the Parser interface for Tekton pipelines.
type Tekton struct{}

func (p *Tekton) DenormalizeRef(ref string) string {
	return resolver.DenormalizeRef(ref)
}

func (p *Tekton) Parse(nodes map[string]*yaml.Node) (*RefsList, error) {
	m := new(RefsList)

	for filename, document := range nodes {
		if err := p.parseNode(document, filename, m); err != nil {
			return nil, err
		}
	}

	return m, nil
}

func (p *Tekton) parseNode(node *yaml.Node, filename string, m *RefsList) error {
	if node == nil {
		return nil
	}

	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			if err := p.parseNode(child, filename, m); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i < len(node.Content)-1; i += 2 {
			keyNode := node.Content[i]
			valueNode := node.Content[i+1]

			if keyNode.Kind == yaml.ScalarNode && keyNode.Value == "image" {
				if valueNode.Kind == yaml.ScalarNode && valueNode.Value != "" {
					ref := resolver.NormalizeContainerRef(valueNode.Value)
					m.AddWithFile(ref, valueNode, filename)
				}
			}

			if err := p.parseNode(valueNode, filename, m); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if err := p.parseNode(child, filename, m); err != nil {
				return err
			}
		}
	}

	return nil
}

// ParserFactory maps parser names to constructors.
var parserFactory = map[string]func() Parser{
	"actions":    func() Parser { return new(Actions) },
	"circleci":   func() Parser { return new(CircleCI) },
	"cloudbuild": func() Parser { return new(CloudBuild) },
	"drone":      func() Parser { return new(Drone) },
	"gitlabci":   func() Parser { return new(GitLabCI) },
	"tekton":     func() Parser { return new(Tekton) },
}

// For returns the parser that corresponds to the given name.
func For(name string) (Parser, error) {
	if v, ok := parserFactory[name]; ok {
		return v(), nil
	}
	return nil, nil // Let caller handle unknown parser
}
