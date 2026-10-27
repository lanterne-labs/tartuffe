// The voter's Merkle path, kept up to date from the mirror's public files
// (SPEC §5.1): the browser never asks for its own path, leaf or block.

import { DEPTH, big, hex, node, zeroHashes } from './crypto';
import type { Sources } from './sources';

const BLOCK = 256;
const BLOCK_LEVEL = 8;
const GROUP = 16;

export interface PathState {
	position: number;
	// siblings inside the block (levels 0..7), cached once the block is full.
	inner: string[] | null;
	// The path as last computed, and the root it gives.
	path: string[];
	root: string;
	treeSize: number;
}

interface Files {
	roots: string[];
	open: string[];
	group: string[] | null;
}

// findPosition finds the leaf's position from the positions shard of its
// nibble (a restore: everyone fetches a whole shard), read at the mirror
// commit given (the newest checkpoint's: the latest files can be stale).
export async function findPosition(src: Sources, id: string, token = ''): Promise<number | null> {
	const shard = await src.json<Record<string, number>>(`positions/${id[2]}.json`, token);
	return shard?.[id] ?? null;
}

async function fetchFiles(src: Sources, token: string, position: number, needGroup: boolean): Promise<Files> {
	const [roots, open, group] = await Promise.all([
		src.json<string[]>('blocks/roots.json', token),
		src.json<string[]>('blocks/open.json', token),
		needGroup ? src.json<string[]>(`blocks/${Math.floor(position / (BLOCK * GROUP))}.json`, token) : Promise.resolve(null)
	]);
	return { roots: roots ?? [], open: open ?? [], group };
}

// subtreeRoot hashes a list of leaves (padded with zeros) up to a level.
function subtreeRoot(leaves: bigint[], level: number): bigint {
	const zeros = zeroHashes();
	let cur = leaves.slice();
	for (let l = 0; l < level; l++) {
		const next: bigint[] = [];
		for (let i = 0; i < cur.length; i += 2) next.push(node(cur[i], i + 1 < cur.length ? cur[i + 1] : zeros[l]));
		cur = next;
	}
	return cur[0] ?? zeros[level];
}

// innerSiblings computes the siblings of a leaf within its block.
function innerSiblings(blockLeaves: bigint[], offset: number): bigint[] {
	const zeros = zeroHashes();
	const out: bigint[] = [];
	let cur = blockLeaves.slice();
	let idx = offset;
	for (let l = 0; l < BLOCK_LEVEL; l++) {
		const sib = idx ^ 1;
		out.push(sib < cur.length ? cur[sib] : zeros[l]);
		const next: bigint[] = [];
		for (let i = 0; i < cur.length; i += 2) next.push(node(cur[i], i + 1 < cur.length ? cur[i + 1] : zeros[l]));
		cur = next;
		idx >>= 1;
	}
	return out;
}

// updatePath recomputes the path of a leaf from the public files for the
// mirror commit given. The state's cached inner siblings are reused once
// the block is full.
export async function updatePath(src: Sources, token: string, state: PathState, leaf: string): Promise<PathState> {
	const myBlock = Math.floor(state.position / BLOCK);
	const needInner = state.inner === null;
	const files = await fetchFiles(src, token, state.position, needInner);
	const fullBlocks = files.roots.length;
	const treeSize = fullBlocks * BLOCK + files.open.length;
	let inner: bigint[];
	let blockLeaves: bigint[] | null = null;
	if (state.inner) {
		inner = state.inner.map(big);
	} else if (myBlock < fullBlocks) {
		// Hidden among the group's 16 blocks.
		const group = files.group ?? [];
		const start = (myBlock % GROUP) * BLOCK;
		blockLeaves = group.slice(start, start + BLOCK).map(big);
		inner = innerSiblings(blockLeaves, state.position % BLOCK);
	} else {
		blockLeaves = files.open.map(big);
		inner = innerSiblings(blockLeaves, state.position % BLOCK);
	}
	if (blockLeaves && blockLeaves[state.position % BLOCK] !== big(leaf)) throw new Error('the mirror does not hold my leaf at my position');
	// Block-level nodes: the full roots, the open block's root, zeros.
	const zeros = zeroHashes();
	const blockNodes = files.roots.map(big);
	if (files.open.length > 0) blockNodes.push(subtreeRoot(files.open.map(big), BLOCK_LEVEL));
	const upper: bigint[] = [];
	let cur = blockNodes;
	let idx = myBlock;
	for (let l = BLOCK_LEVEL; l < DEPTH; l++) {
		const sib = idx ^ 1;
		upper.push(sib < cur.length ? cur[sib] : zeros[l]);
		const next: bigint[] = [];
		for (let i = 0; i < cur.length; i += 2) next.push(node(cur[i], i + 1 < cur.length ? cur[i + 1] : zeros[l]));
		cur = next;
		idx >>= 1;
	}
	const path = [...inner, ...upper];
	let root = big(leaf);
	for (let i = 0; i < DEPTH; i++) root = (state.position >> i) & 1 ? node(path[i], root) : node(root, path[i]);
	return {
		position: state.position,
		inner: myBlock < fullBlocks ? inner.map(hex) : null,
		path: path.map(hex),
		root: hex(root),
		treeSize
	};
}
