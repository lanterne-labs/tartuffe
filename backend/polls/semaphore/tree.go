package semaphore

import (
	"fmt"
	"math/big"

	"github.com/iden3/go-iden3-crypto/poseidon"
)

// Tree is a LeanIMT of commitments: a binary Merkle tree hashed with
// Poseidon, where a node without a right sibling goes up unchanged, so the
// tree has no zero leaves and its depth is the one its size needs. It keeps
// every node: an insertion costs one hash per level.
type Tree struct {
	// nodes[0] are the leaves, nodes[depth] the root.
	nodes [][]*big.Int
}

// NewTree builds the tree of the leaves, in order.
func NewTree(leaves []*big.Int) (*Tree, error) {
	t := &Tree{nodes: [][]*big.Int{{}}}
	for _, l := range leaves {
		if err := t.Insert(l); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// Size is the number of leaves.
func (t *Tree) Size() int { return len(t.nodes[0]) }

// Depth is the number of levels above the leaves.
func (t *Tree) Depth() int { return len(t.nodes) - 1 }

// Root is the root, 0 for an empty tree (as in @zk-kit/lean-imt).
func (t *Tree) Root() *big.Int {
	top := t.nodes[len(t.nodes)-1]
	if len(top) == 0 {
		return new(big.Int)
	}
	return top[0]
}

// Insert adds a leaf after the others.
func (t *Tree) Insert(leaf *big.Int) error {
	if leaf.Sign() < 0 || leaf.Cmp(FieldModulus) >= 0 {
		return fmt.Errorf("%w: the leaf is not a field element", ErrInvalid)
	}
	index := len(t.nodes[0])
	if index > 0 && 1<<t.Depth() < index+1 {
		t.nodes = append(t.nodes, []*big.Int{})
	}
	t.nodes[0] = append(t.nodes[0], leaf)
	node := leaf
	for level := 0; level < t.Depth(); level++ {
		if index&1 == 1 {
			h, err := hash2(t.nodes[level][index-1], node)
			if err != nil {
				return fmt.Errorf("poseidon: %w", err)
			}
			node = h
		}
		index >>= 1
		if index < len(t.nodes[level+1]) {
			t.nodes[level+1][index] = node
		} else {
			t.nodes[level+1] = append(t.nodes[level+1], node)
		}
	}
	return nil
}

func hash2(a, b *big.Int) (*big.Int, error) { return poseidon.Hash([]*big.Int{a, b}) }

// Leaves are the leaves, in order (not to be changed).
func (t *Tree) Leaves() []*big.Int { return t.nodes[0] }
