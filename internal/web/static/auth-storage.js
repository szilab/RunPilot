(() => {
  const key = "runpilot.token";
  const clearShared = shared => shared.removeItem(key);
  globalThis.RunPilotAuthStorage = Object.freeze({
    clearShared,
    migrate(tab, shared) {
      const current = tab.getItem(key) || shared.getItem(key) || "";
      // Remove shared credentials even when this tab already has a newer token.
      clearShared(shared);
      if (current) tab.setItem(key, current);
      return current;
    },
    save(tab, shared, token) {
      clearShared(shared);
      if (token) tab.setItem(key, token); else tab.removeItem(key);
    },
  });
})();
