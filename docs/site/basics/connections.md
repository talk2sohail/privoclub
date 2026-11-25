# Connecting your accounts

Some servers see each person as themselves: Linear, Notion, GitHub and others
that know their users. For those, you connect your own account to Privo once.
Privo keeps it encrypted and uses it only for calls you make, so the server's
own permissions and history stay yours.

Connecting your own account arrived in 0.5.0, and your own API key in 0.9.0.

## Connect

You can start from three places:

- **Start**, under **Your tools**: choose **Connect** (or **Enter your key**)
  next to the server.
- **Connections**: choose **Connect your account** (or **Enter your API key**)
  under the server.
- Your AI client: on your first call to such a server, it tells you to
  connect first and gives a link. Open it in your browser.

Then, depending on the server:

- **A sign-in (OAuth).** Privo shows where you will sign in and what access it
  asks for. Choose **Continue to** the provider, sign in there and allow
  access. You come back to Privo, connected.
- **Your own API key.** Privo says where to get your key. Paste it and choose
  **Save my key**. Privo never shows it again, to you or to anyone.

Once connected, ask your AI client again: calls to that server now run as you.

## See your connections

**Connections** lists the servers that use your own account: when you
connected, when Privo last used it, and the access it holds.

## Disconnect

On **Connections**, choose **Disconnect** under the server. Privo deletes your
token or key at once. For a sign-in, it also asks the provider to revoke the
token, where the provider allows it. Your AI client can no longer use that
server as you until you connect again.

!!! note
    When an admin removes you from Privo, all your connections end the same
    way.

## Going further

- [Custom upstream sign-in and connections](../advanced/upstream-auth.md): how
  Privo signs in to these servers, settings entered by hand, and
  **Everyone's connections** for admins.
