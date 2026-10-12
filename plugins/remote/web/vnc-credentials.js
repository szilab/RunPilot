export function provideVNCCredentials(rfb, username, password) {
  const credentials = { username, password };
  rfb.sendCredentials(credentials);
  return () => {
    credentials.username = "";
    credentials.password = "";
  };
}
