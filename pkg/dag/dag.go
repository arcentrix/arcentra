// Copyright 2025 Arcentra Team
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dag

import (
	"strings"

	"github.com/pkg/errors"
)

// DAG represents a directed acyclic graph.
type DAG struct {
	// Nodes represents map of name to Node in DAG.
	Nodes map[string]*defaultNode
	// allowMarkArbitraryNodesAsDone allows any node in the DAG to be marked as done;
	// otherwise, all nodes on the preceding path must be marked as done
	allowMarkArbitraryNodesAsDone bool
	// allowNotCheckCycle allows checking for cycles (performance issue)
	allowNotCheckCycle bool
}

// NamedNode is a convenience interface for users, only used when creating a DAG
type NamedNode interface {
	// NodeName uniquely identifies a node
	NodeName() string
	// PrevNodeNames represents the immediately preceding nodes connected to the current node
	PrevNodeNames() []string
}

// Node represents a node in the DAG
type Node interface {
	NamedNode
	PrevNodes() []Node
	NextNodes() []Node
	NextNodeNames() []string
}

type Option func(*DAG)

func WithAllowMarkArbitraryNodesAsDone(allow bool) Option {
	return func(g *DAG) {
		g.allowMarkArbitraryNodesAsDone = allow
	}
}

func WithAllowNotCheckCycle(allow bool) Option {
	return func(g *DAG) {
		g.allowNotCheckCycle = allow
	}
}

// New returns a DAG
// @nodes: map[node name]NamedNode
func New(nodes []NamedNode, ops ...Option) (*DAG, error) {
	g := DAG{
		Nodes: map[string]*defaultNode{},
	}

	// apply ops
	for _, op := range ops {
		op(&g)
	}

	// initialize DAG
	for _, n := range nodes {
		if err := g.addNode(n); err != nil {
			return nil, errors.Errorf("failed to add node %q to DAG, err: %v", n.NodeName(), err)
		}
	}

	// add links between nodes
	for _, n := range g.Nodes {
		for _, prevNodeName := range n.PrevNodeNames() {
			if err := g.addLink(n, prevNodeName); err != nil {
				return nil, errors.Errorf("failed to add link between %q and %q, err: %v", n.NodeName(), prevNodeName, err)
			}
		}
	}
	return &g, nil
}

func (g *DAG) addNode(n NamedNode) error {
	if _, ok := g.Nodes[n.NodeName()]; ok {
		return errors.Errorf("duplicate node: %s", n.NodeName())
	}
	g.Nodes[n.NodeName()] = &defaultNode{name: n.NodeName(), prevNodeNames: n.PrevNodeNames()}
	return nil
}

func (g *DAG) addLink(n Node, prevNodeName string) error {
	// find previous node
	prevNode, ok := g.Nodes[prevNodeName]
	if !ok {
		return errors.Errorf("node %q depends on an nonexistent node %q", n.NodeName(), prevNodeName)
	}
	// link two nodes
	if err := g.linkTwoNodes(prevNode, n); err != nil {
		return errors.Errorf("failed to create link from %q to %q, err: %v", prevNode.NodeName(), n.NodeName(), err)
	}
	return nil
}

func (g *DAG) linkTwoNodes(from, to Node) error {
	if !g.allowNotCheckCycle {
		if err := validateNodes(from, to); err != nil {
			return err
		}
	}
	// link node
	to.(*defaultNode).prevNodes = append(to.(*defaultNode).prevNodes, from.(*defaultNode))
	from.(*defaultNode).nextNodes = append(from.(*defaultNode).nextNodes, to.(*defaultNode))
	return nil
}

func validateNodes(from, to Node) error {
	// check for self cycle
	if from.NodeName() == to.NodeName() {
		return errors.Errorf("self cycle detected: node %q depends on itself", from.NodeName())
	}

	// check for cycle
	path := []string{to.NodeName(), from.NodeName()}
	if err := visit(to, from.PrevNodes(), path); err != nil {
		return errors.Errorf("cycle detected: %v", err)
	}

	return nil
}

func visit(startNode Node, prev []Node, visitedPath []string) error {
	for _, n := range prev {
		visitedPath = append(visitedPath, n.NodeName())
		if n.NodeName() == startNode.NodeName() {
			return errors.Errorf("%s", getVisitedPath(visitedPath))
		}
		if err := visit(startNode, n.PrevNodes(), visitedPath); err != nil {
			return err
		}
	}
	return nil
}

func getVisitedPath(path []string) string {
	// reverse the path since we traversed the DAG using prev pointers.
	for i := len(path)/2 - 1; i >= 0; i-- {
		opp := len(path) - 1 - i
		path[i], path[opp] = path[opp], path[i]
	}
	return strings.Join(path, " -> ")
}

type defaultNode struct {
	name          string
	prevNodeNames []string

	prevNodes []*defaultNode
	nextNodes []*defaultNode
}

func (n *defaultNode) NodeName() string {
	return n.name
}

func (n *defaultNode) PrevNodeNames() []string {
	return n.prevNodeNames
}

func (n *defaultNode) PrevNodes() []Node {
	r := make([]Node, 0, len(n.prevNodes))
	for _, prev := range n.prevNodes {
		r = append(r, prev)
	}
	return r
}

func (n *defaultNode) NextNodeNames() []string {
	r := make([]string, 0, len(n.nextNodes))
	for _, next := range n.nextNodes {
		r = append(r, next.name)
	}
	return r
}

func (n *defaultNode) NextNodes() []Node {
	r := make([]Node, 0, len(n.nextNodes))
	for _, next := range n.nextNodes {
		r = append(r, next)
	}
	return r
}
