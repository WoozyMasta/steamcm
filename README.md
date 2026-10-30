# steamcm

[`steamcm`][pkg] is a Go client for
Steam Connection Manager (CM) WebSocket sessions.
It provides CM framing and jobs, Steam authentication,
and gameserver query methods.

> [!WARNING]
> CM session and gameserver methods are not a documented,
> stable Steamworks API.
> Steam may change or restrict them without notice.

## Installation

Add the module to your project:

```sh
go get github.com/woozymasta/steamcm@latest
```

## Usage

Authenticate with the `auth` package or another trusted Steam flow,
then use the resulting refresh token to create an individual CM session.

> [!NOTE]
> A Steam Web API key does not authenticate a CM session.

```go
import (
  "context"
  "errors"

  cm "github.com/woozymasta/steamcm"
  "github.com/woozymasta/steamcm/auth"
  "github.com/woozymasta/steamcm/gameservers"
)

func queryServers(
  ctx context.Context,
  token auth.Token,
) ([]gameservers.Endpoint, error) {
  directory := &cm.Directory{}
  endpoints, err := directory.Get(ctx, cm.DirectoryQuery{
    MaxCount: 5,
  })
  if err != nil {
    return nil, err
  }
  if len(endpoints) == 0 {
    return nil, errors.New("Steam CM directory returned no endpoints")
  }

  client, err := cm.New()
  if err != nil {
    return nil, err
  }
  defer client.Close()

  if err := client.Connect(ctx, endpoints[0]); err != nil {
    return nil, err
  }
  session, err := client.LogonToken(ctx, cm.TokenLogonDetails{
    SteamID: token.SteamID, AccountName: token.AccountName,
    RefreshToken: token.RefreshToken,
  })
  if err != nil {
    return nil, err
  }

  servers, err := gameservers.New(session)
  if err != nil {
    return nil, err
  }
  return servers.MasterServerQuery(ctx, gameservers.QueryRequest{
    AppID: 221100, Filter: `\appid\221100`, Limit: 100,
  })
}
```

`LogonAnonymous` is available for CM sessions,
but gameserver query methods require an authenticated session.
The `auth` package exposes password- and QR-based login steps,
Steam Guard code submission, and polling.

## Gameserver discovery

`gameservers.List` requests rich records
through the historical `GameServers.GetServerList` method.
Some CM endpoints may reject or ignore that method.
`Query` and `MasterServerQuery` return server endpoints;
use them when addresses are sufficient.

CM does not expose a continuation cursor,
and an individual response is limited to about 10,000 entries.
`Discover` partitions an AppID-root query using observed map, version,
and game-type metadata, then returns unique endpoints and a reusable plan.
`Collect` runs that plan again on later polls.
When callers need server metadata,
`DiscoverServers` and `CollectServers` return one latest record per address;
these methods retain more data in memory
and do not require a diagnostics callback.
The planner is bounded and may return partial results:

```go
result, err := servers.DiscoverServers(ctx, gameservers.QueryRequest{
  AppID: 221100,
}, gameservers.DiscoveryOptions{MaxRequests: 24})
if err != nil {
  return err
}
if !result.Status.Complete {
  // Inspect Status and retain the plan; this run did not drain every branch.
}

_, err = servers.CollectServers(ctx, result.Plan, gameservers.CollectionOptions{})
if err != nil {
  return err
}
```

Discovery is unpaced by default.
Request pacing and the one-time initial burst are caller-controlled.
For repeated polling, the package's suggested starting values
are a burst of 3 and 60 seconds between later request starts;
observed workloads have varied, so these are not guaranteed Steam limits.
A complete result describes sampled filter coverage,
not a consistent global snapshot;
CM responses can overlap or change between requests.

## Related modules

* [`serverlist`][serverlist] calls Steam's Web API
  `IGameServersService/GetServerList` endpoint and requires a Web API key.
* [`msqp`][msqp] implements the legacy UDP Master Server Query Protocol.

These modules use different Steam interfaces and have separate data contracts.

## Documentation and tests

* [Steamworks: game servers][steamworks-gameservers]
* [Steamworks: ISteamMatchmakingServers][steamworks-matchmaking]
* [`steamcm` on pkg.go.dev][pkg]
* [`steamcm/auth` on pkg.go.dev][pkg-auth]
* [`steamcm/gameservers` on pkg.go.dev][pkg-gameservers]

Run local tests with:

```sh
go test ./...
```

Authenticated CM integration tests are opt-in.
Run them with the `cm_auth_live` build tag
and `STEAMCM_AUTH_TOKEN_FILE` pointing to an owner-provided refresh-token file.

<!-- links -->

[pkg]: https://pkg.go.dev/github.com/woozymasta/steamcm
[pkg-auth]: https://pkg.go.dev/github.com/woozymasta/steamcm/auth
[pkg-gameservers]: https://pkg.go.dev/github.com/woozymasta/steamcm/gameservers
[steamworks-gameservers]: https://partner.steamgames.com/doc/features/multiplayer/game_servers
[steamworks-matchmaking]: https://partner.steamgames.com/doc/api/ISteamMatchmakingServers
[serverlist]: https://github.com/woozymasta/steamserverlist
[msqp]: https://github.com/woozymasta/msqp
