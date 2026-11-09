#!/usr/bin/env node
// The mobile audit: renders the site's pages at phone widths in a headless
// Chrome and reports what breaks on a phone: a horizontal overflow (the page
// scrolls sideways), the elements that stick out of the viewport, the links
// and buttons too small for a finger, the text under 11px, and the page's
// errors. Full-page screenshots go to the output directory, one per page and
// width, for a look by eye.
//
//   npm run audit:mobile                          # the dev server, http://localhost:3000
//   npm run audit:mobile -- https://tartuffe.me   # the site
//   AUDIT_PATHS="/,/feed,/person/1" npm run audit:mobile
//   AUDIT_OUT=/tmp/shots npm run audit:mobile
//
// Needs Google Chrome (CHROME names another binary). The browser renders on
// the CPU (SwiftShader): never give it the GPU on this machine.
import puppeteer from 'puppeteer-core';
import { mkdirSync } from 'node:fs';
import { existsSync } from 'node:fs';

const base = (process.argv[2] ?? 'http://localhost:3000').replace(/\/$/, '');
const out = process.env.AUDIT_OUT ?? 'mobile-audit';
const CHROME =
	process.env.CHROME ??
	['/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser'].find(existsSync);
if (!CHROME) {
	console.error('no Chrome found: set CHROME to its binary');
	process.exit(2);
}
const PATHS = (process.env.AUDIT_PATHS ?? '/,/dashboard,/feed,/person/1,/about,/election/presidentielle-2027,/election/presidentielle-2027/voter,/election/presidentielle-2027/fonctionnement,/election/presidentielle-2027/verifier,/corrections,/contact,/lab,/profile')
	.split(',')
	.map((s) => s.trim())
	.filter(Boolean);
const WIDTHS = (process.env.AUDIT_WIDTHS ?? '360,375').split(',').map(Number);
const UA = 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1';

mkdirSync(out, { recursive: true });
const browser = await puppeteer.launch({
	executablePath: CHROME,
	headless: true,
	args: ['--use-angle=swiftshader', '--enable-unsafe-swiftshader', '--no-first-run', '--lang=fr-FR']
});
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Runs in the page. The fixed elements (the nav, the event bar) and what an
// ancestor clips are not overflow; a target is small under 32px either way.
const audit = () => {
	const vw = document.documentElement.clientWidth;
	const sw = document.documentElement.scrollWidth;
	const name = (el) => `${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}${typeof el.className === 'string' && el.className.trim() ? '.' + el.className.trim().split(/\s+/).filter((c) => !/^s(velte)?-/.test(c)).slice(0, 3).join('.') : ''}`;
	const out = [], tiny = [], small = new Set();
	for (const el of document.querySelectorAll('body *')) {
		const r = el.getBoundingClientRect();
		if (r.width === 0 || r.height === 0) continue;
		const cs = getComputedStyle(el);
		if (cs.visibility === 'hidden' || cs.display === 'none') continue;
		if (r.right > vw + 1 || r.left < -1) {
			let p = el.parentElement, clipped = false;
			while (p && p !== document.body) {
				if (/(hidden|clip|auto|scroll)/.test(getComputedStyle(p).overflowX)) { clipped = true; break; }
				p = p.parentElement;
			}
			if (!clipped) out.push(`${name(el)} left=${Math.round(r.left)} right=${Math.round(r.right)}`);
		}
		if ((el.tagName === 'A' || el.tagName === 'BUTTON') && (r.width < 32 || r.height < 32)) {
			tiny.push(`${name(el)} ${Math.round(r.width)}x${Math.round(r.height)} "${(el.textContent || el.getAttribute('aria-label') || '').trim().slice(0, 30)}"`);
		}
		const fs = parseFloat(cs.fontSize);
		if (fs && fs < 11 && [...el.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim())) small.add(`${name(el)} ${fs}px`);
	}
	return { vw, sw, h: document.documentElement.scrollHeight, out: out.slice(0, 30), tiny: [...new Set(tiny)].slice(0, 30), small: [...small].slice(0, 20) };
};

let problems = 0;
for (const path of PATHS) {
	for (const width of WIDTHS) {
		const page = await browser.newPage();
		await page.setUserAgent(UA);
		await page.setViewport({ width, height: 740, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
		const errors = [];
		page.on('pageerror', (e) => errors.push(String(e)));
		page.on('console', (m) => m.type() === 'error' && errors.push(m.text()));
		const slug = path.replace(/[^a-z0-9]+/gi, '-').replace(/^-|-$/g, '') || 'home';
		try {
			await page.goto(base + path, { waitUntil: 'networkidle2', timeout: 90000 });
			await sleep(800);
			// Scroll through: sections that fade in on scroll, lazy lists.
			const max = Math.min(await page.evaluate(() => document.documentElement.scrollHeight), 12000);
			for (let y = 0; y < max; y += 500) {
				await page.evaluate((y) => window.scrollTo(0, y), y);
				await sleep(80);
			}
			await page.evaluate(() => window.scrollTo(0, 0));
			await sleep(400);
			const res = await page.evaluate(audit);
			await page.screenshot({ path: `${out}/${slug}-${width}.png`, fullPage: true });
			const overflow = res.sw > res.vw;
			if (overflow) problems++;
			console.log(`\n== ${path} @ ${width}px  height ${res.h}px  ${overflow ? `OVERFLOW: page is ${res.sw}px wide` : 'no overflow'}`);
			if (res.out.length) console.log('  sticks out:\n    ' + res.out.join('\n    '));
			if (res.tiny.length) console.log('  small targets (<32px):\n    ' + res.tiny.join('\n    '));
			if (res.small.length) console.log('  small text (<11px):\n    ' + res.small.join('\n    '));
			if (errors.length) console.log('  errors:\n    ' + [...new Set(errors)].slice(0, 6).join('\n    '));
		} catch (e) {
			problems++;
			console.log(`\n== ${path} @ ${width}px  FAILED: ${e.message}`);
		}
		await page.close();
	}
}
await browser.close();
console.log(`\n${problems ? `${problems} page(s) with a horizontal overflow or a failure` : 'no horizontal overflow'}; screenshots in ${out}/`);
process.exit(problems ? 1 : 0);
