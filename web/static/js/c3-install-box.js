import { LitElement, html } from '/static/vendor/lit/lit-all.min.js';

/** A command in a box with a copy button. The server renders the <code>; this adds the button. */
export class C3InstallBox extends LitElement {
  static properties = { copied: { state: true } };

  constructor() {
    super();
    this.copied = false;
  }

  createRenderRoot() { return this; }

  async copy() {
    const text = this.querySelector('code')?.textContent ?? '';
    try {
      await navigator.clipboard.writeText(text);
      this.copied = true;
    } catch {
      // clipboard blocked (e.g. insecure context): select the text so Ctrl+C works
      const range = document.createRange();
      range.selectNodeContents(this.querySelector('code'));
      const sel = getSelection();
      sel.removeAllRanges();
      sel.addRange(range);
      return;
    }
    clearTimeout(this._t);
    this._t = setTimeout(() => (this.copied = false), 1500);
  }

  render() {
    return html`<button type="button" class="btn copy" @click=${() => this.copy()} aria-live="polite">${this.copied ? 'Copied ✓' : 'Copy'}</button>`;
  }
}
customElements.define('c3-install-box', C3InstallBox);
