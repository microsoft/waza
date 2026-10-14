package models

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestFaultSourceDetachmentCaps(t *testing.T) {
	t.Run("depth", func(t *testing.T) {
		for _, depth := range []int{1000, 1001} {
			node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "fixture"}
			for range depth {
				node = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{node}}
			}
			captured, err := detachedFaultNode(node)
			if depth == 1000 {
				require.NoError(t, err)
				require.NotNil(t, captured)
			} else {
				require.ErrorContains(t, err, "expansion exceeds limits")
				require.Nil(t, captured)
			}
		}
	})
	t.Run("expanded nodes", func(t *testing.T) {
		for _, count := range []int{99999, 100000} {
			target := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "fixture"}
			node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			for range count {
				node.Content = append(node.Content, target)
			}
			captured, err := detachedFaultNode(node)
			if count == 99999 {
				require.NoError(t, err)
				require.Len(t, captured.Content, count)
			} else {
				require.ErrorContains(t, err, "expansion exceeds limits")
				require.Nil(t, captured)
			}
		}
	})
}

func TestFaultSourceMergeDepthCaps(t *testing.T) {
	for _, depth := range []int{1000, 1001} {
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "selected"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "fixture"},
		}}
		for range depth {
			node = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Tag: "!!merge", Value: "<<"}, node,
			}}
		}
		selected, lookupErr := faultDocumentValue(node, "selected")
		fields, fieldsErr := faultEffectiveFields(node, make(map[*yaml.Node]bool))
		if depth == 1000 {
			require.NoError(t, lookupErr)
			require.Equal(t, "fixture", selected.Value)
			require.NoError(t, fieldsErr)
			require.Len(t, fields, 2)
		} else {
			require.ErrorContains(t, lookupErr, "depth exceeds limits")
			require.Nil(t, selected)
			require.ErrorContains(t, fieldsErr, "depth limits")
			require.Nil(t, fields)
		}
	}
}
