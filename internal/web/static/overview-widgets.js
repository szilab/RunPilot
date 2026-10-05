(function (global) {
  class OverviewWidgets {
    constructor(root) { this.root = root; this.entries = new Map(); this.active = false; }
    register(owner, widget) {
      if (!widget || typeof widget.id !== "string" || !/^[a-z0-9][a-z0-9._-]*$/.test(widget.id) || !widget.title || typeof widget.mount !== "function" || (widget.refresh && typeof widget.refresh !== "function") || (widget.dispose && typeof widget.dispose !== "function")) throw new TypeError("invalid Overview widget");
      const prior = this.entries.get(widget.id);
      if (prior) {
        if (prior.owner !== owner) throw new Error("Overview widget ID is already registered");
        return () => this.unregister(owner, widget.id);
      }
      const entry = {owner, widget, shell:null, body:null, mounted:false, generation:0, disposeMount:null, busy:false};
      this.entries.set(widget.id, entry);
      if (this.active) this.render();
      return () => this.unregister(owner, widget.id);
    }
    unregister(owner, id) {
      const entry = this.entries.get(id);
      if (!entry || entry.owner !== owner) return;
      this.disposeEntry(entry); this.entries.delete(id); this.render();
    }
    unregisterOwner(owner) { for (const [id, entry] of [...this.entries]) if (entry.owner === owner) this.unregister(owner, id); }
    setActive(active) {
      if (this.active === active) return;
      this.active = active;
      if (!active) { for (const entry of this.entries.values()) this.disposeEntry(entry); this.root.replaceChildren(); }
      else this.render();
    }
    disposeEntry(entry) {
      entry.generation++;
      if (entry.mounted) {
        try { entry.disposeMount?.(); } catch (error) { console.error("widget cleanup", error); }
        try { entry.widget.dispose?.(); } catch (error) { console.error("widget cleanup", error); }
      }
      entry.mounted = false; entry.disposeMount = null; entry.busy = false;
      entry.shell?.remove(); entry.shell = null; entry.body = null;
    }
    render() {
      if (!this.active) return;
      const ordered = [...this.entries.values()].sort((a,b) => (Number(a.widget.order)||0)-(Number(b.widget.order)||0) || a.widget.id.localeCompare(b.widget.id));
      for (const entry of ordered) {
        if (!entry.shell) {
          const card = document.createElement("article"); card.className = "overview-widget";
          card.dataset.size = ["small","medium","wide"].includes(entry.widget.size) ? entry.widget.size : "medium";
          const header = document.createElement("div"); header.className = "overview-widget-head";
          const title = document.createElement("h2"); title.textContent = entry.widget.title;
          const refresh = document.createElement("button"); refresh.type = "button"; refresh.className = "button secondary small"; refresh.textContent = "Refresh"; refresh.setAttribute("aria-label", `Refresh ${entry.widget.title}`);
          refresh.onclick = () => this.refreshEntry(entry);
          header.append(title, refresh);
          const body = document.createElement("div"); body.className = "overview-widget-body";
          body.setAttribute("role", "status"); body.textContent = "Loading…";
          card.append(header, body); entry.shell = card; entry.body = body;
        }
        this.root.append(entry.shell);
        if (!entry.mounted) this.mountEntry(entry);
      }
    }
    async mountEntry(entry) {
      const generation = entry.generation;
      entry.mounted = true;
      try {
        const cleanup = await entry.widget.mount(entry.body);
        if (generation !== entry.generation) { if (typeof cleanup === "function") cleanup(); return; }
        if (typeof cleanup === "function") entry.disposeMount = cleanup;
      } catch (error) { if (generation === entry.generation) this.showError(entry, error); }
    }
    async refreshEntry(entry) {
      if (!this.active || !entry.mounted || !entry.widget.refresh || entry.busy) return;
      const generation = entry.generation; entry.busy = true;
      try { await entry.widget.refresh(entry.body); }
      catch (error) { if (generation === entry.generation) this.showError(entry, error); }
      finally { entry.busy = false; }
    }
    refresh() { return Promise.all([...this.entries.values()].map(entry => this.refreshEntry(entry))); }
    showError(entry, error) {
      console.error(`Overview widget ${entry.widget.id}`, error);
      entry.body.replaceChildren(); const message = document.createElement("p"); message.className = "overview-widget-error"; message.setAttribute("role", "alert"); message.textContent = error?.message || "Widget unavailable"; entry.body.append(message);
    }
  }
  global.RunPilotOverviewWidgets = OverviewWidgets;
  if (typeof module !== "undefined") module.exports = OverviewWidgets;
})(typeof window === "undefined" ? globalThis : window);
