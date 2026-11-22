'use strict';
// App themes. A theme is a set of colour values (the CSS variables of app.css)
// plus a few looks (corner radius, label font, glow). Each theme has colour
// options (usually accents) and some also a light and a dark base.
// Loaded first and applied at once, so the page never flashes the wrong colours.
(function (root) {
  const SERIF = '"Iowan Old Style", "Palatino Linotype", Palatino, Georgia, serif';
  const PIXEL = '"Press Start 2P", ui-monospace, "SF Mono", Menlo, Consolas, monospace';
  const SYS = 'system-ui, -apple-system, "Segoe UI", Roboto, sans-serif';
  const MONO = 'ui-monospace, "SF Mono", Menlo, Consolas, "DejaVu Sans Mono", monospace';

  // base colours: bg page, panel cards, art placeholders and tracks, text, dim text, line borders, error, heart, ok
  const DARK = (o) => Object.assign({ error: '#ef5350', heart: '#ff5a7a', ok: '#4cd48a', scheme: 'dark' }, o);
  const LIGHT = (o) => Object.assign({ error: '#d93025', heart: '#e0245e', ok: '#188038', scheme: 'light' }, o);

  const THEMES = [
    { id: 'cliamp', name: 'Spindle', blurb: 'The default look', look: { radius: '12px', font: SYS, skin: 'soft' },
      modes: {
        dark: DARK({ bg: '#101418', panel: '#1b2128', art: '#2a313a', text: '#e8eaed', dim: '#9aa0a6', line: '#2a313a' }),
        light: LIGHT({ bg: '#f3f5f8', panel: '#ffffff', art: '#e2e6eb', text: '#14181c', dim: '#5b646e', line: '#dce1e7' }),
      },
      options: [{ id: 'sky', name: 'Sky', accent: { dark: '#4fc3f7', light: '#0277bd' } }, { id: 'mint', name: 'Mint', accent: { dark: '#4cd4a0', light: '#097a56' } },
        { id: 'violet', name: 'Violet', accent: { dark: '#b39dff', light: '#6446d6' } }] },
    { id: 'minimal', name: 'Minimal', blurb: 'Flat and quiet, paper or ink', look: { radius: '10px', font: SYS, skin: 'minimal', icons: 'line' },
      modes: {
        dark: DARK({ bg: '#121212', panel: '#1b1b1b', art: '#2a2a2a', text: '#eeeeee', dim: '#9b9b9b', line: '#2b2b2b' }),
        light: LIGHT({ bg: '#faf9f6', panel: '#ffffff', art: '#ebe9e3', text: '#1b1b1b', dim: '#6c6a64', line: '#e2dfd8' }),
      },
      options: [{ id: 'ink', name: 'Ink', accent: { light: '#1b1b1b', dark: '#f2f2f2' } }, { id: 'blue', name: 'Blue', accent: { light: '#2563eb', dark: '#6ea0ff' } },
        { id: 'green', name: 'Green', accent: { light: '#16803c', dark: '#4ade80' } }, { id: 'rose', name: 'Rose', accent: { light: '#e11d48', dark: '#fb7185' } }] },
    { id: 'hifi', name: 'Hi-Fi', blurb: 'Brushed metal and a receiver glow', look: { radius: '8px', font: SYS, caps: 'uppercase', track: '.08em', skin: 'hifi' },
      modes: {
        dark: DARK({ bg: '#1a1b1d', panel: '#232528', art: '#2f3236', text: '#e9e6e0', dim: '#9a968d', line: '#36393e' }),
        light: LIGHT({ bg: '#d9dade', panel: '#ececef', art: '#c6c8ce', text: '#1b1c1f', dim: '#575a62', line: '#b6b9c1' }),
      },
      options: [{ id: 'amber', name: 'Amber', accent: { dark: '#ffb04a', light: '#a85f00' } }, { id: 'champagne', name: 'Champagne', accent: { dark: '#e6cf9d', light: '#7d5f1f' } },
        { id: 'ice', name: 'Ice', accent: { dark: '#8fd3ff', light: '#1366a0' } }, { id: 'vfd', name: 'VFD green', accent: { dark: '#6df2a5', light: '#0d7a48' } }] },
    { id: 'walkman', name: 'Walkman', blurb: 'Eighties portable: cream, silver, bold colour', look: { radius: '18px', font: SYS, skin: 'walkman' }, home: 'light',
      modes: {
        light: LIGHT({ bg: '#e9e3d5', panel: '#f6f2e7', art: '#d9d2c1', text: '#25221c', dim: '#6c665a', line: '#cdc5b3' }),
        dark: DARK({ bg: '#16130f', panel: '#221e18', art: '#332d24', text: '#f3ecdd', dim: '#b0a690', line: '#463e31' }),
      },
      options: [{ id: 'orange', name: 'Orange', accent: { light: '#e85200', dark: '#ff7a2e' } }, { id: 'yellow', name: 'Sports yellow', accent: { light: '#b87d00', dark: '#ffc83a' } },
        { id: 'blue', name: 'Blue', accent: { light: '#1f6fe0', dark: '#5b9bff' } }, { id: 'red', name: 'Red', accent: { light: '#d92b2b', dark: '#ff5d55' } }] },
    { id: 'winamp', name: 'Winamp', blurb: 'LCD text on dark (or pale LCD), monospace', look: { radius: '3px', font: MONO, skin: 'winamp' },
      modes: {
        dark: DARK({ bg: '#0e1013', panel: '#171a1e', art: '#232830', text: '#c6f5cb', dim: '#6f9a76', line: '#2a3038' }),
        light: LIGHT({ bg: '#e2ebd6', panel: '#f1f6e9', art: '#cddbbc', text: '#163816', dim: '#52704e', line: '#b2c59f' }),
      },
      options: [{ id: 'green', name: 'Classic green', accent: { dark: '#00e676', light: '#0a7d36' } },
        { id: 'amber', name: 'Amber LCD', accent: { dark: '#ffb000', light: '#9a5f00' }, vars: { text: '#ffd98a', dim: '#a78650' },
          lightVars: { bg: '#f0e4c4', panel: '#f9f1da', art: '#e0d0a4', text: '#3a2800', dim: '#76602e', line: '#cbb685' } },
        { id: 'blue', name: 'Blue skin', accent: { dark: '#4aa3ff', light: '#1259b0' }, vars: { text: '#cfe5ff', dim: '#6f8bab' },
          lightVars: { bg: '#dce7f3', panel: '#edf3fa', art: '#c4d6e9', text: '#0e2842', dim: '#4c6784', line: '#afc4da' } }] },
    { id: 'neon', name: 'Neon', blurb: 'Deep purple with a glowing accent, or daylight lilac', look: { radius: '14px', font: SYS, glow: true, skin: 'neon', icons: 'line' },
      modes: {
        dark: DARK({ bg: '#0d0b1e', panel: '#17142e', art: '#241f45', text: '#f2eeff', dim: '#9a92c9', line: '#2e2858' }),
        light: LIGHT({ bg: '#f5f1ff', panel: '#ffffff', art: '#e4d9fb', text: '#1d1238', dim: '#645790', line: '#d9cdf2' }),
      },
      options: [{ id: 'magenta', name: 'Magenta', accent: { dark: '#ff3df2', light: '#b8008f' } }, { id: 'cyan', name: 'Cyan', accent: { dark: '#22e6ff', light: '#00758f' } },
        { id: 'sunset', name: 'Sunset', accent: { dark: '#ff8a3d', light: '#b84a00' } }] },
    { id: 'oled', name: 'OLED black', blurb: 'Pure black (or pure white), high contrast', look: { radius: '12px', font: SYS, skin: 'soft' },
      modes: {
        dark: DARK({ bg: '#000000', panel: '#0c0c0e', art: '#1a1a1d', text: '#ffffff', dim: '#8e8e93', line: '#1d1d20' }),
        light: LIGHT({ bg: '#ffffff', panel: '#f3f3f5', art: '#e3e3e8', text: '#000000', dim: '#66666b', line: '#e0e0e5' }),
      },
      options: [{ id: 'sky', name: 'Sky', accent: { dark: '#4fc3f7', light: '#0069b8' } }, { id: 'mono', name: 'Mono', accent: { dark: '#ffffff', light: '#000000' } },
        { id: 'green', name: 'Green', accent: { dark: '#30d158', light: '#17803a' } }, { id: 'amber', name: 'Amber', accent: { dark: '#ffb340', light: '#a85f00' } }] },
    { id: 'vinyl', name: 'Vinyl lounge', blurb: 'Warm wood and brass, or parchment, with a serif voice', look: { radius: '14px', font: SYS, fontHead: SERIF, skin: 'vinyl', icons: 'line' },
      modes: {
        dark: DARK({ bg: '#1b1410', panel: '#271c15', art: '#3a2a1f', text: '#f2e7d3', dim: '#b5a086', line: '#45342a' }),
        light: LIGHT({ bg: '#f0e7d4', panel: '#faf3e4', art: '#dfd2b6', text: '#32251a', dim: '#76644c', line: '#d4c4a3' }),
      },
      options: [{ id: 'burgundy', name: 'Burgundy', accent: { dark: '#e26d63', light: '#9a2d28' } }, { id: 'brass', name: 'Brass', accent: { dark: '#d9ac55', light: '#8a6210' } },
        { id: 'forest', name: 'Forest', accent: { dark: '#74c498', light: '#2a7650' } }] },
    { id: '8bit', name: '8-bit', blurb: 'Blocky pixels and a console font', look: { radius: '0px', font: MONO, fontHead: PIXEL, skin: 'pixel8', icons: 'pixel' },
      modes: {
        dark: DARK({ bg: '#080810', panel: '#1b1b2b', art: '#2d2d44', text: '#fcfcfc', dim: '#a0a0c0', line: '#5c5c7a', error: '#ff4d6d', heart: '#fc74b4', ok: '#3ddc5a' }),
        light: LIGHT({ bg: '#d8d8d8', panel: '#f0f0f0', art: '#b8b8b8', text: '#101018', dim: '#44445a', line: '#101018', error: '#c4002f', heart: '#c8286a', ok: '#0a7a22' }),
      },
      options: [{ id: 'nes', name: 'NES red', accent: { dark: '#ff3b5c', light: '#c4002f' } },
        { id: 'gameboy', name: 'Game Boy', accent: { dark: '#9bbc0f', light: '#306230' },
          vars: { bg: '#0f380f', panel: '#1b4a1b', art: '#306230', text: '#d4e8a0', dim: '#8bac0f', line: '#4f7f2a', error: '#e0f8a0', heart: '#e0f8a0', ok: '#9bbc0f' },
          lightVars: { bg: '#c4d89a', panel: '#dcebb4', art: '#9bbc0f', text: '#0f380f', dim: '#2f5a2f', line: '#306230', error: '#0f380f', heart: '#0f380f', ok: '#0f380f' } },
        { id: 'c64', name: 'Blue', accent: { dark: '#7cc4ff', light: '#2020a8' }, vars: { bg: '#1b1b80', panel: '#2a2aa0', art: '#3d3dc0', text: '#ffffff', dim: '#b8b8ff', line: '#6c6cf0' },
          lightVars: { bg: '#b4b4ff', panel: '#d2d2ff', art: '#9090ec', text: '#0c0c48', dim: '#2c2c8c', line: '#2c2c8c' } },
        { id: 'amber', name: 'Amber', accent: { dark: '#ffb000', light: '#a35f00' } }] },
    { id: '16bit', name: '16-bit', blurb: 'Shaded buttons, bright console colours', look: { radius: '4px', font: SYS, fontHead: PIXEL, skin: 'pixel16', icons: 'pixel' },
      modes: {
        dark: DARK({ bg: '#14122b', panel: '#221f4a', art: '#322d68', text: '#f4f0ff', dim: '#aaa4d8', line: '#4b44a0' }),
        light: LIGHT({ bg: '#e8e5fa', panel: '#ffffff', art: '#d3cdf4', text: '#1b1740', dim: '#58528c', line: '#c0b8ec' }),
      },
      options: [{ id: 'snes', name: 'Lilac', accent: { dark: '#b9a7ff', light: '#6446d6' } },
        { id: 'megadrive', name: 'Mega blue', accent: { dark: '#4d95ff', light: '#1859d0' }, vars: { bg: '#05060d', panel: '#0d1530', art: '#16224a', line: '#24407f' },
          lightVars: { bg: '#e3ebfa', panel: '#ffffff', art: '#cddbf4', text: '#08142e', dim: '#445e8c', line: '#b3c7ea' } },
        { id: 'pce', name: 'Orange', accent: { dark: '#ff9a3d', light: '#b85500' } }, { id: 'jade', name: 'Jade', accent: { dark: '#3be0a8', light: '#0a7a58' } }] },
    { id: 'psx', name: 'PSX', blurb: 'Grey bevels and the four symbols', look: { radius: '10px', font: SYS, caps: 'uppercase', track: '.05em', skin: 'psx' },
      modes: {
        dark: DARK({ bg: '#0a0b0f', panel: '#171a20', art: '#242831', text: '#e9ecf1', dim: '#97a0ad', line: '#323844' }),
        light: LIGHT({ bg: '#c6c9ce', panel: '#dadde1', art: '#b1b5bc', text: '#13151a', dim: '#4a505b', line: '#a1a6af' }),
      },
      options: [{ id: 'triangle', name: 'Triangle', accent: { dark: '#38d67a', light: '#0f7a44' } }, { id: 'circle', name: 'Circle', accent: { dark: '#ff5a52', light: '#b8201f' } },
        { id: 'cross', name: 'Cross', accent: { dark: '#5a8dff', light: '#1f4fc4' } }, { id: 'square', name: 'Square', accent: { dark: '#ff7ac2', light: '#a8337c' } }] },
  ];

  const byId = (id) => THEMES.find((t) => t.id === id) || THEMES[0];
  const hasBoth = (t) => !!(t.modes.light && t.modes.dark);

  // resolveMode: the mode a theme is shown in. pref is 'light', 'dark', 'auto' (follow the system)
  // or empty (the theme's own: dark, except for themes that say otherwise).
  function resolveMode(theme, pref, systemDark) {
    let m = pref === 'auto' ? (systemDark ? 'dark' : 'light') : pref === 'light' || pref === 'dark' ? pref : theme.home || 'dark';
    if (!theme.modes[m]) m = theme.modes.dark ? 'dark' : 'light';
    return m;
  }

  const hex2rgb = (h) => { const m = /^#?([0-9a-f]{6})$/i.exec(h || ''); if (!m) return [0, 0, 0]; const n = parseInt(m[1], 16); return [(n >> 16) & 255, (n >> 8) & 255, n & 255]; };
  const rgb2hex = (r, g, b) => '#' + [r, g, b].map((v) => Math.max(0, Math.min(255, Math.round(v))).toString(16).padStart(2, '0')).join('');
  // relative luminance 0..1
  function luminance(hex) {
    const [r, g, b] = hex2rgb(hex).map((v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); });
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
  }
  // the text colour that reads on top of an accent
  const onColor = (hex) => { const l = luminance(hex); return (l + 0.05) / 0.058 >= 1.05 / (l + 0.05) ? '#101418' : '#ffffff'; }; // whichever of dark and white contrasts more
  const alpha = (hex, a) => { const [r, g, b] = hex2rgb(hex); return 'rgba(' + r + ',' + g + ',' + b + ',' + a + ')'; };

  // accentFor reads an option's accent for a mode (a plain colour or {light, dark})
  const accentFor = (opt, mode) => (typeof opt.accent === 'string' ? opt.accent : opt.accent[mode] || opt.accent.dark || opt.accent.light);

  // themeVars: every CSS variable of a theme + option in a mode
  function themeVars(theme, optId, mode) {
    const base = theme.modes[mode] || theme.modes.dark || theme.modes.light;
    const opt = theme.options.find((o) => o.id === optId) || theme.options[0];
    const v = Object.assign({}, base, (base.scheme === 'light' ? opt.lightVars : opt.vars) || {});
    const accent = accentFor(opt, base.scheme);
    const look = theme.look || {};
    return {
      '--bg': v.bg, '--panel': v.panel, '--art': v.art, '--text': v.text, '--dim': v.dim, '--line': v.line,
      '--error': v.error, '--heart': v.heart, '--ok': v.ok,
      '--accent': accent, '--on-accent': onColor(accent), '--focus': alpha(accent, 0.25),
      '--radius': look.radius || '12px', '--font': look.font || SYS, '--font-head': look.fontHead || look.font || SYS,
      '--caps': look.caps || 'none', '--track': look.track || 'normal',
      '--glow': look.glow ? '0 0 22px ' + alpha(accent, 0.55) : 'none',
      scheme: base.scheme, skin: look.skin || 'soft', icons: look.icons || 'solid',
    };
  }

  // pickAccent chooses a colour for the controls from cover pixels (RGBA bytes):
  // the most common saturated hue, set to a lightness that reads on the theme.
  function pickAccent(rgba, dark) {
    const buckets = new Array(12).fill(0), sums = new Array(12).fill(null).map(() => [0, 0, 0, 0]);
    for (let i = 0; i + 3 < rgba.length; i += 4) {
      if (rgba[i + 3] < 200) continue;
      const r = rgba[i] / 255, g = rgba[i + 1] / 255, b = rgba[i + 2] / 255;
      const mx = Math.max(r, g, b), mn = Math.min(r, g, b), d = mx - mn;
      const l = (mx + mn) / 2;
      if (d < 0.12 || l < 0.12 || l > 0.92) continue; // grey, black or white: no colour to take
      const s = d / (1 - Math.abs(2 * l - 1));
      let h = mx === r ? ((g - b) / d) % 6 : mx === g ? (b - r) / d + 2 : (r - g) / d + 4;
      h = (h * 60 + 360) % 360;
      const k = Math.floor(h / 30) % 12, w = s * (1 - Math.abs(l - 0.5));
      buckets[k] += w; sums[k][0] += w * h; sums[k][1] += w * s; sums[k][2] += w;
    }
    let best = 0;
    for (let k = 1; k < 12; k++) if (buckets[k] > buckets[best]) best = k;
    if (buckets[best] <= 0) return null;
    const h = sums[best][0] / sums[best][2], s = Math.min(0.9, Math.max(0.5, sums[best][1] / sums[best][2]));
    const l = dark ? 0.62 : 0.42;
    // hsl -> hex
    const c = (1 - Math.abs(2 * l - 1)) * s, x = c * (1 - Math.abs(((h / 60) % 2) - 1)), m = l - c / 2;
    const [r, g, b] = h < 60 ? [c, x, 0] : h < 120 ? [x, c, 0] : h < 180 ? [0, c, x] : h < 240 ? [0, x, c] : h < 300 ? [x, 0, c] : [c, 0, x];
    return rgb2hex((r + m) * 255, (g + m) * 255, (b + m) * 255);
  }

  // ---- this browser's choice ----
  const KEYS = { theme: 'pref:theme', mode: 'pref:mode', cover: 'pref:coveraccent', opt: (id) => 'pref:themeopt:' + id };
  const get = (k, def) => { try { const v = localStorage.getItem(k); return v === null ? def : v; } catch (e) { return def; } };
  const set = (k, v) => { try { localStorage.setItem(k, v); } catch (e) { /* storage may be off */ } };

  let coverAccent = null; // an accent taken from the cover, over the theme's own
  function apply() {
    if (typeof document === 'undefined') return null;
    const theme = byId(get(KEYS.theme, 'cliamp'));
    const sysDark = typeof matchMedia === 'function' ? matchMedia('(prefers-color-scheme: dark)').matches : true;
    const mode = resolveMode(theme, get(KEYS.mode, ''), sysDark);
    const vars = themeVars(theme, get(KEYS.opt(theme.id), theme.options[0].id), mode);
    if (coverAccent && get(KEYS.cover, '0') === '1') {
      vars['--accent'] = coverAccent; vars['--on-accent'] = onColor(coverAccent); vars['--focus'] = alpha(coverAccent, 0.25);
      if (theme.look && theme.look.glow) vars['--glow'] = '0 0 22px ' + alpha(coverAccent, 0.55);
    }
    const el = document.documentElement;
    for (const k of Object.keys(vars)) if (k.startsWith('--')) el.style.setProperty(k, vars[k]);
    el.style.colorScheme = vars.scheme;
    el.dataset.theme = theme.id;
    el.dataset.skin = vars.skin;
    el.dataset.icons = vars.icons;
    const meta = document.querySelector('meta[name=theme-color]');
    if (meta) meta.setAttribute('content', vars['--bg']);
    return vars;
  }
  function setCoverAccent(hex) { coverAccent = hex || null; apply(); }

  const iconPack = () => (typeof document !== 'undefined' && document.documentElement.dataset.icons) || 'solid';
  const api = { iconPack, THEMES, byId, hasBoth, resolveMode, themeVars, pickAccent, onColor, luminance, accentFor, KEYS, get, set, apply, setCoverAccent };
  root.CLIAMP_THEMES = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  apply();
  if (typeof matchMedia === 'function') {
    try { matchMedia('(prefers-color-scheme: dark)').addEventListener('change', apply); } catch (e) { /* old browser */ }
  }
})(typeof window !== 'undefined' ? window : globalThis);
