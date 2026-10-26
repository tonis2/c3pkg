import { LitElement, html, nothing } from '/static/vendor/lit/lit-all.min.js';

/**
 * Turns a plain <input type="file"> into a drag-and-drop zone. The input stays in the page
 * (stretched invisibly over the zone), so clicking, dropping and keyboard use are all native,
 * and the form still submits normally. Checks the extension and size before uploading.
 */
export class C3Upload extends LitElement {
  static properties = {
    maxMb: { type: Number, attribute: 'max-mb' },
    ext: { type: String },
    file: { state: true },
    over: { state: true },
    problem: { state: true },
  };

  constructor() {
    super();
    this.maxMb = 20;
    this.ext = '.c3l';
    this.file = null;
    this.over = false;
    this.problem = '';
  }

  createRenderRoot() { return this; }

  get input() { return this.querySelector('input[type=file]'); }

  connectedCallback() {
    super.connectedCallback();
    this.classList.add('upload-zone');
    this._change = () => this.pick(this.input.files[0] || null);
    this.input?.addEventListener('change', this._change);
    for (const t of ['dragenter', 'dragover']) this.addEventListener(t, (e) => { e.preventDefault(); this.over = true; });
    for (const t of ['dragleave', 'drop']) this.addEventListener(t, () => { this.over = false; });
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this.input?.removeEventListener('change', this._change);
  }

  pick(file) {
    this.problem = '';
    this.file = file;
    const input = this.input;
    input.setCustomValidity('');
    if (!file) return;
    if (!file.name.toLowerCase().endsWith(this.ext)) {
      this.problem = `The file must have the ${this.ext} extension.`;
    } else if (file.size > this.maxMb * 1024 * 1024) {
      this.problem = `That file is ${formatSize(file.size)}; the limit is ${this.maxMb} MB.`;
    } else if (file.size === 0) {
      this.problem = 'That file is empty.';
    }
    if (this.problem) input.setCustomValidity(this.problem);
  }

  render() {
    this.classList.toggle('over', this.over);
    this.classList.toggle('has-file', !!this.file && !this.problem);
    this.classList.toggle('has-problem', !!this.problem);
    return html`
      <div class="upload-text" aria-hidden="true">
        ${this.file
          ? html`<strong class="mono">${this.file.name}</strong> <span class="muted">${formatSize(this.file.size)}</span>`
          : html`<strong>Drop a ${this.ext} file here</strong> <span class="muted">or click to browse</span>`}
      </div>
      ${this.problem ? html`<p class="error" role="alert">${this.problem}</p>` : nothing}`;
  }
}

export function formatSize(n) {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KB`;
  return `${n} B`;
}

customElements.define('c3-upload', C3Upload);
