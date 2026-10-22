# Desktop client

A small desktop app (Windows, macOS, Linux) that opens your steamcmd-serve
controller in its own window. It is a thin shell: the window shows the
controller's own web UI, with the same login, sessions and security
settings as in a browser. The client stores only the controller address and
never any credentials.

## Download

Get the archive for your platform from the project's GitHub Releases page:

- `steamcmd-serve-client-<version>-darwin-universal.zip` (macOS, Intel and
  Apple silicon)
- `steamcmd-serve-client-<version>-windows-amd64.zip`
- `steamcmd-serve-client-<version>-linux-amd64.tar.gz`

Linux needs GTK 3 and WebKit2GTK 4.0 installed
(`libgtk-3-0` and `libwebkit2gtk-4.0-37` on Debian and Ubuntu).

## First connect

1. Start the app. It shows a connect page.
2. Enter the controller address, for example `http://192.168.1.50:8080`.
   If you leave out the scheme, `http://` is assumed.
3. Select **Connect**. The client checks `GET /healthz` (5 second timeout)
   and, on success, saves the address and opens the controller UI. Sign in
   there as usual.

The address must be a bare `http://` or `https://` origin: no path, query,
or embedded credentials. A notice appears when you use plain `http://` with
a host that is not on a private network (loopback, RFC 1918, `fc00::/7`,
`localhost`, `*.local`), because traffic would cross the internet unencrypted.
Prefer `https://` behind a reverse proxy for anything outside your LAN.

On later launches the client connects to the saved address automatically. If
the controller does not answer, the connect page opens with the error.

## Change controller

Use **Controller > Change controller...** to return to the connect page
(automatic connect is skipped once). **Controller > Reload** (Cmd/Ctrl+R)
reloads the current page.

The saved address lives in `config.json` inside a `steamcmd-serve-client`
folder under your user config directory (`os.UserConfigDir`): for example
`~/Library/Application Support` on macOS, `%AppData%` on Windows and
`~/.config` on Linux. Login cookies are kept in the web view's own storage.

## Unsigned builds

Release builds are not code-signed or notarized, so the operating system
warns on first launch.

- **macOS (Gatekeeper):** unzip, move the app to Applications, then
  Control-click it and choose **Open**, then **Open** again. Alternatively,
  open System Settings > Privacy & Security and choose **Open Anyway**. From a
  terminal: `xattr -dr com.apple.quarantine steamcmd-serve.app`.
- **Windows (SmartScreen):** choose **More info**, then **Run anyway**.
- **Linux:** make the binary executable (`chmod +x steamcmd-serve-client`).

## Build from source

Requirements: Go (see `client/go.mod`), the platform's C toolchain and web
view (Xcode command line tools on macOS; `libgtk-3-dev` and
`libwebkit2gtk-4.0-dev` on Linux; WebView2 on Windows). There is no npm
step.

```bash
cd client
go test ./...
go run github.com/wailsapp/wails/v2/cmd/wails@v2.17.0 build
```

The output is in `client/build/bin/` (on macOS, `steamcmd-serve.app`). The
`client/` directory is a separate Go module, so the controller build does
not depend on Wails.
