# CM protobuf provenance

The minimal schemas in `steam_base.proto` and `steam_cm.proto` are derived
from:

* Repository: `https://github.com/SteamTracking/Protobufs`
* Upstream commit: `4db94f3b2f432be2809399b7b6c6dd17ea452bcd`
* Source files:
  `steam/steammessages_base.proto`,
  `steam/steammessages_clientserver_login.proto`
* Imported: 2026-09-25

Only fields required by the CM discovery runtime are retained.
Do not replace this file with an unreviewed regeneration
of the complete upstream schema.

Authentication messages and the additional user-logon fields are a reviewed
subset derived from:

* Repository: `https://github.com/SteamTracking/Protobufs`
* Upstream commit: `4db94f3b2f432be2809399b7b6c6dd17ea452bcd`
* Source files:
  `steam/steammessages_auth.steamclient.proto`,
  `steam/steammessages_clientserver_login.proto`,
  `steam/enums_clientserver.proto`
* Imported: 2026-09-26

Only the password RSA key, credential and QR authentication, Steam Guard code,
authentication polling and individual SteamClient logon fields are retained.
UM service RPC names and EMsg identifiers
are not represented by the generated Go message descriptors.
RPC names were checked against the Authentication service declaration.
`ClientHello` (9805), `ServiceMethodCallFromClientNonAuthed` (9804),
`ServiceMethodCallFromClient` (151),
`ServiceMethodResponse` (147), `ClientLogon` (5514),
`ClientLogOnResponse` (751), `ClientLogOff` (706) and `ClientLoggedOff` (757)
were checked against the EMsg enum at that commit.

`CMsgIPAddress` is kept in `steam_base.proto` because it is shared
by the CM logon and legacy GMS query subsets.
The anonymous logon subset includes the current client fields
for obfuscated private IP, chat mode, machine name and rate-limit capability
so their protobuf field presence matches the known anonymous client.

The gameserver schemas are a reviewed subset derived from:

* Repository: `https://github.com/ValvePython/steam`
* Source files:
  `protobufs/steammessages_base.proto`,
  `protobufs/steammessages_clientserver_gameservers.proto`,
  `protobufs/steammessages_gameservers.proto`
* Imported: 2026-09-25

The current SteamTracking schema contains `ClientGMSServerQuery`
and the `GameServers.MasterServerQuery` Unified Messaging wrapper.
The local schema also retains the historical
`GameServers.GetServerList` messages, which are not present
in the current SteamTracking gameserver service schema.

The direct GMS messages are kept as a minimal reviewed subset.
Newer fields are added when required by the current protocol,
but unused fields are not copied automatically from upstream.

Anonymous CM logon uses the public anonymous-user SteamID
in the protobuf header and a platform-specific client OS type.
`GameServers.GetServerList` remains historical/experimental
because current CM endpoints may reject
or ignore it even when anonymous logon succeeds.
`GameServers.MasterServerQuery` is implemented
as a separate current Unified Messaging route;
availability may vary by CM endpoint.

Generation tooling used for the authentication subset:

* `protoc`: libprotoc 36.2
* `protoc-gen-go`: v1.36.12
* Go toolchain used for implementation checks: go1.27.0 windows/amd64
