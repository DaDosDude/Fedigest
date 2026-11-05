// Theme editor: Simple mode edits the :root variables and a generated
// options block inside the stylesheet textarea, which stays the only thing
// saved. The preview iframe shows the textarea's CSS on the live site.
(function () {
	var form = document.getElementById('theme-editor');
	if (!form) return;
	var ta = form.querySelector('textarea[name=css]');
	var simple = form.querySelector('.theme-simple');
	var cssWrap = form.querySelector('.theme-css');
	var tabs = form.querySelector('.theme-mode');
	var previewBox = form.querySelector('.theme-preview');
	var iframe = document.getElementById('theme-preview-iframe');
	var frameWrap = form.querySelector('.theme-preview-frame');

	var START = '/* fedigest:simple-options ';
	var END = '/* fedigest:simple-options end */';
	var FONTS = {
		sans: 'system-ui, -apple-system, "Segoe UI", sans-serif',
		serif: 'Georgia, "Times New Roman", serif',
		mono: 'ui-monospace, "Cascadia Mono", "DejaVu Sans Mono", Consolas, monospace',
		vga: '"Fedigest VGA", "Courier New", monospace'
	};
	var LINES = {
		thin: ['solid', '1px'], thick: ['solid', '3px'], double: ['double', '3px'],
		dashed: ['dashed', '2px'], dotted: ['dotted', '2px'], none: ['none', '0']
	};

	// --- reading and writing the stylesheet ---

	function rootBlock(css) {
		var m = /:root\s*\{([^}]*)\}/.exec(css);
		return m ? { start: m.index, end: m.index + m[0].length, body: m[1] } : null;
	}
	function escapeRe(s) { return s.replace(/[-\/\\^$*+?.()|[\]{}]/g, '\\$&'); }
	function getVar(css, name) {
		var rb = rootBlock(css);
		if (!rb) return '';
		var m = new RegExp(escapeRe(name) + '\\s*:\\s*([^;]*);').exec(rb.body);
		return m ? m[1].trim() : '';
	}
	function setVar(css, name, value) {
		var rb = rootBlock(css);
		if (!rb) return ':root {\n\t' + name + ': ' + value + ';\n}\n\n' + css;
		var re = new RegExp('(' + escapeRe(name) + '\\s*:\\s*)([^;]*)(;)');
		var body = re.test(rb.body) ? rb.body.replace(re, function (_, a, b, c) { return a + value + c; })
			: rb.body.replace(/\s*$/, '') + '\n\t' + name + ': ' + value + ';\n';
		return css.slice(0, rb.start) + ':root {' + body + '}' + css.slice(rb.end);
	}
	function getOpts(css) {
		var i = css.indexOf(START);
		if (i < 0) return {};
		var j = css.indexOf('*/', i);
		try { return JSON.parse(css.slice(i + START.length, j)) || {}; } catch (e) { return {}; }
	}
	function optsCSS(o, css) {
		var out = [];
		// The VGA font needs its @font-face wherever --font-sans names it,
		// including after the menu is set back to "As in the stylesheet".
		if (o.font === 'vga' || /Fedigest VGA/.test(getVar(css || '', '--font-sans'))) {
			out.push('@font-face { font-family: "Fedigest VGA"; src: url("/themes/terminal/fonts/ibm-vga-8x16.woff") format("woff"); font-display: swap; }');
		}
		if (o.size) out.push('html { font-size: ' + o.size + 'px; }\nbody { font-size: 1rem; }');
		if (o.lines && LINES[o.lines]) {
			var s = LINES[o.lines][0], w = LINES[o.lines][1];
			out.push('.site-header, .sidebar-block, .pick-card, .category-index li,\n.comment-list:not(.comment-children) > .comment { border-bottom-style: ' + s + '; border-bottom-width: ' + w + '; border-bottom-color: var(--color-border); }');
			out.push('.comment-children { border-left-style: ' + s + '; border-left-width: ' + w + '; }');
			out.push('.post-link-out, details.spoiler, .spoiler-card { border-style: ' + s + '; border-width: ' + w + '; }');
		}
		if (o.buttons === 'filled') {
			out.push('button { background: var(--color-accent); color: ' + (o.buttonText || '#ffffff') + '; border: 2px solid var(--color-accent); }');
		} else if (o.buttons === 'outline') {
			out.push('button { background: transparent; color: var(--color-accent); border: 2px solid var(--color-accent); }');
		} else if (o.buttons === 'inverse') {
			out.push('button { background: var(--color-text); color: var(--color-bg); border: 2px solid var(--color-text); }');
		}
		return out.join('\n');
	}
	function setOpts(css, o) {
		var i = css.indexOf(START);
		if (i >= 0) {
			var j = css.indexOf(END, i);
			css = css.slice(0, i).replace(/\s*$/, '\n') + (j >= 0 ? css.slice(j + END.length).replace(/^\s*/, '') : '');
		}
		var clean = {};
		Object.keys(o).forEach(function (k) { if (o[k]) clean[k] = o[k]; });
		if (!clean.buttons) delete clean.buttonText;
		var body = optsCSS(clean, css);
		if (!body) return css.replace(/\s*$/, '\n');
		return css.replace(/\s*$/, '\n') + '\n' + START + JSON.stringify(clean) + ' */\n' + body + '\n' + END + '\n';
	}

	// --- colours ---

	var probe = document.createElement('canvas').getContext('2d');
	function toHex(v) {
		if (!v) return '#000000';
		probe.fillStyle = '#000000';
		probe.fillStyle = v;
		var c = probe.fillStyle;
		return /^#[0-9a-f]{6}$/i.test(c) ? c : '#000000';
	}

	// --- simple mode ---

	var opts = {};
	// Sliders only write their variable once moved, so a value the slider
	// can't show exactly (0.5rem, 48rem) isn't rewritten by other changes.
	var touched = {};
	function pxValue(v) {
		var m = /^(-?[\d.]+)(px|rem|em)?$/.exec((v || '').trim());
		if (!m) return 0;
		return Math.round(parseFloat(m[1]) * (m[2] === 'rem' || m[2] === 'em' ? 16 : 1));
	}
	function loadSimple() {
		touched = {};
		var css = ta.value;
		opts = getOpts(css);
		form.querySelectorAll('[data-var]').forEach(function (el) {
			var v = getVar(css, el.dataset.var);
			el.value = toHex(v);
			form.querySelector('[data-var-text="' + el.dataset.var + '"]').value = v;
		});
		form.querySelectorAll('[data-var-px]').forEach(function (el) {
			var raw = getVar(css, el.dataset.varPx);
			el.value = pxValue(raw);
			form.querySelector('[data-out="' + el.dataset.varPx + '"]').textContent = raw || (el.value + 'px');
		});
		form.querySelectorAll('[data-opt]').forEach(function (el) { el.value = opts[el.dataset.opt] || ''; });
		var bt = opts.buttonText || '#ffffff';
		form.querySelector('[data-opt-color=buttonText]').value = toHex(bt);
		form.querySelector('[data-opt-text=buttonText]').value = bt;
		var fontSel = form.querySelector('[data-opt=font]');
		if (!opts.font) {
			var cur = getVar(css, '--font-sans');
			Object.keys(FONTS).forEach(function (k) { if (FONTS[k] === cur) fontSel.value = k; });
		}
		showButtonText();
	}
	function showButtonText() {
		var el = form.querySelector('[data-show-for=filled]');
		el.hidden = form.querySelector('[data-opt=buttons]').value !== 'filled';
	}
	function writeSimple() {
		var css = ta.value;
		form.querySelectorAll('[data-var-text]').forEach(function (el) {
			if (el.value.trim()) css = setVar(css, el.dataset.varText, el.value.trim());
		});
		form.querySelectorAll('[data-var-px]').forEach(function (el) {
			if (!touched[el.dataset.varPx]) return;
			css = setVar(css, el.dataset.varPx, el.value + 'px');
			form.querySelector('[data-out="' + el.dataset.varPx + '"]').textContent = el.value + 'px';
		});
		var o = {};
		form.querySelectorAll('[data-opt]').forEach(function (el) { o[el.dataset.opt] = el.value; });
		o.buttonText = form.querySelector('[data-opt-text=buttonText]').value.trim();
		if (o.font && FONTS[o.font]) css = setVar(css, '--font-sans', FONTS[o.font]);
		ta.value = setOpts(css, o);
		showButtonText();
		preview();
	}
	simple.addEventListener('input', function (e) {
		var t = e.target;
		if (t.dataset.varPx) touched[t.dataset.varPx] = true;
		if (t.dataset.var) form.querySelector('[data-var-text="' + t.dataset.var + '"]').value = t.value;
		if (t.dataset.varText && /^#[0-9a-f]{6}$/i.test(t.value.trim())) form.querySelector('[data-var="' + t.dataset.varText + '"]').value = t.value.trim();
		if (t.dataset.optColor) form.querySelector('[data-opt-text="' + t.dataset.optColor + '"]').value = t.value;
		if (t.dataset.optText && /^#[0-9a-f]{6}$/i.test(t.value.trim())) form.querySelector('[data-opt-color="' + t.dataset.optText + '"]').value = t.value.trim();
		writeSimple();
	});
	simple.addEventListener('change', writeSimple);

	// --- modes ---

	function setMode(mode) {
		simple.hidden = mode !== 'simple';
		cssWrap.hidden = mode !== 'css';
		form.classList.toggle('mode-css', mode === 'css');
		tabs.querySelectorAll('[data-mode]').forEach(function (b) {
			b.classList.toggle('is-active', b.dataset.mode === mode);
			b.setAttribute('aria-pressed', b.dataset.mode === mode ? 'true' : 'false');
		});
		if (mode === 'simple') loadSimple();
	}
	tabs.addEventListener('click', function (e) {
		var b = e.target.closest('[data-mode]');
		if (b) setMode(b.dataset.mode);
	});

	// --- preview ---

	var timer;
	ta.addEventListener('input', function () { clearTimeout(timer); timer = setTimeout(preview, 300); });
	function preview() {
		var doc;
		try { doc = iframe.contentDocument; } catch (e) { return; }
		if (!doc || !doc.head) return;
		doc.querySelectorAll('link[rel=stylesheet]').forEach(function (l) {
			if (/\/themes?\//.test(l.getAttribute('href') || '')) l.disabled = true;
		});
		var st = doc.getElementById('fedigest-theme-preview');
		if (!st) {
			st = doc.createElement('style');
			st.id = 'fedigest-theme-preview';
			doc.head.appendChild(st);
		}
		st.textContent = ta.value;
	}
	iframe.addEventListener('load', preview);
	document.getElementById('theme-preview-page').addEventListener('change', function () { iframe.src = this.value; });

	var size = 'phone';
	function fit() {
		var w = frameWrap.clientWidth;
		var full = size === 'desktop' ? 1280 : 390;
		var scale = Math.min(1, w / full);
		iframe.style.width = full + 'px';
		iframe.style.transform = 'scale(' + scale + ')';
		var h = frameWrap.clientHeight;
		iframe.style.height = (h / scale) + 'px';
		iframe.style.marginLeft = size === 'phone' && w > full ? ((w - full) / 2) + 'px' : '0';
	}
	form.querySelector('.theme-preview-size').addEventListener('click', function (e) {
		var b = e.target.closest('[data-size]');
		if (!b) return;
		size = b.dataset.size;
		this.querySelectorAll('[data-size]').forEach(function (x) {
			x.classList.toggle('is-active', x === b);
			x.setAttribute('aria-pressed', x === b ? 'true' : 'false');
		});
		fit();
	});
	window.addEventListener('resize', fit);

	tabs.hidden = false;
	previewBox.hidden = false;
	form.classList.add('has-js');
	setMode('simple');
	fit();
})();
