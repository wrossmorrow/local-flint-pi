export function createLogoHeader(tui, getTheme, shapes, animate) {
  const start = performance.now();
  let phase = 0;
  let timer;
  const pause = () => { clearInterval(timer); timer = undefined; };
  return {
    render(width) {
      const height = tui.terminal.rows;
      if (height < 12 || width < 9) { pause(); return []; }
      const theme = getTheme();
      const graphic = width >= 38 && height >= 24;
      if (graphic && animate) {
        timer ??= setInterval(() => {
          phase = ((performance.now() - start) / 9000) % 1;
          tui.requestRender();
        }, 50);
      } else {
        pause();
      }
      const wordmark = [theme.bold(theme.fg('text', 'recursive')), theme.fg('muted', 'flint')];
      const help = width >= 10 ? ['', ' '.repeat(Math.floor((width - 10) / 2)) + theme.fg('dim', '/ commands')] : [];
      if (!graphic) return [...wordmark.map(line => ' '.repeat(Math.floor((width - 9) / 2)) + line), ...help];
      const shape = width >= 60 && height >= 32 ? shapes.large : shapes.small;
      const light = theme.name === 'light';
      const low = light ? [206, 196, 220] : [54, 42, 78];
      const high = light ? [70, 48, 108] : [227, 213, 255];
      const palette = Array.from({ length: 256 }, (_, alpha) => {
        const rgb = low.map((value, i) => Math.round(value + (high[i] - value) * alpha / 255));
        if (theme.getColorMode() === 'truecolor') return `\x1b[38;2;${rgb.join(';')}m`;
        const levels = [0, 95, 135, 175, 215, 255];
        const [r, g, b] = rgb.map(value => levels.reduce((best, level, i) =>
          Math.abs(level - value) < Math.abs(levels[best] - value) ? i : best, 0));
        return `\x1b[38;5;${16 + 36 * r + 6 * g + b}m`;
      });
      const left = ' '.repeat(Math.floor((width - shape[0].length - 11) / 2));
      const middle = Math.floor(shape.length / 2) - 1;
      return [...shape.map((row, y) => {
        let previousColor = '';
        const pixels = row.map((cell, x) => {
          if (cell.glyph === '\u2800') return cell.glyph;
          const offset = cell.owner / 3 + (x / (row.length - 1) - 0.5) * 0.06 + (y / (shape.length - 1) - 0.5) * 0.03;
          const glow = cell.owner === 3 ? 0 : (1 + Math.cos((phase - offset) * Math.PI * 2)) / 2;
          const color = palette[Math.round(glow * 255)];
          const prefix = color === previousColor ? '' : color;
          previousColor = color;
          return prefix + cell.glyph;
        }).join('') + '\x1b[39m';
        const label = y === middle ? wordmark[0] : y === middle + 1 ? wordmark[1] : '';
        return left + pixels + '  ' + label;
      }), ...help];
    },
    invalidate() {},
    stop() { animate = false; pause(); },
    dispose() { animate = false; pause(); },
  };
}

export function registerLogo(pi, shapes) {
  let header;
  pi.on('session_start', (_event, ctx) => {
    if (!ctx.hasUI) return;
    const fresh = !ctx.sessionManager.getBranch().some(entry => entry.type === 'message');
    ctx.ui.setHeader(tui => {
      header = createLogoHeader(tui, () => ctx.ui.theme, shapes, fresh);
      return header;
    });
  });
  pi.on('agent_start', () => header?.stop());
  pi.on('user_bash', () => header?.stop());
  pi.on('session_shutdown', () => header?.dispose());
}
