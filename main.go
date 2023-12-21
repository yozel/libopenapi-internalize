package main

import (
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/pb33f/libopenapi/index"
	"github.com/pb33f/libopenapi/utils"
	"gopkg.in/yaml.v3"
)

func nodeWalk(index *index.SpecIndex, parentNode *yaml.Node, node *yaml.Node, do func(*yaml.Node, *yaml.Node) error) error {
	err := do(parentNode, node)
	if err != nil {
		return err
	}
	switch node.Kind {
	case yaml.DocumentNode:
		err = nodeWalk(index, node, node.Content[0], do)
		if err != nil {
			return err
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			err = nodeWalk(index, node, child, do)
			if err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i < len(node.Content); i += 2 {
			err = nodeWalk(index, node, node.Content[i+1], do)
			if err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
	case yaml.AliasNode:
		return fmt.Errorf("unsupported node type: %d", node.Kind)
	}
	return nil
}

func genRolodexForNode(node *yaml.Node) (*index.Rolodex, error) {
	// create a new config that does not allow lookups.
	indexConfig := &index.SpecIndexConfig{
		AllowRemoteLookup: true,
		AllowFileLookup:   true,
		BasePath:          ".",
	}

	fsCfg := &index.LocalFSConfig{
		BaseDirectory: "./",
		IndexConfig:   indexConfig,
	}
	// create a local file system using config.
	fileFS, err := index.NewLocalFSWithConfig(fsCfg)
	if err != nil {
		return nil, err
	}

	// create a new rolodex
	rolodex := index.NewRolodex(indexConfig)

	rolodex.AddLocalFS("./", fileFS)

	// set the rolodex root node to the root node of the spec.
	rolodex.SetRootNode(node)
	err = rolodex.IndexTheRolodex()
	if err != nil {
		return nil, err
	}

	return rolodex, nil
}

func getAllRefs(rolodex *index.Rolodex) map[string]*index.Reference {
	results := map[string]*index.Reference{}
	rolodex.GetRootIndex().BuildIndex()
	for k, v := range rolodex.GetRootIndex().GetMappedReferences() {
		results[k] = v
	}

	for _, index := range rolodex.GetIndexes() {
		index.BuildIndex()
		for k, v := range index.GetMappedReferences() {
			results[k] = v
		}
	}

	return results
}

func parseComponentPath(path string) (string, string, error) {
	if !strings.HasPrefix(path, "$.components.") {
		return "", "", fmt.Errorf("path must start with $. got %s", path)
	}
	path = strings.TrimPrefix(path, "$.components.")
	paths := strings.Split(path, ".")
	if len(paths) != 2 {
		return "", "", fmt.Errorf("invalid path")
	}
	return paths[0], paths[1], nil
}

func parsePath(path string) ([]string, error) {
	if !strings.HasPrefix(path, "$.") {
		return nil, fmt.Errorf("path must start with $. got %s", path)
	}
	return strings.Split(path, ".")[1:], nil
}

func getKeys(node *yaml.Node) (map[string]bool, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("node is not a mapping node")
	}
	if len(node.Content)%2 != 0 {
		return nil, fmt.Errorf("node is not a valid mapping node")
	}
	keys := map[string]bool{}
	for i := 0; i < len(node.Content); i += 2 {
		keys[node.Content[i].Value] = true
	}
	return keys, nil
}

func addAsNewComponent(root *yaml.Node, ref *index.Reference) (*index.Reference, error) {
	componentType, itemName, err := parseComponentPath(ref.Path)
	if err != nil {
		return nil, err
	}

	item := ref.Node

	components, err := getOneNode(root, "$.components")
	if err != nil {
		return nil, fmt.Errorf("unable to find components")
	}

	if components == nil {
		components = utils.CreateEmptyMapNode()
		root.Content[0].Content = append(root.Content[0].Content, utils.CreateStringNode("components"))
		root.Content[0].Content = append(root.Content[0].Content, components)
	}

	componentContainer, err := getOneNode(root, "$.components."+componentType)
	if err != nil {
		return nil, fmt.Errorf("unable to find components")
	}
	if componentContainer == nil {
		componentContainer = utils.CreateEmptyMapNode()
		components.Content = append(components.Content, utils.CreateStringNode(componentType))
		components.Content = append(components.Content, componentContainer)
	}

	keys, err := getKeys(componentContainer)
	if err != nil {
		return nil, err
	}
	for keys[itemName] {
		itemName = itemName + "X"
	}

	newDefinition := fmt.Sprintf("#/components/%s/%s", componentType, itemName)

	componentContainer.Content = append(componentContainer.Content, utils.CreateStringNode(itemName))
	componentContainer.Content = append(componentContainer.Content, item)

	itemName, path := utils.ConvertComponentIdIntoPath(newDefinition)

	return &index.Reference{
		FullDefinition: newDefinition,
		Definition:     newDefinition,
		Name:           itemName,
		Node:           ref.Node,
		Path:           path,
	}, nil
}

func getOneNode(node *yaml.Node, path string) (*yaml.Node, error) {
	r, err := utils.FindNodesWithoutDeserializing(node, path)
	if err != nil {
		return nil, err
	}
	if len(r) == 0 {
		return nil, nil
	} else if len(r) > 1 {
		return nil, fmt.Errorf("expected 1 node, got %d", len(r))
	}
	return r[0], nil
}

func main() {
	// read in an OpenAPI Spec to a byte array
	specBytes, err := os.ReadFile("spec.yaml")
	if err != nil {
		panic(err.Error())
	}

	rootNode := &yaml.Node{}
	err = yaml.Unmarshal(specBytes, rootNode)
	if err != nil {
		panic(err.Error())
	}

	err = internalize(rootNode)
	if err != nil {
		panic(err.Error())
	}

	b, err := yaml.Marshal(rootNode)
	if err != nil {
		panic(err.Error())
	}

	fmt.Printf("%s", b)
}

func internalize(root *yaml.Node) error {
	rolodex, err := genRolodexForNode(root)
	if err != nil {
		panic(err.Error())
	}

	mapping := map[string]string{}
	for _, ref := range getAllRefs(rolodex) {
		if !ref.IsRemote {
			continue
		}
		_, _, err := parseComponentPath(ref.Path)
		if err != nil {
			continue
		}
		newRef, err := addAsNewComponent(root, ref)
		if err != nil {
			return fmt.Errorf("unable to add as new component %w", err)
		}
		if newRef != nil {
			mapping[ref.FullDefinition] = newRef.FullDefinition
		}
	}

	// replace all references with new references
	return nodeWalk(nil, nil, root, func(parentNode *yaml.Node, node *yaml.Node) error {
		if node.Kind != yaml.MappingNode {
			return nil
		}

		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Value != "$ref" {
				continue
			}
			oldRefNode := node.Content[i+1]

			// If the reference is a local reference, then we don't need to do anything.
			if strings.Split(oldRefNode.Value, "#")[0] == "" {
				continue
			}

			origin := rolodex.FindNodeOrigin(node)
			if origin == nil {
				return fmt.Errorf("unable to find origin for node")
			}
			oldRefFull := path.Join(path.Dir(origin.AbsoluteLocation), oldRefNode.Value)
			if newRef, ok := mapping[oldRefFull]; ok {
				oldRefNode.Value = newRef
			} else {
				// The reference is not an component, so the only option is resolving it with the rolodex
				rolodex, err := genRolodexForNode(&yaml.Node{
					Kind: yaml.DocumentNode,
					Content: []*yaml.Node{
						parentNode,
					},
					Line:   0,
					Column: 0,
				})
				if err != nil {
					return err
				}
				rolodex.Resolve()
			}

		}
		return nil
	})
}
