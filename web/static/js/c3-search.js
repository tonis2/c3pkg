import { LitElement, html, nothing } from '/static/vendor/lit/lit-all.min.js';

/**
 * Wraps a server-rendered <input name="q">. Without JavaScript the surrounding form still
 * searches; with it, suggestions appear as you type and "/" focuses the first search box.
 * Light DOM, so the page's own CSS styles it and the input stays where the server put it.
 */
export class C3Search extends LitElement {
  static properties = {
    items: { state: true },
    active: { state: true },
    open: { state: true },
  };

  constructor() {
    super();
    this.items = [];
    this.active = -1;
    this.open = false;
    this._timer = 0;
    this._abort = null;
  }

  createRenderRoot() { return this; }

  get input() { return this.querySelector('input'); }

  connectedCallback() {
    super.connectedCallback();
    const input = this.input;
    if (!input) return;
    input.setAttribute('role', 'combobox');
    input.setAttribute('aria-autocomplete', 'list');
    input.setAttribute('aria-expanded', 'false');
    input.setAttribute('aria-controls', 'c3-suggest-list');
    this._onInput = () => this.schedule();
    this._onKey = (e) => this.keydown(e);
    this._onBlur = () => setTimeout(() => this.close(), 150); // let a click on a suggestion land first
    input.addEventListener('input', this._onInput);
    input.addEventListener('keydown', this._onKey);
    input.addEventListener('blur', this._onBlur);
    document.addEventListener('keydown', C3Search.slash);
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    const input = this.input;
    if (input) {
      input.removeEventListener('input', this._onInput);
      input.removeEventListener('keydown', this._onKey);
      input.removeEventListener('blur', this._onBlur);
    }
    document.removeEventListener('keydown', C3Search.slash);
    clearTimeout(this._timer);
    this._abort?.abort();
  }

  /** "/" focuses the first search box, unless the user is typing somewhere. */
  static slash(e) {
    if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey) return;
    const t = e.target;
    if (t instanceof HTMLElement && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return;
    const box = document.querySelector('c3-search input');
    if (box) {
      e.preventDefault();
      box.focus();
    }
  }

  schedule() {
    clearTimeout(this._timer);
    this._timer = setTimeout(() => this.fetchSuggestions(), 150);
  }

  async fetchSuggestions() {
    const q = this.input.value.trim();
    this._abort?.abort();
    if (q.length < 2) {
      this.close();
      return;
    }
    this._abort = new AbortController();
    try {
      const res = await fetch(`/api/v1/packages?per_page=6&sort=downloads&q=${encodeURIComponent(q)}`, {
        signal: this._abort.signal,
        headers: { Accept: 'application/json' },
      });
      if (!res.ok) return this.close();
      const data = await res.json();
      this.items = data.packages || [];
      this.active = -1;
      this.setOpen(this.items.length > 0);
    } catch (err) {
      if (err.name !== 'AbortError') this.close();
    }
  }

  setOpen(open) {
    this.open = open;
    this.input?.setAttribute('aria-expanded', String(open));
    if (!open) this.input?.removeAttribute('aria-activedescendant');
  }

  close() {
    this.items = [];
    this.active = -1;
    this.setOpen(false);
  }

  keydown(e) {
    if (!this.open) return;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      // position 0 is "no selection" (back in the input), 1..n are the suggestions
      const slots = this.items.length + 1;
      const next = (this.active + 1 + (e.key === 'ArrowDown' ? 1 : -1) + slots) % slots;
      this.active = next - 1;
      if (this.active >= 0) this.input.setAttribute('aria-activedescendant', `c3-suggest-${this.active}`);
      else this.input.removeAttribute('aria-activedescendant');
    } else if (e.key === 'Enter' && this.active >= 0) {
      e.preventDefault();
      location.assign(`/packages/${encodeURIComponent(this.items[this.active].name)}`);
    } else if (e.key === 'Escape') {
      this.close();
    }
  }

  render() {
    if (!this.open) return nothing;
    return html`
      <ul class="suggest" id="c3-suggest-list" role="listbox">
        ${this.items.map(
          (p, i) => html`
            <li id="c3-suggest-${i}" role="option" aria-selected=${i === this.active} class=${i === this.active ? 'active' : ''}>
              <a href="/packages/${encodeURIComponent(p.name)}">
                <span class="mono name">${p.name}</span>
                ${p.latest_version ? html`<span class="muted small mono">${p.latest_version}</span>` : nothing}
                <span class="muted small desc">${p.description}</span>
              </a>
            </li>`,
        )}
      </ul>`;
  }
}
customElements.define('c3-search', C3Search);
