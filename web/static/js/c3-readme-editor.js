import { LitElement, html, nothing, unsafeHTML } from '/static/vendor/lit/lit-all.min.js';

/**
 * Adds Write / Preview tabs and a "load from file" button to the README <textarea> the server
 * rendered. The textarea stays in the form, so without JavaScript it is a plain textarea and
 * the form submits the same way. The preview is rendered by the server (the same renderer and
 * sanitizer as the package page), so what you see is what gets saved.
 */
export class C3ReadmeEditor extends LitElement {
  static properties = {
    tab: { state: true },
    preview: { state: true },
    problem: { state: true },
  };

  constructor() {
    super();
    this.tab = 'write';
    this.preview = '';
    this.problem = '';
    this.previewed = null;
  }

  // Keep the server-rendered textarea and put the tabs and preview in front of it.
  createRenderRoot() {
    this.renderOptions.renderBefore = this.textarea;
    return this;
  }

  get textarea() { return this.querySelector('textarea'); }

  connectedCallback() {
    super.connectedCallback();
    this.classList.add('readme-editor');
  }

  async show(tab) {
    this.tab = tab;
    this.textarea.hidden = tab === 'preview';
    if (tab === 'preview') await this.renderPreview();
  }

  async renderPreview() {
    const text = this.textarea.value;
    if (text === this.previewed) return;
    this.problem = '';
    if (text.trim() === '') {
      this.preview = '';
      this.previewed = text;
      return;
    }
    try {
      const res = await fetch('/dashboard/readme/preview', {
        method: 'POST',
        headers: { 'Content-Type': 'text/markdown', 'X-CSRF-Token': this.closest('form').elements.csrf.value },
        body: text,
      });
      if (!res.ok) {
        this.problem = (await res.text()).trim() || 'Could not render the preview.';
        this.preview = '';
        return;
      }
      this.preview = await res.text();
      this.previewed = text;
    } catch {
      this.problem = 'Could not render the preview.';
    }
  }

  async load(e) {
    const file = e.target.files[0];
    e.target.value = '';
    if (!file) return;
    if (file.size > 512 << 10) {
      this.problem = 'That file is larger than 512 KB.';
      return;
    }
    this.problem = '';
    this.textarea.value = await file.text();
    this.show('write');
  }

  render() {
    return html`
      <div class="readme-tabs" role="tablist">
        <button type="button" role="tab" class=${this.tab === 'write' ? 'active' : ''} aria-selected=${this.tab === 'write'}
          @click=${() => this.show('write')}>Write</button>
        <button type="button" role="tab" class=${this.tab === 'preview' ? 'active' : ''} aria-selected=${this.tab === 'preview'}
          @click=${() => this.show('preview')}>Preview</button>
        <label class="readme-load">Load from file
          <input type="file" accept=".md,.markdown,text/markdown,text/plain" hidden @change=${(e) => this.load(e)}>
        </label>
      </div>
      ${this.problem ? html`<p class="error" role="alert">${this.problem}</p>` : nothing}
      ${this.tab === 'preview'
        ? html`<div class="readme readme-preview">${this.preview
            ? unsafeHTML(this.preview)
            : html`<p class="muted">Nothing to preview.</p>`}</div>`
        : nothing}`;
  }
}

customElements.define('c3-readme-editor', C3ReadmeEditor);
