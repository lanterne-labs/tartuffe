package ballot

import (
	"errors"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"digital-mirror/backend/vote/poseidon2"
)

// Node hashes two children: H(T_NODE, l, r).
func Node(l, r fr.Element) fr.Element { return poseidon2.Hash(TagNode, l, r) }

// ID is a voter's leaf, H(T_ID, event, s).
func ID(event, s fr.Element) fr.Element { return poseidon2.Hash(TagID, event, s) }

// Nullifier is a voter's pseudonym, H(T_NULL, event, s).
func Nullifier(event, s fr.Element) fr.Element { return poseidon2.Hash(TagNull, event, s) }

// Tree is the append-only voter tree of fixed depth (SPEC §5): leaves at
// positions 0, 1, …, empty leaves 0, every level kept so that a path is a
// lookup.
type Tree struct {
	levels [Depth + 1][]fr.Element
	zero   [Depth + 1]fr.Element
}

// NewTree returns an empty tree.
func NewTree() *Tree {
	t := &Tree{}
	for i := 1; i <= Depth; i++ {
		t.zero[i] = Node(t.zero[i-1], t.zero[i-1])
	}
	return t
}

// Size is the number of leaves.
func (t *Tree) Size() int { return len(t.levels[0]) }

func (t *Tree) node(level, i int) fr.Element {
	if i < len(t.levels[level]) {
		return t.levels[level][i]
	}
	return t.zero[level]
}

// Root is the root over the leaves so far.
func (t *Tree) Root() fr.Element { return t.node(Depth, 0) }

// Append adds a leaf at the next position and returns that position.
func (t *Tree) Append(leaf fr.Element) (int, error) {
	pos := t.Size()
	if pos >= 1<<Depth {
		return 0, errors.New("ballot: the tree is full")
	}
	t.levels[0] = append(t.levels[0], leaf)
	i := pos
	for level := 1; level <= Depth; level++ {
		i >>= 1
		h := Node(t.node(level-1, 2*i), t.node(level-1, 2*i+1))
		if i < len(t.levels[level]) {
			t.levels[level][i] = h
		} else {
			t.levels[level] = append(t.levels[level], h)
		}
	}
	return pos, nil
}

// Path returns the siblings of the leaf at pos, leaf level first.
func (t *Tree) Path(pos int) ([Depth]fr.Element, error) {
	var path [Depth]fr.Element
	if pos < 0 || pos >= t.Size() {
		return path, errors.New("ballot: no leaf at that position")
	}
	i := pos
	for level := 0; level < Depth; level++ {
		path[level] = t.node(level, i^1)
		i >>= 1
	}
	return path, nil
}

// RootFromPath recomputes a root from a leaf, its path and its position,
// as the circuit does.
func RootFromPath(leaf fr.Element, path [Depth]fr.Element, pos int) fr.Element {
	cur := leaf
	for level := 0; level < Depth; level++ {
		if pos>>uint(level)&1 == 1 {
			cur = Node(path[level], cur)
		} else {
			cur = Node(cur, path[level])
		}
	}
	return cur
}
