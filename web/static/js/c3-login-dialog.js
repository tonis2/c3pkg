import { LitElement, html, nothing } from '/static/vendor/lit/lit-all.min.js';

/**
 * A login pop-up. Any link to /login opens it instead of navigating (modifier-clicks and
 * no-JS browsers still get the full page). It posts to /login asking for JSON.
 */
export class C3LoginDialog extends LitElement {
  static properties = {
    csrf: { type: String },
    github: { type: Boolean },
    error: { state: true },
    busy: { state: true },
  };

  constructor() {
    super();
    this.csrf = '';
    this.github = false;
    this.error = '';
    this.busy = false;
    this._intercept = (e) => this.intercept(e);
  }

  createRenderRoot() { return this; }

  connectedCallback() {
    super.connectedCallback();
    document.addEventListener('click', this._intercept);
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    document.removeEventListener('click', this._intercept);
  }

  get dialog() { return this.querySelector('dialog'); }

  intercept(e) {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const a = e.target instanceof Element ? e.target.closest('a[href]') : null;
    if (!a || a.target || new URL(a.href, location.href).pathname !== '/login' || new URL(a.href).origin !== location.origin) return;
    e.preventDefault();
    this.show();
  }

  show() {
    this.error = '';
    this.dialog?.showModal();
    this.querySelector('input[name=email]')?.focus();
  }

  backdrop(e) {
    if (e.target === this.dialog) this.dialog.close();
  }

  async submit(e) {
    e.preventDefault();
    if (this.busy) return;
    const form = e.target;
    this.busy = true;
    this.error = '';
    const body = new URLSearchParams(new FormData(form));
    body.set('next', location.pathname + location.search);
    try {
      const res = await fetch('/login', {
        method: 'POST',
        headers: { Accept: 'application/json', 'X-CSRF-Token': this.csrf, 'Content-Type': 'application/x-www-form-urlencoded' },
        body,
      });
      if (res.ok) {
        const data = await res.json();
        // Reload when we are staying on the same page, otherwise follow the server's choice.
        if (data.next === location.pathname + location.search) location.reload();
        else location.assign(data.next || '/dashboard');
        return;
      }
      const data = await res.json().catch(() => ({}));
      this.error = data.error || 'Could not log in. Please try again.';
      form.elements.password.value = '';
      form.elements.password.focus();
    } catch {
      this.error = 'Network error. Please try again.';
    } finally {
      this.busy = false;
    }
  }

  render() {
    return html`
      <dialog class="login-dialog" aria-labelledby="login-title" @click=${(e) => this.backdrop(e)}>
        <form @submit=${(e) => this.submit(e)} method="post" action="/login">
          <div class="split">
            <h2 id="login-title">Log in</h2>
            <button type="button" class="btn" aria-label="Close" @click=${() => this.dialog.close()}>✕</button>
          </div>
          ${this.error ? html`<p class="error banner" role="alert">${this.error}</p>` : nothing}
          <div class="field">
            <label for="dlg-email">Email</label>
            <input id="dlg-email" name="email" type="email" autocomplete="email" required>
          </div>
          <div class="field">
            <label for="dlg-password">Password</label>
            <input id="dlg-password" name="password" type="password" autocomplete="current-password" required>
          </div>
          <button class="btn btn-primary wide" type="submit" ?disabled=${this.busy}>${this.busy ? 'Logging in…' : 'Log in'}</button>
          ${this.github ? html`<p class="or">or</p><a class="btn wide" href="/auth/github/login">Continue with GitHub</a>` : nothing}
          <p class="muted">New here? <a href="/register">Create an account</a></p>
        </form>
      </dialog>`;
  }
}
customElements.define('c3-login-dialog', C3LoginDialog);
