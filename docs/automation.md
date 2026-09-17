# Deployment automation (v0.2.2)

This guide targets the v0.2.2 Controller and node binaries. Upgrade an
existing Controller first and set its `release_version` to `v0.2.2`. The v0.2.0
Controller does not offer the new panel controls. Node installers obtain
verified v0.2.2 assets from GitHub or the Controller's on-demand cache.
The v0.2.1 Controller's on-demand cache rejects its release manifest and
returns 502; upgrade the Controller before selecting a cached install command.

## Install nodes independently

Create a Gateway and an Agent separately in the Controller panel. Each node has
an **Install Gateway** or **Install Agent** button that generates a one-time
token and verified systemd/Docker Compose commands. Copy the chosen command to
the corresponding host, then enter the token at its prompt. The Controller
does not SSH into hosts. Tokens expire after 30 minutes and a newer unused
token revokes the previous unused token for the same node.

The installers check required local tools and Controller HTTPS before changing
the host. New Gateway installations also check that TCP 8080 and 8443 are free
when `ss` is available. Firewall reachability from other hosts must still be
verified in the actual network. Node readiness in the panel distinguishes
enrollment, online status, configuration, Xray, and Link state and suggests the
next check. An installed node without a Link is not yet a usable route.

## Assign routes and open subscriptions

Select one Agent and several Gateways in **Assign multiple Gateways to an
Agent**. The Controller creates each missing attachment and REALITY Link in one
transaction. Existing routes are left untouched. A Gateway keeps one REALITY
target; newly selected Gateways use the target entered in that form. The Agent
initiates a separate connection to each Gateway. A multi-container test runs
two Gateway containers, one Agent, two client containers, Controller, and a
target, checking TCP and UDP through both REALITY routes.

In **Open VMess/WS subscription**, select one user and only the routes they
should access. The Controller creates missing Grants in one transaction. The
panel shows how many entries are published and offers a copy button. A new
Grant appears in the subscription only after its Gateway applies the updated
configuration. The subscription URL is a bearer secret; use HTTPS, do not put
it in logs, and use **Reset link** if it leaks. Users connect to Gateway via
VMess/WS; REALITY is only the Gateway-to-Agent transport.

## Upgrade, rotate, and back up

The panel generates fixed-version upgrade commands for existing nodes. The
Docker installer preserves the previous image and Compose definition, checks
container startup plus the node-reported binary version, and restores the
previous image on failure. The systemd installer restarts and checks the
reported version, restoring its previous binaries on failure. Link health is
separate: an offline peer is not itself grounds for rolling back an otherwise
healthy node upgrade.
Upgrade the Controller first so `/api/v1/self/status` is available to the node
installers, then upgrade Gateways and Agents.

To rotate a node credential, generate a new enrollment token for that node and
use the **rotate** command shown on the result page. The installer updates the
same node identity atomically. The previous credential has a 15-minute grace
period and is revoked as soon as the new node reports status. Unused tokens can
be revoked from the panel. Subscription links can be reset separately.

On a Docker Controller host with a v0.2.2 source checkout, run:

```sh
sudo sh scripts/backup-controller.sh /opt/xmesh-docker-controller /var/backups/xmesh-controller
```

The helper is also a v0.2.2 release asset and is available from the
Controller's on-demand `/releases/v0.2.2/backup-controller.sh` URL. If the
source checkout is absent, download the helper and `SHA256SUMS` from the same
release source, verify the helper with `sha256sum -c`, then run it as root.

The script archives `config/controller.json` and `data/controller-state.json`,
extracts them to a temporary directory to check readability and equality, and
generates a SHA-256 checksum. Store both files off-host and restrict access:
they contain keys and credentials. Restore is deliberately manual; test it on
an isolated host before relying on a backup. Release binaries and the cache can
be downloaded again and are not part of the backup.
