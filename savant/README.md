# Bundling youtube_analytics in Savant Desktop

1. Add this repo as a submodule at `mcp_servers/youtube_analytics/` and a build block in
   `build/scripts/build-mac.sh` (amd64 + arm64, lipo) and `build-windows.ps1`, like the existing
   `url_fetcher` block. The binary is `bin/youtube_analytics` from `go build ./cmd`.
2. Copy `youtube_analytics.yaml.template` to `mcp_servers/configs/`.
3. Secrets: `YOUTUBE_OAUTH_CLIENT_ID` and `YOUTUBE_OAUTH_CLIENT_SECRET` (a Google Cloud OAuth
   client of type Desktop app; setup steps are in the main README). The settings panel should link
   to those steps.
4. `YOUTUBE_API_KEY` is optional (only `video_comments` uses it). It is deliberately not in
   `secret_keys`, because Savant treats a missing secret as a fatal config error; expose it as an
   optional field in the settings panel once Savant supports optional secrets.
5. Sign-in happens through the `connect_channel` tool: it opens the system browser on a loopback
   redirect (`http://127.0.0.1:<random port>/callback`) and waits up to 90 seconds. No elicitation is
   needed. Tokens are stored by the server itself under the OS config dir
   (`gomcpgo/youtube_analytics/tokens.json`), not in the Savant keyring, because they are
   refreshed by the server.
6. A Savant-owned, Google-verified OAuth client would remove step 3 for end users, but the YouTube
   scopes are "sensitive" and need Google's app verification before a shared client can serve more
   than 100 users.
