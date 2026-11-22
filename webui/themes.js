'use strict';
// App themes. A theme is a set of colour values (the CSS variables of app.css)
// plus a few looks (corner radius, label font, glow). Each theme has colour
// options (usually accents) and some also a light and a dark base.
// Loaded first and applied at once, so the page never flashes the wrong colours.
(function (root) {
  const SYS = 'system-ui, -apple-system, "Segoe UI", Roboto, sans-serif';
  const MONO = 'ui-monospace, "SF Mono", Menlo, Consolas, "DejaVu Sans Mono", monospace';

  // base colours: bg page, panel cards, art placeholders and tracks, text, dim text, line borders, error, heart, ok
  const DARK = (o) => Object.assign({ error: '#ef5350', heart: '#ff5a7a', ok: '#4cd48a', scheme: 'dark' }, o);
  const LIGHT = (o) => Object.assign({ error: '#d93025', heart: '#e0245e', ok: '#188038', scheme: 'light' }, o);

  const THEMES = [
    { id: 'cliamp', name: 'cliamp', blurb: 'The original dark look', look: { radius: '12px', font: SYS },
      modes: { dark: DARK({ bg: '#101418', panel: '#1b2128', art: '#2a313a', text: '#e8eaed', dim: '#9aa0a6', line: '#2a313a' }) },
      options: [{ id: 'sky', name: 'Sky', accent: '#4fc3f7' }, { id: 'mint', name: 'Mint', accent: '#4cd4a0' }, { id: 'violet', name: 'Violet', accent: '#b39dff' }] },
    { id: 'minimal', name: 'Minimal', blurb: 'Flat and quiet, paper or ink', look: { radius: '10px', font: SYS },
      modes: {
        light: LIGHT({ bg: '#faf9f6', panel: '#ffffff', art: '#ebe9e3', text: '#1b1b1b', dim: '#6c6a64', line: '#e2dfd8' }),
        dark: DARK({ bg: '#121212', panel: '#1b1b1b', art: '#2a2a2a', text: '#eeeeee', dim: '#9b9b9b', line: '#2b2b2b' }),
      },
      options: [{ id: 'ink', name: 'Ink', accent: { light: '#1b1b1b', dark: '#f2f2f2' } }, { id: 'blue', name: 'Blue', accent: { light: '#2563eb', dark: '#6ea0ff' } },
        { id: 'green', name: 'Green', accent: { light: '#16803c', dark: '#4ade80' } }, { id: 'rose', name: 'Rose', accent: { light: '#e11d48', dark: '#fb7185' } }] },
    { id: 'hifi', name: 'Hi-Fi', blurb: 'Brushed metal and a receiver glow', look: { radius: '8px', font: SYS, caps: 'uppercase', track: '.08em' },
      modes: { dark: DARK({ bg: '#1a1b1d', panel: '#232528', art: '#2f3236', text: '#e9e6e0', dim: '#9a968d', line: '#36393e' }) },
      options: [{ id: 'amber', name: 'Amber', accent: '#ffb04a' }, { id: 'champagne', name: 'Champagne', accent: '#e6cf9d' },
        { id: 'ice', name: 'Ice', accent: '#8fd3ff' }, { id: 'vfd', name: 'VFD green', accent: '#6df2a5' }] },
    { id: 'walkman', name: 'Walkman', blurb: 'Eighties portable: cream, silver, bold colour', look: { radius: '18px', font: SYS },
      modes: { light: LIGHT({ bg: '#e9e3d5', panel: '#f6f2e7', art: '#d9d2c1', text: '#25221c', dim: '#6c665a', line: '#cdc5b3' }) },
      options: [{ id: 'orange', name: 'Orange', accent: '#e85200' }, { id: 'yellow', name: 'Sports yellow', accent: '#b87d00' },
        { id: 'blue', name: 'Blue', accent: '#1f6fe0' }, { id: 'red', name: 'Red', accent: '#d92b2b' }] },
    { id: 'winamp', name: 'Winamp', blurb: 'Dark panels, LCD text, monospace', look: { radius: '3px', font: MONO },
      modes: { dark: DARK({ bg: '#0e1013', panel: '#171a1e', art: '#232830', text: '#c6f5cb', dim: '#6f9a76', line: '#2a3038' }) },
      options: [{ id: 'green', name: 'Classic green', accent: '#00e676' },
        { id: 'amber', name: 'Amber LCD', accent: '#ffb000', vars: { text: '#ffd98a', dim: '#a78650' } },
        { id: 'blue', name: 'Blue skin', accent: '#4aa3ff', vars: { text: '#cfe5ff', dim: '#6f8bab' } }] },
    { id: 'neon', name: 'Neon', blurb: 'Deep purple with a glowing accent', look: { radius: '14px', font: SYS, glow: true },
      modes: { dark: DARK({ bg: '#0d0b1e', panel: '#17142e', art: '#241f45', text: '#f2eeff', dim: '#9a92c9', line: '#2e2858' }) },
      options: [{ id: 'magenta', name: 'Magenta', accent: '#ff3df2' }, { id: 'cyan', name: 'Cyan', accent: '#22e6ff' }, { id: 'sunset', name: 'Sunset', accent: '#ff8a3d' }] },
    { id: 'oled', name: 'OLED black', blurb: 'Pure black, high contrast', look: { radius: '12px', font: SYS },
      modes: { dark: DARK({ bg: '#000000', panel: '#0c0c0e', art: '#1a1a1d', text: '#ffffff', dim: '#8e8e93', line: '#1d1d20' }) },
      options: [{ id: 'sky', name: 'Sky', accent: '#4fc3f7' }, { id: 'white', name: 'White', accent: '#ffffff' },
        { id: 'green', name: 'Green', accent: '#30d158' }, { id: 'amber', name: 'Amber', accent: '#ffb340' }] },
  ];

  const byId = (id) => THEMES.find((t) => t.id === id) || THEMES[0];
  const hasBoth = (t) => !!(t.modes.light && t.modes.dark);

  // resolveMode: the mode a theme is shown in. 'auto' follows the system when the theme has both.
  function resolveMode(theme, pref, systemDark) {
    const both = hasBoth(theme);
    if (!both) return theme.modes.dark ? 'dark' : 'light';
    if (pref === 'light' || pref === 'dark') return pref;
    return systemDark ? 'dark' : 'light';
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
    const v = Object.assign({}, base, opt.vars || {});
    const accent = accentFor(opt, base.scheme);
    const look = theme.look || {};
    return {
      '--bg': v.bg, '--panel': v.panel, '--art': v.art, '--text': v.text, '--dim': v.dim, '--line': v.line,
      '--error': v.error, '--heart': v.heart, '--ok': v.ok,
      '--accent': accent, '--on-accent': onColor(accent), '--focus': alpha(accent, 0.25),
      '--radius': look.radius || '12px', '--font': look.font || SYS,
      '--caps': look.caps || 'none', '--track': look.track || 'normal',
      '--glow': look.glow ? '0 0 22px ' + alpha(accent, 0.55) : 'none',
      scheme: base.scheme,
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
    const mode = resolveMode(theme, get(KEYS.mode, 'auto'), sysDark);
    const vars = themeVars(theme, get(KEYS.opt(theme.id), theme.options[0].id), mode);
    if (coverAccent && get(KEYS.cover, '0') === '1') {
      vars['--accent'] = coverAccent; vars['--on-accent'] = onColor(coverAccent); vars['--focus'] = alpha(coverAccent, 0.25);
      if (theme.look && theme.look.glow) vars['--glow'] = '0 0 22px ' + alpha(coverAccent, 0.55);
    }
    const el = document.documentElement;
    for (const k of Object.keys(vars)) if (k.startsWith('--')) el.style.setProperty(k, vars[k]);
    el.style.colorScheme = vars.scheme;
    el.dataset.theme = theme.id;
    const meta = document.querySelector('meta[name=theme-color]');
    if (meta) meta.setAttribute('content', vars['--bg']);
    return vars;
  }
  function setCoverAccent(hex) { coverAccent = hex || null; apply(); }

  const api = { THEMES, byId, hasBoth, resolveMode, themeVars, pickAccent, onColor, luminance, accentFor, KEYS, get, set, apply, setCoverAccent };
  root.CLIAMP_THEMES = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  apply();
  if (typeof matchMedia === 'function') {
    try { matchMedia('(prefers-color-scheme: dark)').addEventListener('change', apply); } catch (e) { /* old browser */ }
  }
})(typeof window !== 'undefined' ? window : globalThis);
