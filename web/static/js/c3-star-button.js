import { LitElement } from '/static/vendor/lit/lit-all.min.js';

/**
 * Wraps the star <form>. Submitting it calls the JSON API and updates the button in place;
 * without JavaScript the form posts normally and the page reloads.
 */
export class C3StarButton extends LitElement {
  createRenderRoot() { return this; }

  connectedCallback() {
    super.connectedCallback();
    this._submit = (e) => this.toggle(e);
    this.querySelector('form')?.addEventListener('submit', this._submit);
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this.querySelector('form')?.removeEventListener('submit', this._submit);
  }

  async toggle(e) {
    e.preventDefault();
    const form = e.target;
    const button = form.querySelector('button');
    const m = new URL(form.action).pathname.match(/^\/packages\/([^/]+)\/(star|unstar)$/);
    if (!m || button.disabled) return form.submit();
    const [, name, action] = m;
    button.disabled = true;
    try {
      const res = await fetch(`/api/v1/packages/${name}/star`, {
        method: action === 'star' ? 'PUT' : 'DELETE',
        headers: { Accept: 'application/json', 'X-CSRF-Token': form.elements.csrf.value },
      });
      if (!res.ok) return form.submit(); // fall back to the full-page flow
      const { starred, stars } = await res.json();
      button.textContent = `${starred ? '★ Starred' : '☆ Star'} · ${stars}`;
      form.action = `/packages/${name}/${starred ? 'unstar' : 'star'}`;
      button.setAttribute('aria-pressed', String(starred));
    } catch {
      form.submit();
    } finally {
      button.disabled = false;
    }
  }
}
customElements.define('c3-star-button', C3StarButton);
