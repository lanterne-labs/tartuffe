// The anchor a ballot proves against (SPEC §5): the newest checkpoint
// within the anchor age that holds the leaf; a first ballot prefers one
// that R_MIN later registrants share.
import { describe, it, expect } from 'vitest';
import { chooseAnchor, parseCheckpoint, type CheckpointInfo } from './protocol';

const cp = (index: number, treeSize: number, time: number): CheckpointInfo => ({ note: '', logSize: 0, treeRoot: `0x${index}`, treeSize, index, event: 1, time, mirror: `m${index}` });

describe('chooseAnchor', () => {
	const now = 10_000;
	const list = [cp(5, 100, 9_900), cp(4, 80, 9_000), cp(3, 60, 8_000), cp(2, 50, 7_000), cp(1, 40, 1_000)];

	it('takes the newest checkpoint for a later ballot', () => {
		expect(chooseAnchor(list, 70, false, 32, 86_400, now)?.index).toBe(5);
	});

	it('skips checkpoints that do not hold the leaf yet, and stale ones', () => {
		expect(chooseAnchor(list, 99, false, 32, 86_400, now)?.index).toBe(5);
		expect(chooseAnchor(list, 100, false, 32, 86_400, now)).toBeNull();
		expect(chooseAnchor(list, 30, false, 32, 500, now)?.index).toBe(5);
		expect(chooseAnchor([cp(1, 40, 1_000)], 30, false, 32, 500, now)).toBeNull();
	});

	it('for a first ballot, prefers a root at least R_MIN later registrants share, else the newest', () => {
		// Position 60: the newest tree (100) has 39 leaves after it.
		expect(chooseAnchor(list, 60, true, 32, 86_400, now)?.index).toBe(5);
		// Position 90: only 9 leaves after it anywhere: the newest root.
		expect(chooseAnchor(list, 90, true, 32, 86_400, now)?.index).toBe(5);
		// Position 67: 32 after it in the newest tree exactly.
		expect(chooseAnchor(list, 67, true, 32, 86_400, now)?.index).toBe(5);
		expect(chooseAnchor(list, 68, true, 32, 86_400, now)?.index).toBe(5);
	});
});

describe('parseCheckpoint', () => {
	it('reads the lines of a note', () => {
		const note = 'tartuffe.me/vote-log\n280\nabc=\ntree 0x19b3 37\nindex 56\nevent 5\ntime 1793074568\nmirror dir-8340\n\n— sig\n';
		const info = parseCheckpoint(note);
		expect(info).toMatchObject({ logSize: 280, treeRoot: '0x19b3', treeSize: 37, index: 56, event: 5, time: 1793074568, mirror: 'dir-8340' });
	});
});
