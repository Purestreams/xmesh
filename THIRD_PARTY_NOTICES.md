# Third-party notices

XMesh embeds unmodified Xray-core Go modules in its binary for the Agent-side
REALITY transport. The Gateway release also bundles the upstream Xray executable.

- Xray-core: `github.com/xtls/xray-core` (version pinned in `go.mod`; executable
  version pinned in `versions.env`). Source: <https://github.com/XTLS/Xray-core>.
- REALITY: `github.com/xtls/reality` (version pinned in `go.mod`). Source:
  <https://github.com/XTLS/REALITY>.

These upstream components are licensed under the Mozilla Public License 2.0.
The license text is available at <https://www.mozilla.org/MPL/2.0/>. XMesh's
own source files remain under the repository's MIT license.
