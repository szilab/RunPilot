import test from "node:test";
import assert from "node:assert/strict";
import { provideVNCCredentials } from "./credentials.js";

test("VNC credentials stay available through noVNC's deferred auth and clear afterward", async () => {
  let consumed, provided;
  const rfb = { sendCredentials(credentials) {
    provided = credentials;
    setTimeout(() => { consumed = { username: credentials.username, password: credentials.password }; }, 0);
  } };
  const clear = provideVNCCredentials(rfb, "saved-user", "test-secret");
  await new Promise(resolve => setTimeout(resolve, 1));
  assert.deepEqual(consumed, { username: "saved-user", password: "test-secret" });
  clear();
  assert.deepEqual(consumed, { username: "saved-user", password: "test-secret" });
  assert.deepEqual(provided, { username: "", password: "" });
});
